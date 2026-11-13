package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// Work-history callbacks are optional reads, not unbounded scans. A full final
// page leaves completeness unknown, so exhaustion must never mean "not found".
const (
	botWorkHistoryPageSize = 100
	botWorkHistoryMaxPages = 20
	botWorkHistoryTimeout  = 2 * time.Second
)

var errBotWorkHistoryExhausted = errors.New("Bot work history lookup limit exhausted; result is incomplete")

// BotChatScope is loaded from trusted owner/tenant storage, never a viewing
// member's HTTP Services. Sessions must wrap the existing run's scoped store;
// Models must resolve its session pin without swapping a shared provider.
// Cleanup releases per-attempt resources, not the persistent session or Bot home.
type BotChatScope struct {
	// OwnerEmail reads fresh account/link facts; it neither grants nor sends.
	OwnerEmail func(context.Context) (store.BotOwnerEmailDelivery, error)
	Services   *store.Services
	Sessions   session.Service
	Models     bot.ModelResolver
	Cleanup    func()
}

// BotChatSafeResources contains only explicitly supplied, mechanically guarded
// tools. There is intentionally no native-tool, browser, MCP or daemon-host
// fallback. Prepare must provision/verify the run's sandbox and network policy
// before returning; Tools must never fall back to host execution. It must return
// partial resources with Cleanup on failure. Empty Tools is a valid toolless run.
type BotChatSafeResources struct {
	// ChatConfig and Catalog are trusted owner-scoped Chat dependencies, never host defaults.
	ChatConfig    *config.AppConfig
	Catalog       *ChatCatalogResources
	Tools         []tool.Tool
	WorkspaceDir  string
	PolicyRuntime agent.BotPolicyRuntime
	// VerifyIsolation checks the current lease-bound sandbox/network binding.
	// It is mandatory even for a toolless run (where it may verify deny-all).
	VerifyIsolation func(context.Context) error
	Cleanup         func()
}

// BotChatFactoryConfig is an opt-in composition boundary, not daemon readiness.
// LoadScope must authenticate live owner membership and load owner-scoped stores.
// WrapModel must install reservation/settlement against req.Budget (including
// auxiliary and child calls). It must not return an unbudgeted passthrough in
// production. Prepare must not use global credentials or host fallback tools.
// Install the same live BotPolicyRuntime on the worker context BEFORE calling
// api.BotChatRuntimeAdapter.Start, so ChatRunner's pre-tool checks also see it.
// No callbacks here authorize enabling a daemon supervisor automatically.
type BotChatFactoryConfig struct {
	LoadScope func(context.Context, bot.RunRequest) (*BotChatScope, error)
	Prepare   func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error)
	WrapModel func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error)
	// ContextWindow is explicitly configured: no network model-metadata fallback.
	ContextWindow int
}

// BotChatModelResolver resolves pins using the already owner-scoped reader.
// ResolveProvider must use request-local provider configuration and never swap a
// daemon-global LLM. ResolveInherited handles an unpinned session explicitly;
// omitting it fails closed rather than selecting a host default.
type BotChatModelResolver struct {
	SessionPin       func(context.Context, string) (*store.SessionPin, error)
	ResolveProvider  func(context.Context, string, string) (model.LLM, error)
	ResolveInherited func(context.Context, uuid.UUID) (bot.ResolvedModel, error)
}

func (r BotChatModelResolver) Resolve(ctx context.Context, id uuid.UUID) (bot.ResolvedModel, error) {
	if r.SessionPin == nil || r.ResolveProvider == nil || id == uuid.Nil {
		return bot.ResolvedModel{}, store.ErrBotUnsupported
	}
	pin, err := r.SessionPin(ctx, id.String())
	if err != nil {
		return bot.ResolvedModel{}, err
	}
	if pin == nil || pin.Provider == "" || pin.Model == "" {
		if r.ResolveInherited == nil {
			return bot.ResolvedModel{}, store.ErrBotUnsupported
		}
		// A partial pin must be resolved explicitly, not silently discarded.
		if pin != nil && (pin.Provider != "" || pin.Model != "") {
			return bot.ResolvedModel{}, store.ErrBotInvalid
		}
		return r.ResolveInherited(ctx, id)
	}
	llm, err := r.ResolveProvider(ctx, pin.Provider, pin.Model)
	if err != nil {
		return bot.ResolvedModel{}, err
	}
	if llm == nil {
		return bot.ResolvedModel{}, store.ErrBotUnsupported
	}
	return bot.ResolvedModel{Provider: pin.Provider, Model: pin.Model, LLM: llm}, nil
}

// BotChatServicesForOwner builds a new dependency container from an already
// authorized organization handle. Call it in LoadScope after live membership
// authorization, using req.Principal and req.Bot.Scope, never HTTP viewer data.
// It performs no provisioning. Team Bots do not even open the owner's private
// store; personal Bots use only the accountable owner's private store.
func BotChatServicesForOwner(org store.OrgDataStore, principal execution.Principal, scope store.BotScope) (*store.Services, error) {
	if org == nil || principal.Validate() != nil || principal.Kind != execution.PrincipalKindUser || principal.OrgSlug == "" || principal.TeamSlug == "" {
		return nil, store.ErrBotForbidden
	}
	if scope != store.BotScopePersonal && scope != store.BotScopeTeam {
		return nil, store.ErrBotInvalid
	}
	team := org.ForTeam(principal.TeamSlug)
	if team == nil {
		return nil, store.ErrBotUnsupported
	}
	svc := &store.Services{Mode: store.ModePlatform, Scheduler: team.ScheduledJobs(), Sessions: team.Sessions(), Bots: team.Bots(), Credentials: team.Credentials(), Memory: team.Memories(), Flows: team.Flows(), Docs: team.Docs(), Apps: team.Apps(), TeamSkills: team.Skills(), TeamMCPServers: team.MCPServers(), TeamNetworkPolicies: team.NetworkPolicies(), Settings: team.Settings()}
	if scope == store.BotScopePersonal {
		personal := org.ForUser(principal.Subject)
		if personal == nil {
			return nil, store.ErrBotUnsupported
		}
		svc.PersonalSessions, svc.PersonalBots, svc.PersonalCredentials = personal.Sessions(), personal.Bots(), personal.Credentials()
		svc.PersonalFlows, svc.PersonalDocs, svc.PersonalApps = personal.Flows(), personal.Docs(), personal.Apps()
		svc.PersonalScheduler = personal.ScheduledJobs()
		svc.PersonalSettings = personal.PersonalSettings()
		svc.Memory = personal.Memories()
	}
	return svc, nil
}

// NewBotChatAgentFactory constructs fresh wired resources for each invocation.
// Compose its result with api.NewBotChatRuntimeAdapter after installing the live
// guard/budget infrastructure. Construction never creates or reparents sessions.
func NewBotChatAgentFactory(cfg BotChatFactoryConfig) (api.BotChatAgentFactory, error) {
	if cfg.LoadScope == nil || cfg.Prepare == nil || cfg.WrapModel == nil || cfg.ContextWindow <= 0 {
		return nil, fmt.Errorf("%w: explicit Bot scope, isolation, budget wrapper and context window required", store.ErrBotUnsupported)
	}
	return func(ctx context.Context, req bot.RunRequest) (resources *api.BotChatResources, err error) {
		if err = validateBotFactoryRequest(ctx, req); err != nil {
			return nil, err
		}
		// Keep cancellation/deadlines, discard all ambient viewer/service/LLM values.
		owner, cancelOwner := context.WithCancel(botFactoryOwnerContext(ctx, req))
		// The adapter installs this server-only ledger before factory construction.
		// Carry it explicitly without restoring any ambient viewer values.
		owner = bot.WithToolOutcomesFromContext(owner, ctx)
		owner = bot.WithBotWorkServicesFromContext(owner, ctx)
		cleanups := []func(){cancelOwner}
		var once sync.Once
		cleanup := func() {
			once.Do(func() {
				for i := len(cleanups) - 1; i >= 0; i-- {
					cleanups[i]()
				}
			})
		}
		defer func() {
			if resources == nil {
				cleanup()
			}
		}()
		scoped, err := cfg.LoadScope(owner, req)
		if scoped != nil && scoped.Cleanup != nil {
			cleanups = append(cleanups, scoped.Cleanup)
		}
		if err != nil {
			return nil, err
		}
		if scoped == nil || scoped.Services == nil || scoped.Sessions == nil || scoped.Models == nil || scoped.Cleanup == nil {
			return nil, store.ErrBotUnsupported
		}
		// Copy the container so later viewer mutations cannot replace its stores.
		services := *scoped.Services
		if req.Bot.Scope == store.BotScopeTeam {
			// Team automation never consumes ANY personal credentials/settings/stores.
			services.PersonalCredentials = nil
			services.PersonalSessions = nil
			services.PersonalSettings = nil
			services.PersonalBots = nil
			services.PersonalDocs = nil
			services.PersonalApps = nil
			services.PersonalFlows = nil
			services.PersonalScheduler = nil
		}
		scopeCopy := *scoped
		scopeCopy.Services = &services
		scoped = &scopeCopy
		owner = store.WithServices(owner, &services)
		creds := store.NewMergedCredentialStore(services.PersonalCredentials, services.Credentials)
		owner = store.WithCredentialStore(owner, creds)
		owner = withoutGenericBotMemory(owner)
		owner = store.WithFlowStore(owner, services.PersonalFlows)
		owner = store.WithTeamFlowStore(owner, services.Flows)
		existing, err := scoped.Sessions.Get(owner, &session.GetRequest{AppName: "astonish", UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
		if err != nil {
			return nil, err
		}
		if existing == nil || existing.Session == nil || existing.Session.ID() != req.Run.SessionID.String() || existing.Session.UserID() != req.Bot.OwnerID.String() || existing.Session.AppName() != "astonish" {
			return nil, store.ErrBotForbidden
		}
		resolved, err := scoped.Models.Resolve(owner, req.Run.SessionID)
		if err != nil {
			return nil, err
		}
		if resolved.LLM == nil || strings.TrimSpace(resolved.Provider) == "" || strings.TrimSpace(resolved.Model) == "" {
			return nil, store.ErrBotUnsupported
		}
		wrapped, err := cfg.WrapModel(owner, resolved.LLM, req.Budget)
		if err != nil {
			return nil, err
		}
		if wrapped == nil {
			return nil, store.ErrBotUnsupported
		}
		var safe *BotChatSafeResources
		if req.Run.ControlOnly() && !botFactoryInteractive(req) && req.Mandate == nil {
			// No preparation callback may provision a sandbox or external tools for
			// a control-only conversation. Identity, lease and model budgets remain bound.
			safe = &BotChatSafeResources{WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntimeFromContext(ctx), VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}
		} else {
			safe, err = cfg.Prepare(owner, req, scoped)
		}
		if safe != nil && safe.Cleanup != nil {
			cleanups = append(cleanups, safe.Cleanup)
		}
		if err != nil {
			return nil, err
		}
		if safe == nil || safe.Cleanup == nil || safe.VerifyIsolation == nil || (!botFactoryInteractive(req) && (safe.PolicyRuntime.Guard == nil || safe.PolicyRuntime.Classifier == nil)) || safe.WorkspaceDir == "" {
			return nil, store.ErrBotUnsupported
		}
		if err = safe.VerifyIsolation(owner); err != nil {
			return nil, err
		}
		if err = validateBotFactoryRequest(ctx, req); err != nil {
			return nil, err
		}
		if !botFactoryInteractive(req) {
			safe.PolicyRuntime.Classifier = WithBotMandatedReadClassifier(safe.PolicyRuntime.Classifier, safe.Tools)
			safe.PolicyRuntime.Classifier = WithBotEmailClassifier(safe.PolicyRuntime.Classifier, safe.Tools)
			safe.PolicyRuntime.Classifier = WithBotMCPClassifier(safe.PolicyRuntime.Classifier, safe.Tools)
			safe.PolicyRuntime, err = api.BindBotChatPolicyRuntime(ctx, safe.PolicyRuntime)
			if err != nil {
				return nil, err
			}
			owner = agent.WithBotPolicyRuntime(owner, safe.PolicyRuntime)
		}
		binding := &botFactoryBinding{owner: owner, req: req, ownerEmail: scoped.OwnerEmail, verifyIsolation: safe.VerifyIsolation}
		llm := &botFactoryLLM{LLM: wrapped, binding: binding}
		// Bound tool contexts resolve values from owner, not the caller; install
		// the same metered auxiliary capability there before publishing resources.
		owner = agent.WithBotAuxiliaryLLM(owner, llm)
		binding.owner = owner
		injection := &botWiredInjection{scope: scoped, safe: safe, binding: binding, llm: llm, contextWindow: cfg.ContextWindow, credentials: creds}
		wired, err := NewWiredChatAgent(context.WithValue(owner, botWiredKey{}, injection), &ChatFactoryConfig{AppConfig: &config.AppConfig{}, ProviderName: resolved.Provider, ModelName: resolved.Model, PlatformMode: true, SessionService: scoped.Sessions})
		if wired != nil && wired.Cleanup != nil {
			cleanups = append(cleanups, wired.Cleanup)
		}
		if err != nil {
			return nil, err
		}
		repo := services.Bots
		if req.Bot.Scope == store.BotScopePersonal {
			repo = services.PersonalBots
		}
		transcript, _ := repo.(store.BotTranscriptStore)
		work, _ := repo.(store.BotWorkStore)
		skills, _ := repo.(store.BotSkillStore)
		var redact func(string) string
		if redactor := credentials.RedactorFromContext(binding.owner); redactor != nil {
			redact = redactor.Redact
		}
		if req.Work != nil && work == nil {
			return nil, fmt.Errorf("%w: pinned Bot work store unavailable", store.ErrBotUnsupported)
		}
		sessionStore := services.Sessions
		if req.Bot.Scope == store.BotScopePersonal {
			sessionStore = services.PersonalSessions
		}
		evidenceReader, _ := sessionStore.(store.BotTranscriptReader)
		previousWork := func(c context.Context) (*store.BotWorkProjection, error) {
			bound, cancel := context.WithTimeout(binding.bind(c), botWorkHistoryTimeout)
			defer cancel()
			if err := binding.check(bound); err != nil {
				return nil, err
			}
			var latest *store.BotRun
			cursor := uuid.Nil
			for page := 0; page < botWorkHistoryMaxPages; page++ {
				if err := bound.Err(); err != nil {
					return nil, err
				}
				rows, err := repo.ListRuns(bound, req.Bot.ID, store.BotPage{Limit: botWorkHistoryPageSize, After: cursor.String()})
				if err != nil {
					return nil, err
				}
				if err := bound.Err(); err != nil {
					return nil, err
				}
				if len(rows) > botWorkHistoryPageSize {
					return nil, fmt.Errorf("%w: oversized run history page", store.ErrBotInvalid)
				}
				for i := range rows {
					run := rows[i]
					if bytes.Compare(run.ID[:], cursor[:]) <= 0 {
						return nil, fmt.Errorf("%w: run history cursor did not advance", store.ErrBotInvalid)
					}
					cursor = run.ID
					if run.ID != req.Run.ID && run.FinishedAt != nil && run.FinishedAt.Before(req.Run.CreatedAt) && run.Report.Work != nil && (latest == nil || run.FinishedAt.After(*latest.FinishedAt)) {
						latest = &run
					}
				}
				if len(rows) < botWorkHistoryPageSize {
					if latest == nil {
						return nil, nil
					}
					return latest.Report.Work, nil
				}
			}
			return nil, fmt.Errorf("previous work: %w", errBotWorkHistoryExhausted)
		}
		continuation := func(c context.Context, kind store.BotWorkContinuationKind, id uuid.UUID) error {
			bound, cancel := context.WithTimeout(binding.bind(c), botWorkHistoryTimeout)
			defer cancel()
			if err := binding.check(bound); err != nil {
				return err
			}
			switch kind {
			case store.BotWorkContinueExistingSchedule:
				current, err := repo.Get(bound, req.Bot.ID)
				if err != nil {
					return err
				}
				version, err := repo.GetVersion(bound, req.Bot.ID, current.Version)
				if err != nil {
					return err
				}
				if err := bound.Err(); err != nil {
					return err
				}
				for _, trigger := range version.Triggers {
					if trigger.ID == id && trigger.Enabled {
						return nil
					}
				}
			case store.BotWorkContinueExistingApproval:
				cursor := uuid.Nil
				for page := 0; page < botWorkHistoryMaxPages; page++ {
					if err := bound.Err(); err != nil {
						return err
					}
					rows, err := repo.ListApprovals(bound, req.Bot.ID, store.BotPage{Limit: botWorkHistoryPageSize, After: cursor.String()})
					if err != nil {
						return err
					}
					if err := bound.Err(); err != nil {
						return err
					}
					if len(rows) > botWorkHistoryPageSize {
						return fmt.Errorf("%w: oversized approval history page", store.ErrBotInvalid)
					}
					for _, approval := range rows {
						if bytes.Compare(approval.ID[:], cursor[:]) <= 0 {
							return fmt.Errorf("%w: approval history cursor did not advance", store.ErrBotInvalid)
						}
						cursor = approval.ID
						if approval.ID == id && approval.RunID == req.Run.ID && (approval.Status == store.ApprovalPending || approval.Status == store.ApprovalApproved) && time.Now().Before(approval.ExpiresAt) {
							return nil
						}
					}
					if len(rows) < botWorkHistoryPageSize {
						return store.ErrBotForbidden
					}
				}
				return fmt.Errorf("approval continuation: %w", errBotWorkHistoryExhausted)
			}
			return store.ErrBotForbidden
		}
		return &api.BotChatResources{ModelSelection: &store.SessionPin{Provider: resolved.Provider, Model: resolved.Model}, PreviousWork: previousWork, ValidateWorkContinuation: continuation, EvidenceReader: evidenceReader, AssessmentContext: binding.assessmentContext, Skills: skills, Redact: redact, Work: work, Authorization: req.Authorization, Transcript: transcript, Agent: wired.ChatAgent, SessionService: scoped.Sessions, AppName: "astonish", Cleanup: cleanup, PolicyRuntime: &safe.PolicyRuntime}, nil
	}, nil
}

// botFactoryInteractive selects ephemeral turn authority only when no durable
// authority was verified. A saved interactive admission flag is not a registry dependency.
func botFactoryInteractive(req bot.RunRequest) bool {
	return req.Run.IsInteractiveChat() && req.Mandate == nil
}

func validateBotFactoryRequest(ctx context.Context, req bot.RunRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, ok := execution.PrincipalFromContext(ctx)
	id, identified := execution.BotRunIdentityFromContext(ctx)
	want := execution.BotRunIdentity{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Version.Version, ActorID: req.Bot.OwnerID.String(), OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || !reflect.DeepEqual(p, req.Principal) || p.Subject != req.Bot.OwnerID.String() || !identified || id != want || want.Validate() != nil || !req.Run.ExecutionMode.Valid() || (req.Run.RequiresAutonomousPolicy() && req.Policy == nil) || (req.Policy != nil && req.Policy.Identity() != want) || execution.RunPolicyFromContext(ctx) != req.Policy {
		return store.ErrBotForbidden
	}
	if req.Bot.Scope != store.BotScopePersonal && req.Bot.Scope != store.BotScopeTeam {
		return store.ErrBotInvalid
	}
	if !req.Run.Intent.AllowsStatus(req.Bot.Status) || req.Run.Status != store.RunRunning || req.Run.BotID != req.Bot.ID || req.Version.BotID != req.Bot.ID || req.Run.Version != req.Version.Version || req.Bot.Version != req.Version.Version || req.Run.SessionID == uuid.Nil || (!botFactoryInteractive(req) && req.Interactive) {
		return store.ErrBotInvalid
	}
	if req.Run.MandateRef != nil && req.Mandate == nil {
		return store.ErrBotForbidden
	}
	if req.Mandate != nil {
		if req.Policy == nil || req.Run.MandateRef == nil || *req.Run.MandateRef != req.Mandate.Authorization.Ref() || req.Mandate.Identity != want || !reflect.DeepEqual(req.Mandate.Authorization.Principal(p.Surface), p) {
			return store.ErrBotForbidden
		}
		if err := req.Mandate.Check(ctx); err != nil {
			return err
		}
	}
	if botFactoryInteractive(req) {
		if req.Run.CallerCeiling == nil || !reflect.DeepEqual(req.Run.CallerCeiling.Principal, req.Principal) || !time.Now().Before(req.Run.CallerCeiling.ExpiresAt) || req.Authorization == nil {
			return store.ErrBotForbidden
		}
		if err := req.Authorization.Authorize(ctx, execution.CapabilityChat); err != nil {
			return err
		}
	}
	if len(req.Run.Checkpoint) != 0 {
		checkpoint, err := bot.DecodeApprovalCheckpoint(req.Run.Checkpoint, req.Run)
		if err != nil {
			return err
		}
		rs, ok := bot.ApprovalStoreFromContext(ctx).(store.BotApprovalResumeStore)
		if !ok {
			return store.ErrBotUnsupported
		}
		approval, err := rs.LoadResumeApproval(ctx, req.Run.Lease)
		if err != nil {
			return err
		}
		if approval == nil || !checkpoint.Matches(*approval) || checkpoint.Fence >= req.Run.Lease.Fence {
			return store.ErrBotConflict
		}
	}
	l := req.Run.Lease
	if l.BotID != req.Bot.ID || l.RunID != req.Run.ID || l.Fence <= 0 || l.WorkerID == "" || !time.Now().Before(l.ExpiresAt) {
		return store.ErrBotConflict
	}
	if req.Budget == nil || api.BotChatRunBudgetFromContext(ctx) != req.Budget {
		return store.ErrBotUnsupported
	}
	return req.Budget.Check(ctx)
}

// botValueFreeContext retains lifetime without inheriting caller credentials,
// services, model overrides or other request-local values.
type botValueFreeContext struct{ context.Context }

func (botValueFreeContext) Value(any) any { return nil }
func botFactoryOwnerContext(ctx context.Context, req bot.RunRequest) context.Context {
	owner, _ := execution.WithPrincipal(botValueFreeContext{ctx}, req.Principal)
	if bot.HasInteractionOwner(ctx) {
		owner = bot.WithInteractionOwner(owner)
	}
	owner = execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Version.Version, ActorID: req.Principal.Subject, OrgSlug: req.Principal.OrgSlug, TeamSlug: req.Principal.TeamSlug})
	owner = execution.WithRunPolicy(owner, req.Policy)
	if botFactoryInteractive(req) {
		owner = execution.WithBotTurnAuthorizer(owner, req.Authorization)
	}
	owner = store.WithUserID(owner, req.Bot.OwnerID.String())
	owner = store.WithOrgSlug(owner, req.Principal.OrgSlug)
	owner = store.WithTeamSlug(owner, req.Principal.TeamSlug)
	return store.WithSessionID(owner, req.Run.SessionID.String())
}

type botWiredKey struct{}
type botWiredInjection struct {
	scope         *BotChatScope
	safe          *BotChatSafeResources
	binding       *botFactoryBinding
	llm           model.LLM
	contextWindow int
	credentials   store.CredentialStore
}
type botFactoryBinding struct {
	ownerEmail      func(context.Context) (store.BotOwnerEmailDelivery, error)
	owner           context.Context
	req             bot.RunRequest
	verifyIsolation func(context.Context) error
}

func (b *botFactoryBinding) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.owner.Err(); err != nil {
		return err
	}
	// The request contains the initial lease snapshot. Renewals extend the
	// authoritative lease without mutating that request; validate its identity
	// and fence against owner-scoped storage on every invocation.
	svc := store.FromContext(b.owner)
	if svc == nil {
		return store.ErrBotUnsupported
	}
	repo := svc.Bots
	if b.req.Bot.Scope == store.BotScopePersonal {
		repo = svc.PersonalBots
	}
	if repo == nil {
		return store.ErrBotUnsupported
	}
	bound := b.bind(ctx)
	current, err := repo.Get(bound, b.req.Bot.ID)
	if err != nil {
		return err
	}
	if current == nil || current.ID != b.req.Bot.ID || current.OwnerID != b.req.Bot.OwnerID || current.Scope != b.req.Bot.Scope || !b.req.Run.Intent.AllowsStatus(current.Status) || current.Version != b.req.Version.Version {
		return store.ErrBotConflict
	}
	run, err := repo.GetRun(bound, b.req.Bot.ID, b.req.Run.ID)
	if err != nil {
		return err
	}
	lease := b.req.Run.Lease
	if run == nil || run.ID != b.req.Run.ID || run.BotID != b.req.Bot.ID || run.Status != store.RunRunning || run.Intent != b.req.Run.Intent || run.Version != b.req.Version.Version || run.SessionID != b.req.Run.SessionID || run.Lease.BotID != lease.BotID || run.Lease.RunID != lease.RunID || run.Lease.WorkerID != lease.WorkerID || run.Lease.Fence != lease.Fence || !time.Now().Before(run.Lease.ExpiresAt) {
		return store.ErrBotConflict
	}
	if err := b.req.Budget.Check(ctx); err != nil {
		return err
	}
	if b.req.Mandate != nil {
		if err := b.req.Mandate.Check(b.bind(ctx)); err != nil {
			return err
		}
	}
	if botFactoryInteractive(b.req) {
		if b.req.Authorization == nil {
			return store.ErrBotForbidden
		}
		if err := b.req.Authorization.Authorize(b.bind(ctx), execution.CapabilityChat); err != nil {
			return err
		}
	}
	return b.verifyIsolation(ctx)
}

// bind keeps invocation cancellation and narrowed child policy, but resolves
// every other value from the captured owner context, never from a later viewer.
func (b *botFactoryBinding) bind(ctx context.Context) context.Context {
	policy := execution.RunPolicyFromContext(ctx)
	if policy == nil || policy.Identity() != b.req.Policy.Identity() {
		policy = b.req.Policy
	}
	return execution.WithRunPolicy(botBoundContext{Context: ctx, owner: b.owner}, policy)
}

type botBoundContext struct {
	context.Context
	owner context.Context
}

func (c botBoundContext) Value(key any) any { return c.owner.Value(key) }

type botFactoryLLM struct {
	model.LLM
	binding *botFactoryBinding
}

func (l *botFactoryLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// Invocation-only purpose controls prompt composition, never authority.
		// Read before bind deliberately discards later viewer context values.
		purpose := bot.ModelCallPurposeFromContext(ctx)
		bound := l.binding.bind(ctx)
		if err := l.binding.check(bound); err != nil {
			yield(nil, err)
			return
		}
		request := req
		if purpose != bot.ModelCallPurposeAssessment {
			var err error
			request, err = l.binding.withCurrentControlContext(bound, req)
			if err != nil {
				yield(nil, err)
				return
			}
		}
		// A failed metadata read must not mask revoked execution authority.
		if err := l.binding.check(bound); err != nil {
			yield(nil, err)
			return
		}
		for response, err := range l.LLM.GenerateContent(bound, request, stream) {
			if !yield(response, err) {
				return
			}
		}
	}
}

// currentMemory loads only session-prefiltered Bot rows, with live owner,
// version and lease checks before and after retrieval. No generic fallback.
func (b *botFactoryBinding) currentMemory(ctx context.Context) (string, error) {
	bound := b.bind(ctx)
	if err := b.check(bound); err != nil {
		return "", err
	}
	svc := store.FromContext(bound)
	if svc.Memory == nil {
		return "", nil
	}
	repo := svc.Bots
	if b.req.Bot.Scope == store.BotScopePersonal {
		repo = svc.PersonalBots
	}
	memory, err := store.NewBotRunMemoryService(repo, svc.Memory, b.req.Run.Lease, b.req.Bot.Scope)
	if err != nil {
		return "", err
	}
	rows, err := memory.List(bound, "", 20, 0)
	if err != nil {
		return "", err
	}
	if err := b.check(bound); err != nil {
		return "", err
	}
	text := agent.BotMemoryPrompt(rows)
	if redactor := credentials.RedactorFromContext(bound); redactor != nil {
		text, _ = redactor.Placeholderize(text)
	}
	return text, nil
}

// newBotWiredChatAgent is reached ONLY through NewWiredChatAgent's private
// injection. It deliberately bypasses the legacy host/global initialization.
func newBotWiredChatAgent(ctx context.Context, cfg *ChatFactoryConfig, in *botWiredInjection) (*ChatFactoryResult, error) {
	if botFactoryInteractive(in.binding.req) {
		return newInteractiveBotWiredChatAgent(ctx, cfg, in)
	}
	redactor := credentials.NewRedactor()
	redactor.HydrateFromStore(in.credentials)
	in.binding.owner = credentials.WithRedactor(tools.WithTaskResultStore(in.binding.owner, tools.NewTaskResultStore()), redactor)
	resolver := credentials.NewStoreAdapter(in.credentials)
	mgr := agent.NewSubAgentManager(agent.SubAgentConfig{})
	mgr.LLM, mgr.SessionService, mgr.Redactor, mgr.CredentialStore = in.llm, in.scope.Sessions, redactor, resolver
	mgr.AppName, mgr.UserID = "astonish", in.binding.req.Bot.OwnerID.String()
	mgr.SandboxEnabled, mgr.SandboxWorkspaceDir = true, in.safe.WorkspaceDir
	var native []tool.Tool
	for _, t := range in.safe.Tools {
		if t == nil {
			return nil, store.ErrBotUnsupported
		}
		if in.binding.req.Mandate != nil && in.binding.req.Mandate.Authorization.Authorize(execution.CapabilityToolExecute, t.Name()) != nil {
			continue
		}
		wrapped, err := newBotBoundTool(t, in.binding)
		if err != nil {
			return nil, err
		}
		native = append(native, wrapped)
	}
	workTools, err := newBotWorkTools(in.binding)
	if err != nil {
		return nil, err
	}
	skillTools, err := newBotSkillTools(in.binding, in.scope)
	if err != nil {
		return nil, err
	}
	workTools = append(workTools, skillTools...)
	memoryTools, err := BotMemoryTools(in.binding.req, in.scope)
	if err != nil {
		return nil, err
	}
	for _, t := range memoryTools {
		found := false
		for _, existing := range native {
			if existing.Name() == t.Name() {
				found = true
			}
		}
		if !found {
			workTools = append(workTools, t)
		}
	}
	for _, t := range workTools {
		bound, err := newBotBoundTool(t, in.binding)
		if err != nil {
			return nil, err
		}
		native = append(native, bound)
	}
	var flowCleanups []func()
	for _, t := range in.safe.Tools {
		if d, ok := t.(*botDrillTool); ok {
			d.configure(in.binding, native, in.llm)
		}
		if f, ok := t.(*botFlowTool); ok {
			f.configure(in.llm, native, redactor)
			flowCleanups = append(flowCleanups, f.cleanup)
		}
	}
	mgr.ToolGroups = map[string]*agent.ToolGroup{"bot": {Name: "bot", Description: "Explicitly isolated Bot capabilities", Tools: botChildWorkTools(native)}}
	delegate, err := tools.NewDelegateTasksToolForManager(mgr)
	if err != nil {
		return nil, err
	}
	boundDelegate, err := newBotBoundTool(delegate, in.binding)
	if err != nil {
		return nil, err
	}
	all := append(append([]tool.Tool(nil), native...), boundDelegate)
	memoryText, err := in.binding.currentMemory(ctx)
	if err != nil {
		return nil, err
	}
	prompt := &agent.SystemPromptBuilder{WorkspaceDir: in.safe.WorkspaceDir, SandboxEnabled: true, SandboxWorkspaceDir: in.safe.WorkspaceDir}
	prompt.BuildOverride = func(clone *agent.SystemPromptBuilder) string {
		plain := *clone
		plain.BuildOverride = nil
		return plain.Build() + memoryText
	}
	chat := agent.NewChatAgent(in.llm, nil, nil, in.scope.Sessions, prompt, false, false)
	planTools, err := newBotPlanTools(chat, native, in.safe.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	readResult, err := tools.NewReadTaskResultTool()
	if err != nil {
		return nil, err
	}
	for _, t := range append(planTools, readResult) {
		bound, err := newBotBoundTool(t, in.binding)
		if err != nil {
			return nil, err
		}
		all = append(all, bound)
	}
	if err := restoreBotPlan(ctx, chat, in.scope, in.binding.req.Run.SessionID.String(), in.binding.req.Bot.OwnerID.String()); err != nil {
		return nil, err
	}
	if in.binding.req.Mandate != nil {
		all = botMandateScopedTools(in.binding.req.Mandate, all)
	}
	index := agent.NewLexicalToolIndex()
	if err := index.PrimeTools(ctx, all, nil); err != nil {
		return nil, err
	}
	childIndex := agent.NewLexicalToolIndex()
	if err := childIndex.PrimeTools(ctx, botChildWorkTools(all), nil); err != nil {
		return nil, err
	}
	mgr.ToolIndex = childIndex
	// Use ordinary Chat's progressive discovery/dispatch bridge over the entire
	// guarded catalogue. The bridge resolves bound tools, never raw adapters.
	bridge, err := botProviderTools(index, in.binding)
	if err != nil {
		return nil, err
	}
	providerTools := append(append([]tool.Tool(nil), all...), bridge...)
	// No tool catalogue or discovery bridge in owner conversation mode. Even a
	// hallucinated tool call is denied by the empty run policy and bound dispatch.
	if in.binding.req.Run.ControlOnly() && in.binding.req.Mandate == nil {
		providerTools = nil
		all = nil
	}
	// Capture is deliberately outside external dispatch and its deny-all policy.
	// Reject duplicate names rather than allowing an adapter to impersonate it.
	for _, candidate := range providerTools {
		if candidate.Name() == botProposalToolName {
			return nil, store.ErrBotForbidden
		}
	}
	proposalTool, err := newBotProposalTool(in.binding)
	if err != nil {
		return nil, err
	}
	if proposalTool != nil {
		proposalTool, err = agent.NewBotProposalCaptureTool(proposalTool)
		if err != nil {
			return nil, err
		}
		providerTools = append(providerTools, proposalTool)
		all = append(all, proposalTool)
	}
	prompt.Tools = all
	chat.Tools = providerTools
	chat.ToolIndex, chat.SubAgentManager, chat.Redactor, chat.CredentialStore = index, mgr, redactor, resolver
	chat.PendingSecrets = credentials.NewPendingVault(redactor)
	mgr.PendingSecrets = chat.PendingSecrets
	mgr.EventForwarder = chat.ForwardSubTaskEvent
	mgr.FileArtifactCapture = chat.CaptureFileArtifact
	chat.Compactor = persistentsession.NewCompactor(in.contextWindow)
	// Bind auxiliary calls too; makeLLMFunc's context override must not select an
	// unbudgeted viewer model.
	chat.Compactor.LLM = func(callCtx context.Context, prompt string) (string, error) {
		return makeLLMFunc(in.llm)(in.binding.bind(callCtx), prompt)
	}
	mgr.Compactor = chat.Compactor.Clone()
	done := make(chan struct{})
	close(done)
	return &ChatFactoryResult{ChatAgent: chat, LLM: in.llm, ProviderName: cfg.ProviderName, ModelName: cfg.ModelName, ProviderConfigured: true, Compactor: chat.Compactor, InternalTools: all, SessionService: in.scope.Sessions, PromptBuilder: prompt, IndexingDone: done, Cleanup: func() {
		for _, cleanup := range flowCleanups {
			cleanup()
		}
	}}, nil
}

type botRunnableTool interface {
	tool.Tool
	Declaration() *genai.FunctionDeclaration
	Run(tool.Context, any) (map[string]any, error)
	ProcessRequest(tool.Context, *model.LLMRequest) error
}
type botBoundTool struct {
	botRunnableTool
	binding *botFactoryBinding
}

func newBotBoundTool(t tool.Tool, b *botFactoryBinding) (tool.Tool, error) {
	runnable, ok := t.(botRunnableTool)
	if !ok {
		return nil, fmt.Errorf("%w: unsupported Bot tool %q", store.ErrBotUnsupported, t.Name())
	}
	return &botBoundTool{botRunnableTool: runnable, binding: b}, nil
}

type botBoundToolContext struct {
	tool.Context
	owner context.Context
}

func (c botBoundToolContext) Value(key any) any { return c.owner.Value(key) }
func (t *botBoundTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if t.binding.req.Run.ControlOnly() && t.binding.req.Mandate == nil {
		return nil, store.ErrBotForbidden
	}
	if ctx == nil || ctx.SessionID() == "" {
		return nil, store.ErrBotForbidden
	}
	bound := botBoundToolContext{Context: ctx, owner: t.binding.bind(ctx)}
	if err := t.binding.check(bound); err != nil {
		return nil, err
	}
	if t.binding.req.Mandate != nil {
		if err := t.binding.req.Mandate.Authorization.Authorize(execution.CapabilityToolExecute, t.Name()); err != nil {
			return nil, err
		}
	}
	var emailAuth botEmailAuthorization
	var targetFingerprint []string
	if emailTool, ok := t.botRunnableTool.(*botEmailTool); ok {
		account, err := emailTool.account(bound)
		if err != nil {
			return nil, err
		}
		emailAuth = botEmailAuthorization{target: emailTool, fingerprint: account.Fingerprint(), check: t.binding.check}
		targetFingerprint = []string{emailAuth.fingerprint}
		if emailTool.Name() == "email_reply" {
			raw, err := json.Marshal(args)
			if err != nil {
				return nil, err
			}
			snapshot, err := emailTool.replySnapshot(bound, account, raw)
			if err != nil {
				return nil, err
			}
			emailAuth.snapshot = &snapshot
			targetFingerprint = []string{snapshot.Fingerprint()}
		}
	}
	var mcpAuth botMCPAuthorization
	if mcpTool, ok := t.botRunnableTool.(*botMCPTool); ok {
		mcpAuth = botMCPAuthorization{target: mcpTool, fingerprint: botMCPFingerprint(mcpTool.config), check: t.binding.check}
		targetFingerprint = []string{mcpAuth.fingerprint}
	}
	if err := agent.AuthorizeBotBoundTool(bound, ctx, t.Name(), args, targetFingerprint...); err != nil {
		return nil, err
	}
	if err := t.binding.req.Budget.ReserveTool(bound, t.Name()); err != nil {
		return nil, err
	}
	if emailAuth.target != nil {
		invocation := botBoundToolContext{Context: bound, owner: context.WithValue(bound, botEmailAuthorizedKey{}, emailAuth)}
		return emailAuth.target.Run(invocation, args)
	}
	if mcpAuth.target != nil {
		invocation := botBoundToolContext{Context: bound, owner: context.WithValue(bound, botMCPAuthorizedKey{}, mcpAuth)}
		return mcpAuth.target.Run(invocation, args)
	}
	return t.botRunnableTool.Run(bound, args)
}

// Tool request processors may otherwise observe viewer credentials while adding
// their declarations. Supply the same captured owner scope as Run.
func (t *botBoundTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	if ctx == nil {
		return store.ErrBotForbidden
	}
	bound := botBoundToolContext{Context: ctx, owner: t.binding.bind(ctx)}
	if err := t.binding.check(bound); err != nil {
		return err
	}
	if err := t.botRunnableTool.ProcessRequest(bound, req); err != nil {
		return err
	}
	// Function tools register themselves in the request. Keep that dispatch
	// target wrapped, rather than exposing the underlying unguarded Run method.
	if req != nil && req.Tools != nil {
		if _, registered := req.Tools[t.Name()]; registered {
			req.Tools[t.Name()] = t
		}
	}
	return nil
}
