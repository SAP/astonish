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
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
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
	// ProviderFactory is explicitly supplied by the trusted composition root. It
	// receives scoped config only; nil never permits ambient provider fallback.
	ProviderFactory func(context.Context, string, string, *config.AppConfig) (model.LLM, error)
	// ChatConfig and Catalog are trusted owner-scoped Chat dependencies, never host defaults.
	ChatConfig    *config.AppConfig
	Catalog       *ChatCatalogResources
	TaskWorkspace bot.TaskWorkspace
	Research      *BotResearchEpisode
	// SynthesisNow optionally supplies the soft stopping-policy clock. It never
	// changes budget, lease, authorization or operation deadlines.
	SynthesisNow func() time.Time
	// FileReceipts is trusted per-attempt publication evidence, never model JSON.
	FileReceipts  *BotFileReceipts
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
	// ContextWindow is an explicit override. ResolveContextWindow, when supplied,
	// resolves capacity from the already owner-scoped model (never ambient config).
	ContextWindow        int
	ResolveContextWindow func(context.Context, model.LLM) int
}

// BotEmployeePresentationRequest is server-only input. Store must be the original
// owner-scoped private claim repository; Operations are its verified receipts.
type BotEmployeePresentationRequest struct {
	Bot           store.Bot
	Source        store.BotRun
	Claim         store.BotEmployeePresentationRequest
	Store         store.BotEmployeePresentationStore
	DispatchToken string
	Voice         string
	Operations    []store.BotOperation
}

type BotEmployeePresentationFactoryConfig struct {
	LoadScope func(context.Context, bot.RunRequest) (*BotChatScope, error)
	// ResolvePinned must resolve these exact stored names using owner credentials,
	// not re-read a changed session pin or inherit current defaults.
	ResolvePinned func(context.Context, *BotChatScope, string, string) (model.LLM, error)
	WrapModel     func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error)
}

type BotEmployeePresentationFactory func(context.Context, BotEmployeePresentationRequest) (string, error)

// NewBotEmployeePresentationFactory deliberately has no Prepare, tool factory,
// executable lease, turn authorizer, mandate, or fresh budget constructor.
func NewBotEmployeePresentationFactory(cfg BotEmployeePresentationFactoryConfig) (BotEmployeePresentationFactory, error) {
	if cfg.LoadScope == nil || cfg.ResolvePinned == nil || cfg.WrapModel == nil {
		return nil, store.ErrBotUnsupported
	}
	return func(ctx context.Context, q BotEmployeePresentationRequest) (string, error) {
		if q.Store == nil || q.DispatchToken == "" || q.Claim.BotID != q.Bot.ID || q.Claim.RunID != q.Source.ID || q.Claim.ClaimToken == "" {
			return "", store.ErrBotForbidden
		}
		principal, ok := execution.PrincipalFromContext(ctx)
		if !ok || !principal.Authenticated || principal.Subject != q.Bot.OwnerID.String() {
			return "", store.ErrBotForbidden
		}
		owner, err := execution.WithPrincipal(botValueFreeContext{ctx}, principal)
		if err != nil {
			return "", err
		}
		owner = store.WithUserID(owner, q.Bot.OwnerID.String())
		owner = store.WithOrgSlug(owner, principal.OrgSlug)
		owner = store.WithTeamSlug(owner, principal.TeamSlug)
		owner = store.WithSessionID(owner, q.Source.SessionID.String())
		p, err := q.Store.ReadEmployeePresentation(owner, q.Claim)
		if err != nil {
			return "", err
		}
		if p == nil || p.ValidateBinding(q.Source) != nil || !q.Source.Status.Terminal() || p.State != store.BotEmployeePresentationClaimed || p.ClaimToken != q.Claim.ClaimToken || p.OwnerID != q.Bot.OwnerID || p.OrgSlug != principal.OrgSlug || p.TeamSlug != principal.TeamSlug || !time.Now().Before(p.AuthorityExpiresAt) {
			return "", store.ErrBotForbidden
		}
		owner, cancel := context.WithDeadline(owner, p.AuthorityExpiresAt)
		defer cancel()
		// Only the loader's read scope is shared with execution construction.
		scope, err := cfg.LoadScope(owner, bot.RunRequest{Bot: q.Bot, Run: q.Source, Principal: principal})
		if scope != nil && scope.Cleanup != nil {
			defer scope.Cleanup()
		}
		if err != nil {
			return "", err
		}
		if scope == nil || scope.Services == nil || scope.Cleanup == nil {
			return "", store.ErrBotUnsupported
		}
		services := *scope.Services
		if q.Bot.Scope == store.BotScopeTeam {
			services.PersonalCredentials = nil
			services.PersonalSettings = nil
			services.PersonalBots = nil
		}
		owner = store.WithServices(owner, &services)
		owner = store.WithCredentialStore(owner, store.NewMergedCredentialStore(services.PersonalCredentials, services.Credentials))
		repo := services.PersonalBots
		if q.Bot.Scope == store.BotScopeTeam {
			repo = services.Bots
		}
		if repo == nil {
			return "", store.ErrBotUnsupported
		}
		current, err := repo.Get(owner, q.Bot.ID)
		if err != nil {
			return "", err
		}
		if current == nil || current.OwnerID != p.OwnerID || current.Scope != q.Bot.Scope || current.Status == store.BotDeleted {
			return "", store.ErrBotForbidden
		}
		budget := &bot.PresentationBudget{Store: q.Store, Request: q.Claim, DispatchToken: q.DispatchToken}
		if err := budget.Check(owner); err != nil {
			return "", err
		}
		llm, err := cfg.ResolvePinned(owner, scope, p.Provider, p.Model)
		if err != nil {
			return "", err
		}
		if llm == nil {
			return "", store.ErrBotUnsupported
		}
		llm, err = cfg.WrapModel(owner, llm, budget)
		if err != nil {
			return "", err
		}
		if llm == nil {
			return "", store.ErrBotUnsupported
		}
		return bot.ComposeEmployeePresentation(owner, q.Source, *p, bot.EmployeePresentationOptions{LLM: llm, Boundary: budget.Check, Voice: q.Voice, Operations: q.Operations})
	}, nil
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
	svc := &store.Services{Mode: store.ModePlatform, Scheduler: team.ScheduledJobs(), Sessions: team.Sessions(), Bots: team.Bots(), Credentials: team.Credentials(), Memory: team.Memories(), Flows: team.Flows(), Docs: team.Docs(), Apps: team.Apps(), TeamSkills: team.Skills(), TeamMCPServers: team.MCPServers(), A2AAgents: org.OrgA2AAgents(), TeamA2AAgents: team.A2AAgents(), TeamNetworkPolicies: team.NetworkPolicies(), Settings: team.Settings()}
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
	if cfg.LoadScope == nil || cfg.Prepare == nil || cfg.WrapModel == nil || (cfg.ContextWindow <= 0 && cfg.ResolveContextWindow == nil) {
		return nil, fmt.Errorf("%w: explicit Bot scope, isolation, budget wrapper and context window required", store.ErrBotUnsupported)
	}
	return func(ctx context.Context, req bot.RunRequest) (resources *api.BotChatResources, err error) {
		// Include request validation in construction's synchronous read operation.
		// Authorization and lease state remain fresh SQL reads on every check.
		_, startup := ctx.Value(botStartupReadsKey{}).(func())
		endContentReads := func() {}
		if !startup || entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
			ctx, endContentReads = entstore.WithBotContentReadOperation(ctx)
		}
		defer endContentReads()
		if err = validateBotFactoryRequest(ctx, req); err != nil {
			return nil, err
		}
		// Keep cancellation/deadlines, discard all ambient viewer/service/LLM values.
		owner, cancelOwner := context.WithCancel(botFactoryOwnerContext(ctx, req))
		// Construction borrows the adapter's first-request operation, or owns a
		// synchronous operation when called independently. Current SQL/root/mandate
		// checks still execute; borrowed bytes end at the model/tool boundary.
		owner = entstore.CopyImmutableReadWindow(owner, ctx)
		owner = entstore.WithBotContentReadOperationFromContext(owner, ctx)
		if end, ok := ctx.Value(botStartupReadsKey{}).(func()); ok {
			owner = context.WithValue(owner, botStartupReadsKey{}, end)
		}
		// The adapter installs this server-only ledger before factory construction.
		// Carry it explicitly without restoring any ambient viewer values.
		owner = api.WithBotPreparedPlanSessionFromContext(owner, ctx)
		owner = api.WithBotDefinitionConsentFromContext(owner, ctx)
		owner = bot.WithToolOutcomesFromContext(owner, ctx)
		owner = bot.WithEmployeeChatServiceFromContext(owner, ctx)
		owner = bot.WithBotWorkServicesFromContext(owner, ctx)
		declarations := &botDeclarationReads{}
		cleanups := []func(){cancelOwner, declarations.close}
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
		contextWindow := cfg.ContextWindow
		if contextWindow <= 0 && cfg.ResolveContextWindow != nil {
			contextWindow = cfg.ResolveContextWindow(owner, resolved.LLM)
		}
		if contextWindow <= 0 {
			return nil, store.ErrBotUnsupported
		}
		wrapped, err := cfg.WrapModel(owner, resolved.LLM, req.Budget)
		if err != nil {
			return nil, err
		}
		if wrapped == nil {
			return nil, store.ErrBotUnsupported
		}
		if req.Run.TitleConfirmation != nil && req.Run.TitleConfirmation.State == store.BotTitleConfirmationNeedsQuestion {
			redactor := credentials.NewRedactor()
			owner = credentials.WithRedactor(owner, redactor)
			binding := &botFactoryBinding{declarations: declarations, owner: owner, req: req}
			questionRuntime := agent.BotPolicyRuntime{Guard: botTitleQuestionPolicy{binding}, Classifier: botTitleQuestionPolicy{binding}}
			owner = agent.WithBotPolicyRuntime(owner, questionRuntime)
			binding.owner = owner
			question, err := botWorkflowQuestionTool(req, scoped)
			if err != nil {
				return nil, err
			}
			bound, err := newBotBoundTool(question, binding)
			if err != nil {
				return nil, err
			}
			binding.installProgressBridge()
			llm := &botTitleQuestionLLM{LLM: &botProgressLLM{LLM: wrapped, binding: binding}, binding: binding}
			// Title clarification is a restricted view of the shared Chat runtime,
			// not another agent construction path. No catalog discovery, backend,
			// pool or host bootstrap is needed before the owner answers.
			wired, err := NewScopedChatRuntime(owner, ChatRuntimeDependencies{
				Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}, PlatformMode: true,
					ProviderName: resolved.Provider, ModelName: resolved.Model},
				LLM: llm, Sessions: scoped.Sessions, SessionID: req.Run.SessionID.String(),
				Catalog: &ChatCatalog{MainThreadTools: []tool.Tool{bound}},
				Prompt:  &agent.SystemPromptBuilder{}, Redactor: redactor,
				Credentials: credentials.NewStoreAdapter(creds),
			}, ChatRuntimeHooks{})
			if err != nil {
				return nil, err
			}
			cleanups = append(cleanups, wired.Cleanup)
			chat := wired.ChatAgent
			// The pending-title policy permits only the actual question tool;
			// progressive discovery is intentionally unavailable in this state.
			chat.Tools = wired.InternalTools
			repo := store.BotStoreFromContext(owner, req.Bot.Scope)
			transcript, ok := repo.(store.BotTranscriptStore)
			if !ok {
				return nil, store.ErrBotUnsupported
			}
			if err := binding.check(owner); err != nil {
				return nil, err
			}
			return &api.BotChatResources{ProgressDispatch: bot.ProgressDispatchFromContext(owner), Agent: chat, SessionService: scoped.Sessions, AppName: "astonish", Cleanup: cleanup, Transcript: transcript, PolicyRuntime: &questionRuntime, Authorization: req.Authorization, ModelSelection: &store.SessionPin{Provider: resolved.Provider, Model: resolved.Model}}, nil
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
		// Install only Bot publication/receipt extensions. Ordinary filesystem
		// execution stays in the shared Chat catalog and receives no name-only grant.
		if !req.Run.ControlOnly() || botFactoryInteractive(req) || req.Mandate != nil {
			if safe.FileReceipts == nil {
				fileTools, receipts, fileErr := BotPublicationToolsWithReceipts(req, scoped)
				if fileErr != nil {
					return nil, fileErr
				}
				// Never leave an ambient adapter with the same executable name.
				names := map[string]bool{}
				for _, t := range fileTools {
					names[t.Name()] = true
				}
				kept := make([]tool.Tool, 0, len(safe.Tools)+len(fileTools))
				for _, t := range safe.Tools {
					if t == nil {
						return nil, store.ErrBotUnsupported
					}
					if !names[t.Name()] {
						kept = append(kept, t)
					}
				}
				safe.Tools = append(kept, fileTools...)
				safe.FileReceipts = receipts
			}
			safe.PolicyRuntime.Classifier = botSharedFileClassifier{fallback: safe.PolicyRuntime.Classifier}
		}
		if !botFactoryInteractive(req) || (safe.PolicyRuntime.Guard != nil && safe.PolicyRuntime.Classifier != nil) {
			safe.PolicyRuntime.Classifier = WithBotMandatedReadClassifier(safe.PolicyRuntime.Classifier, safe.Tools)
			safe.PolicyRuntime.Classifier = WithBotEmailClassifier(safe.PolicyRuntime.Classifier, safe.Tools)
			safe.PolicyRuntime.Classifier = WithBotMCPClassifier(safe.PolicyRuntime.Classifier, safe.Tools)
			safe.PolicyRuntime, err = api.BindBotChatPolicyRuntime(ctx, safe.PolicyRuntime)
			if err != nil {
				return nil, err
			}
			owner = agent.WithBotPolicyRuntime(owner, safe.PolicyRuntime)
		}
		// Interactive prepared plans also need the trusted classifier; installing
		// metadata does not replace their turn authorizer with autonomy policy.
		owner = agent.WithBotPolicyRuntime(owner, safe.PolicyRuntime)
		binding := &botFactoryBinding{declarations: declarations, synthesisReserve: newBotSynthesisReserve(req.Budget, req.Version.Budget), research: safe.Research, owner: owner, req: req, ownerEmail: scoped.OwnerEmail, verifyIsolation: safe.VerifyIsolation}
		if binding.synthesisReserve != nil {
			if req.Mandate != nil {
				binding.synthesisReserve.authorityDeadline = req.Mandate.Authorization.Snapshot().ExpiresAt
			} else if req.Run.CallerCeiling != nil {
				binding.synthesisReserve.authorityDeadline = req.Run.CallerCeiling.ExpiresAt
			}
			if deadline := binding.synthesisReserve.deadline(); !deadline.IsZero() {
				binding.synthesisReserve.reserveTime = min(max(time.Duration(0), time.Until(deadline))/5, 60*time.Second)
			}
		}
		if binding.synthesisReserve != nil && safe.SynthesisNow != nil {
			binding.synthesisReserve.now = safe.SynthesisNow
		}
		taskRepo := services.Bots
		if req.Bot.Scope == store.BotScopePersonal {
			taskRepo = services.PersonalBots
		}
		if tasks, ok := taskRepo.(store.BotTaskStore); ok && safe.TaskWorkspace != nil {
			binding.tasks = &botTaskServices{binding: binding, tasks: tasks, workspace: safe.TaskWorkspace}
			if redactor := credentials.RedactorFromContext(owner); redactor != nil {
				binding.redactTask = redactor.Redact
			}
			if req.Run.Task != nil {
				pinned, e := safe.TaskWorkspace.Load(owner, req.Bot.ID, *req.Run.Task)
				if e != nil {
					return nil, e
				}
				if req.Task == nil || pinned.Ref != req.Task.Ref {
					return nil, store.ErrBotConflict
				}
				binding.tasks.current, e = tasks.GetTask(owner, req.Bot.ID, req.Run.Task.TaskID)
				if e != nil {
					return nil, e
				}
				binding.tasks.current.Head = req.Run.Task.Clone()
			}
		} else if req.Run.Task != nil {
			return nil, store.ErrBotUnsupported
		}
		binding.installProgressBridge()
		llm := &botFactoryLLM{LLM: &botProgressLLM{LLM: wrapped, binding: binding}, binding: binding}
		// Bound tool contexts resolve values from owner, not the caller; install
		// the same metered auxiliary capability there before publishing resources.
		owner = agent.WithBotAuxiliaryLLM(owner, llm)
		binding.owner = owner
		deps := &botChatDependencies{scope: scoped, safe: safe, binding: binding, llm: llm, contextWindow: contextWindow, credentials: creds}
		chatCfg := &ChatFactoryConfig{AppConfig: &config.AppConfig{}, ProviderName: resolved.Provider, ModelName: resolved.Model, PlatformMode: true, SessionService: scoped.Sessions}
		wired, err := newBotScopedChatRuntime(owner, chatCfg, deps)
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
		wired.ChatAgent.BeginModelPreparation = func(c context.Context) context.Context {
			// Prompt-only services are installed by the adapter after construction.
			// Carry only the read operation back, not the captured owner's values.
			return entstore.WithBotContentReadOperationFromContext(c, declarations.begin(binding.bind(c)))
		}
		wired.ChatAgent.EndModelPreparation = declarations.close
		evidenceReader, _ := sessionStore.(store.BotTranscriptReader)
		// Historical work bodies are not bootstrap material. MEMORY.md supplies
		// compact continuity; explicit file reads retrieve older deliverables.
		// Current-run recovery still uses the exact lease-bound Work store.
		priorRun := func(c context.Context) (*bot.BotPriorRunContext, error) {
			// Manual/title continuations have no recurring occurrence to compare.
			// Do not put their mandatory active-run validation under an optional
			// history deadline merely to return no prior occurrence.
			if req.Run.Trigger != store.TriggerCron || req.Run.TriggerID == uuid.Nil || req.Run.CreatedAt.IsZero() {
				return nil, nil
			}
			bound, cancel := context.WithTimeout(binding.bind(c), botWorkHistoryTimeout)
			defer cancel()
			if err := binding.check(bound); err != nil {
				return nil, err
			}
			return readBotPriorRun(bound, repo, req.Run)
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
		researchFactory := func(c context.Context, directed api.BotEmployeeResearchRequest) (*api.BotChatResources, error) {
			r := directed.Request
			if r.Run.ID != req.Run.ID || r.Run.SessionID != req.Run.SessionID || r.Budget != req.Budget || r.Mandate != req.Mandate || r.Policy == nil || execution.RunPolicyFromContext(c) != r.Policy || directed.PolicyRuntime.Guard == nil || directed.PolicyRuntime.Classifier == nil || directed.PolicyRuntime.Approvals != nil {
				return nil, store.ErrBotForbidden
			}
			if err := binding.check(c); err != nil {
				return nil, err
			}
			owner, cancel := context.WithCancel(binding.bind(c))
			owner = execution.WithRunPolicy(owner, r.Policy)
			owner = agent.WithBotPolicyRuntime(owner, directed.PolicyRuntime)
			freshBinding := &botFactoryBinding{synthesisReserve: binding.synthesisReserve, owner: owner, req: r, research: binding.research, researchOnly: true, verifyIsolation: binding.verifyIsolation}
			freshBinding.progress = binding.progress
			freshModel := &botFactoryLLM{LLM: &botProgressLLM{LLM: wrapped, binding: freshBinding}, binding: freshBinding, researchOnly: true}
			freshSafe := *safe
			freshSafe.PolicyRuntime = directed.PolicyRuntime
			fresh, e := newBotScopedChatRuntime(owner, chatCfg, &botChatDependencies{scope: scoped, safe: &freshSafe, binding: freshBinding, llm: freshModel, contextWindow: contextWindow, credentials: creds})
			if e != nil {
				cancel()
				return nil, e
			}
			var reads []tool.Tool
			for _, name := range []string{"perplexity_web_search", "web_fetch"} {
				if entry := fresh.ChatAgent.ToolIndex.GetToolEntry(name); entry != nil {
					reads = append(reads, entry.Tool)
				}
			}
			index, e := newChatToolIndex(owner, reads, nil, nil, nil)
			if e != nil {
				fresh.Cleanup()
				cancel()
				return nil, e
			}
			chat := fresh.ChatAgent
			chat.Tools, chat.Toolsets, chat.ToolIndex, chat.SubAgentManager = reads, nil, index, nil
			return &api.BotChatResources{ProgressDispatch: bot.ProgressDispatchFromContext(owner), Agent: chat, SessionService: scoped.Sessions, AppName: "astonish", Cleanup: func() { fresh.Cleanup(); cancel() }, PolicyRuntime: &directed.PolicyRuntime}, nil
		}
		workerFactory := func(c context.Context, directed api.BotEmployeeWorkerRequest) (*api.BotChatResources, error) {
			if directed.Request.Run.ID != req.Run.ID || directed.Request.Run.SessionID != req.Run.SessionID || directed.Request.Budget != req.Budget || directed.Sessions == nil || directed.ConversationID == "" {
				return nil, store.ErrBotForbidden
			}
			if err := binding.check(c); err != nil {
				return nil, err
			}
			owner, cancel := context.WithCancel(binding.bind(c))
			owner = agent.WithBotPrivateWorkerExecution(owner)
			freshBinding := *binding
			freshBinding.owner, freshBinding.conversationID, freshBinding.workerOnly = owner, directed.ConversationID, true
			freshBinding.declarations = &botDeclarationReads{}
			freshModel := &botFactoryLLM{LLM: &botProgressLLM{LLM: wrapped, binding: &freshBinding}, binding: &freshBinding}
			freshScope, freshSafe := *scoped, *safe
			freshScope.Sessions = directed.Sessions
			fresh, err := newBotScopedChatRuntime(owner, chatCfg, &botChatDependencies{scope: &freshScope, safe: &freshSafe, binding: &freshBinding, llm: freshModel, contextWindow: contextWindow, credentials: creds})
			if err != nil {
				cancel()
				return nil, err
			}
			return &api.BotChatResources{Agent: fresh.ChatAgent, SessionService: directed.Sessions, AppName: "astonish", PolicyRuntime: &freshSafe.PolicyRuntime, PublishedFiles: func() []store.BotArtifact { return safe.FileReceipts.Artifacts(req) }, Cleanup: func() { fresh.Cleanup(); freshBinding.declarations.close(); cancel() }}, nil
		}
		return &api.BotChatResources{WorkerFactory: workerFactory, ArtifactBody: binding.artifactReviewReader(scoped, safe.FileReceipts), RepairFileDelivery: binding.fileDeliveryRepair(scoped, safe.FileReceipts), RecoverFileDelivery: binding.fileDeliveryRecovery(scoped, safe.FileReceipts), PublishedFiles: func() []store.BotArtifact { return safe.FileReceipts.Artifacts(req) }, ProgressDispatch: bot.ProgressDispatchFromContext(owner), ResearchFactory: researchFactory, ModelSelection: &store.SessionPin{Provider: resolved.Provider, Model: resolved.Model}, PriorRun: priorRun, ValidateWorkContinuation: continuation, EvidenceReader: evidenceReader, AssessmentContext: binding.assessmentContext, Skills: skills, Redact: redact, Work: work, Authorization: req.Authorization, Transcript: transcript, Agent: wired.ChatAgent, SessionService: scoped.Sessions, AppName: "astonish", Cleanup: cleanup, PolicyRuntime: &safe.PolicyRuntime}, nil
	}, nil
}

// readBotPriorRun scans the scoped UUID-paginated history without assuming that
// UUID order or completion order is occurrence order. Interrupted recovery may
// have no FinishedAt; StateCommittedAt is its persisted outcome observation.
func readBotPriorRun(ctx context.Context, repo interface {
	ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error)
}, current store.BotRun) (*bot.BotPriorRunContext, error) {
	if current.Trigger != store.TriggerCron || current.TriggerID == uuid.Nil || current.CreatedAt.IsZero() {
		return nil, nil
	}
	var latest *bot.BotPriorRunContext
	cursor := uuid.Nil
	for page := 0; page < botWorkHistoryMaxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, err := repo.ListRuns(ctx, current.BotID, store.BotPage{Limit: botWorkHistoryPageSize, After: cursor.String()})
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(rows) > botWorkHistoryPageSize {
			return nil, store.ErrBotInvalid
		}
		for _, run := range rows {
			if bytes.Compare(run.ID[:], cursor[:]) <= 0 {
				return nil, store.ErrBotInvalid
			}
			cursor = run.ID
			if run.Trigger != store.TriggerCron {
				continue
			}
			outcomeAt := run.StateCommittedAt
			if run.FinishedAt != nil && !run.FinishedAt.IsZero() {
				outcomeAt = *run.FinishedAt
			}
			// Legacy rows without a persisted finish/transition timestamp cannot
			// prove when the outcome became known; do not invent one.
			prior := &bot.BotPriorRunContext{BotID: run.BotID, RunID: run.ID, TriggerID: run.TriggerID, CreatedAt: run.CreatedAt, OutcomeAt: outcomeAt, Status: run.Status}
			if run.Report.Failure != nil {
				switch kind := run.Report.Failure.Kind; kind {
				case store.BotReportFailureBudget, store.BotReportFailureExecutionFormat, store.BotReportFailureOperational, store.BotReportFailureInterrupted, store.BotReportFailureSetup:
					prior.FailureKind = kind
				}
			}
			if !prior.ValidFor(current) {
				continue
			}
			if latest == nil || prior.CreatedAt.After(latest.CreatedAt) || (prior.CreatedAt.Equal(latest.CreatedAt) && bytes.Compare(prior.RunID[:], latest.RunID[:]) > 0) {
				latest = prior
			}
		}
		if len(rows) < botWorkHistoryPageSize {
			return latest, nil
		}
	}
	return nil, fmt.Errorf("prior run: %w", errBotWorkHistoryExhausted)
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
	if c := req.Run.TitleConfirmation; c != nil {
		if c.ValidateBinding(req.Run, req.Bot.OwnerID) != nil || (c.State != store.BotTitleConfirmationNeedsQuestion && c.State != store.BotTitleConfirmationConfirmed) {
			return store.ErrBotInvalid
		}
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
		prepared := api.BotPreparedPlanSessionFromContext(ctx)
		if prepared != nil && prepared.Resume != nil && prepared.Resume.Stage != store.BotPlanAwaitingApproval {
			// Only the adapter's private recovered continuation may use consumed
			// approval evidence. Re-read under the current lease, never trust args.
			current, err := prepared.Store.LoadBotPlanByApproval(ctx, req.Run.Lease, checkpoint.ApprovalID)
			if err != nil {
				return err
			}
			if current.OperationID != prepared.Resume.OperationID || current.Revision != prepared.Resume.Revision || checkpoint.Fence >= req.Run.Lease.Fence {
				return store.ErrBotConflict
			}
		} else {
			approval, err := rs.LoadResumeApproval(ctx, req.Run.Lease)
			if err != nil {
				return err
			}
			if approval == nil || !checkpoint.Matches(*approval) || checkpoint.Fence >= req.Run.Lease.Fence {
				return store.ErrBotConflict
			}
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
	if d := bot.ProgressDispatchFromContext(ctx); d != nil {
		owner = bot.WithProgressDispatch(owner, d)
	}
	if c := bot.ProgressControllerFromContext(ctx); c != nil {
		owner = bot.WithProgressController(owner, c)
	}
	if botFactoryInteractive(req) {
		owner = execution.WithBotTurnAuthorizer(owner, req.Authorization)
	}
	owner = store.WithUserID(owner, req.Bot.OwnerID.String())
	owner = store.WithOrgSlug(owner, req.Principal.OrgSlug)
	owner = store.WithTeamSlug(owner, req.Principal.TeamSlug)
	return store.WithSessionID(owner, req.Run.SessionID.String())
}

type botChatDependencies struct {
	scope         *BotChatScope
	safe          *BotChatSafeResources
	binding       *botFactoryBinding
	llm           model.LLM
	contextWindow int
	credentials   store.CredentialStore
}
type botFactoryBinding struct {
	conversationID   string
	workerOnly       bool
	declarations     *botDeclarationReads
	progress         *botProgressBridge
	synthesisReserve *botSynthesisReserve
	researchOnly     bool
	tasks            *botTaskServices
	// Captured from the genuine shared catalog, never a name-only tool lookup at dispatch.
	publicFetch      *sandbox.NodeTool
	taskSearch       botRunnableTool
	taskSearchConfig *config.AppConfig
	redactTask       func(string) string
	research         *BotResearchEpisode
	sharedResearch   *botConfiguredResearchTool
	ownerEmail       func(context.Context) (store.BotOwnerEmailDelivery, error)
	owner            context.Context
	req              bot.RunRequest
	verifyIsolation  func(context.Context) error
}

func (b *botFactoryBinding) check(ctx context.Context) error {
	return b.checkWithBudget(ctx, b.req.Budget.Check)
}

// checkDuringGeneration is only installed as the post-reservation health
// callback. New dispatches continue to use check, including their capacity gate.
func (b *botFactoryBinding) checkDuringGeneration(ctx context.Context) error {
	return b.checkWithBudget(ctx, func(c context.Context) error {
		return provider.CheckGenerationBudget(c, b.req.Budget)
	})
}

func (b *botFactoryBinding) checkWithBudget(ctx context.Context, checkBudget func(context.Context) error) error {
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
	// One synchronous guard chain may verify the same immutable provenance via
	// the lease, budget, mandate and isolation paths. Keep all live checks, but
	// give each path the same owner-bound operation, including direct callers
	// outside construction/declaration preparation.
	bound := b.bind(ctx)
	endReads := func() {}
	if entstore.WithBotContentReadOperationFromContext(context.Background(), bound) == context.Background() {
		bound, endReads = entstore.WithBotContentReadOperation(bound)
	}
	defer endReads()
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
	if !reflect.DeepEqual(run.TitleConfirmation, b.req.Run.TitleConfirmation) || run.TitleConfirmation.ValidateBinding(*run, current.OwnerID) != nil {
		return store.ErrBotConflict
	}
	if err := checkBudget(bound); err != nil {
		return err
	}
	if b.req.Mandate != nil {
		if err := b.req.Mandate.Check(bound); err != nil {
			return err
		}
	}
	if botFactoryInteractive(b.req) {
		if b.req.Authorization == nil {
			return store.ErrBotForbidden
		}
		if err := b.req.Authorization.Authorize(bound, execution.CapabilityChat); err != nil {
			return err
		}
	}
	if b.req.Run.TitleConfirmation != nil && b.req.Run.TitleConfirmation.State == store.BotTitleConfirmationNeedsQuestion {
		if !bot.HasInteractionOwner(bound) {
			return store.ErrBotForbidden
		}
		return nil // no physical workspace exists during title clarification
	}
	return b.verifyIsolation(bound)
}

// bind keeps invocation cancellation and narrowed child policy, but resolves
// every other value from the captured owner context, never from a later viewer.
func (b *botFactoryBinding) bind(ctx context.Context) context.Context {
	policy := execution.RunPolicyFromContext(ctx)
	if b.researchOnly || b.req.Policy == nil || policy == nil || policy.Identity() != b.req.Policy.Identity() {
		policy = b.req.Policy
	}
	owner := api.PreserveCapturedDrillOccurrence(entstore.WithBotContentReadOperationFromContext(b.owner, ctx), ctx)
	if end, ok := ctx.Value(botNativeNetworkPreflightKey{}).(func()); ok {
		owner = context.WithValue(owner, botNativeNetworkPreflightKey{}, end)
	}
	return execution.WithRunPolicy(botBoundContext{Context: ctx, owner: owner}, policy)
}

type botBoundContext struct {
	context.Context
	owner context.Context
}

func (c botBoundContext) Value(key any) any { return c.owner.Value(key) }

type botFactoryLLM struct {
	model.LLM
	binding      *botFactoryBinding
	researchOnly bool
}

func (l *botFactoryLLM) SupportsModelPreflight() bool { return true }

func (l *botFactoryLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		observePreflight := entstore.ObserveBotExecutionStage(ctx, entstore.BotStagePreflight)
		observeProvider := func(error) {}
		var stageErr error
		defer func() { observeProvider(stageErr) }()
		// Share declaration bytes with the synchronous factory checks.
		declaration := l.binding.declarations.bind(l.binding, req)
		// Invocation-only purpose controls prompt composition, never authority.
		// Read before bind deliberately discards later viewer context values.
		purpose := bot.ModelCallPurposeFromContext(ctx)
		prepared := entstore.WithBotContentReadOperationFromContext(declaration.bind(ctx), ctx)
		bound, endReads := entstore.WithBotContentReadOperationIfAbsent(prepared)
		endContentReads := sync.OnceFunc(func() {
			endReads()
			observePreflight(stageErr)
			entstore.EndImmutableReadWindow(bound)
			l.binding.declarations.close()
			endBotStartupReads(l.binding.owner)
			provider.EndModelPreflight(ctx)
			observeProvider = entstore.ObserveBotExecutionStage(bound, entstore.BotStageProvider)
		})
		defer endContentReads()
		originalYield := yield
		yield = func(r *model.LLMResponse, err error) bool {
			stageErr = errors.Join(stageErr, err)
			endContentReads()
			return originalYield(r, err)
		}
		bound = provider.WithModelPreflightEnd(bound, endContentReads)
		if err := l.binding.check(bound); err != nil {
			yield(nil, err)
			return
		}
		request := req
		if !l.researchOnly && purpose != bot.ModelCallPurposeAssessment && purpose != bot.ModelCallPurposeProgress {
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
		if l.researchOnly {
			stream = false
		}
		var synthesis bool
		if purpose != bot.ModelCallPurposeAssessment && purpose != bot.ModelCallPurposeProgress {
			var err error
			request, synthesis, err = l.binding.synthesisReserve.prepare(bound, request, l.LLM)
			if err != nil {
				yield(nil, err)
				return
			}
			if l.binding.synthesisReserve != nil && request != nil {
				bound = provider.WithBudgetInputGuidance(bound, request)
			}
		}
		// Transfer cleanup only to wrappers with an explicit preflight contract.
		if next, ok := l.LLM.(interface{ SupportsModelPreflight() bool }); synthesis || !ok || !next.SupportsModelPreflight() {
			endContentReads()
		}
		if synthesis {
			// The final-only path drains every attempt before deciding whether one
			// same-budget correction is safe. It never exposes incomplete output.
			l.generateSynthesis(bound, request, yield)
			return
		}
		for response, err := range l.LLM.GenerateContent(bound, request, stream) {
			if !yield(response, err) || err != nil {
				return
			}
		}
	}
}

type botTitleQuestionPolicy struct{ binding *botFactoryBinding }

func (p botTitleQuestionPolicy) DescribeAction(ctx context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	if err := p.binding.check(ctx); err != nil {
		return execution.ActionDescriptor{}, err
	}
	if name != "ask_user" || validateBotTitleQuestionArgs(raw) != nil {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	return execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}
func (p botTitleQuestionPolicy) Authorize(ctx context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if _, err := p.DescribeAction(ctx, action.Tool, action.Arguments); err != nil {
		return execution.PolicyDecision{}, err
	}
	if p.binding.req.Mandate != nil {
		if err := p.binding.req.Mandate.Authorization.Authorize(execution.CapabilityToolExecute, "ask_user"); err != nil {
			return execution.PolicyDecision{}, err
		}
	}
	return execution.EvaluateRunPolicy(ctx, action, time.Now()), nil
}

// validateBotTitleQuestionArgs refuses convenience pickers and all non-question
// fields before the genuine handler can consult any resource.
func validateBotTitleQuestionArgs(args any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return store.ErrBotInvalid
	}
	for key := range values {
		switch key {
		case "kind", "prompt", "options", "allow_custom_answer":
		default:
			return store.ErrBotInvalid
		}
	}
	values["questionId"] = "title-validation"
	raw, _ = json.Marshal(values)
	return store.ValidateBotTitleQuestion(&store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "title-validation", Payload: raw})
}

// Buffer a full response before exposing any call. One correction consumes the
// same active budget; invalid prose/calls never reach the Chat tool loop.
type botTitleQuestionLLM struct {
	model.LLM
	binding *botFactoryBinding
}

func (m *botTitleQuestionLLM) GenerateContent(ctx context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.binding.declarations.close()
		endBotStartupReads(m.binding.owner)
		bound := m.binding.bind(ctx)
		for attempt := 0; attempt < 2; attempt++ {
			if err := m.binding.check(bound); err != nil {
				yield(nil, err)
				return
			}
			var responses []*model.LLMResponse
			var call *genai.FunctionCall
			calls, size := 0, 0
			for response, err := range m.LLM.GenerateContent(bound, request, false) {
				if err != nil {
					yield(nil, err)
					return
				}
				raw, err := json.Marshal(response)
				if err != nil {
					yield(nil, err)
					return
				}
				size += len(raw)
				if size > 1<<20 {
					yield(nil, errors.New("title clarification generation failed: oversized response"))
					return
				}
				if response != nil && response.Content != nil {
					for _, p := range response.Content.Parts {
						if p != nil && p.FunctionCall != nil {
							calls++
							call = p.FunctionCall
						}
					}
				}
				responses = append(responses, response)
			}
			if calls == 1 && call.Name == "ask_user" && validateBotTitleQuestionArgs(call.Args) == nil {
				if err := m.binding.check(bound); err != nil {
					yield(nil, err)
					return
				}
				for _, response := range responses {
					if !yield(response, nil) {
						return
					}
				}
				return
			}
			copy := *request
			copy.Contents = append(append([]*genai.Content(nil), request.Contents...), genai.NewContentFromText("No tool was executed. Correct your response: call only ask_user once, kind=select, three or four distinct title-specific options, allow_custom_answer=true. No other fields or tools and no plain-text question.", "user"))
			request = &copy
		}
		yield(nil, errors.New("title clarification generation failed: expected one valid ask_user call after one correction"))
	}
}

// currentMemory loads the actual bounded critical Home files and MEMORY.md.
// Historical memory rows and report bodies are never bootstrap material.
func (b *botFactoryBinding) currentMemory(ctx context.Context) (string, error) {
	bound := b.bind(ctx)
	if err := b.check(bound); err != nil {
		return "", err
	}
	svc := store.FromContext(bound)
	if svc == nil || svc.BotFilesFactory == nil {
		return "", store.ErrBotUnsupported
	}
	repo := store.BotStoreFromContext(bound, b.req.Bot.Scope)
	files, err := svc.BotFilesFactory(bound, svc, b.req.Bot.Scope)
	if err != nil {
		return "", err
	}
	documents, err := bot.NewDocuments(repo, files)
	if err != nil {
		return "", err
	}
	projection, err := documents.Load(bound, b.req.Bot.ID, 0)
	if err != nil {
		return "", err
	}
	if err := b.check(bound); err != nil {
		return "", err
	}
	text := agent.BotDocumentsPrompt(projection.Documents)
	if redactor := credentials.RedactorFromContext(bound); redactor != nil {
		text, _ = redactor.Placeholderize(text)
	}
	return text, nil
}

// priorRetrievalContext supplies retrieval guidance without opening historical
// run checkpoints or transcripts. Critical Home files and compact MEMORY.md are
// loaded by currentMemory; historical sources require explicit file reads.
func (b *botFactoryBinding) priorRetrievalContext(ctx context.Context) (string, error) {
	if err := b.check(b.bind(ctx)); err != nil {
		return "", err
	}
	return "\n\n## Prior work retrieval\nUse MEMORY.md as the compact index to earlier outcomes and files. Read referenced files explicitly when needed. Do not infer a research tool or successful external action from a URL or summary; missing history is not proof that an action did not occur.\n", nil
}

// Runnable shape shared by Bot policy dispatch and Chat execution.
type botRunnableTool interface {
	tool.Tool
	Declaration() *genai.FunctionDeclaration
	Run(tool.Context, any) (map[string]any, error)
	ProcessRequest(tool.Context, *model.LLMRequest) error
}
type botBoundTool struct {
	identity string
	next     func(tool.Context, any) (map[string]any, error)
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

// preserveBotToolBinding carries an existing factory authorization marker across
// our lifetime-only wrappers. It must never mint a binding for an unbound or
// arbitrary embedded context, or replace the captured owner values.
func preserveBotToolBinding(original, wrapped tool.Context) tool.Context {
	if bound, ok := original.(botBoundToolContext); ok {
		bound.Context = wrapped
		return bound
	}
	return wrapped
}

func (t *botBoundTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	t.binding.declarations.close()
	endBotStartupReads(t.binding.owner)
	// A newly admitted draft conversation may only ask its pending title
	// question. The authoritative binding, session, arguments, authorization,
	// and budget checks below still gate the genuine question handler.
	pendingTitleQuestion := t.Name() == "ask_user" && t.binding.req.Run.TitleConfirmation != nil && t.binding.req.Run.TitleConfirmation.State == store.BotTitleConfirmationNeedsQuestion
	if t.binding.req.Run.ControlOnly() && t.binding.req.Mandate == nil && !pendingTitleQuestion {
		return nil, store.ErrBotForbidden
	}
	if ctx == nil || ctx.SessionID() == "" {
		return nil, store.ErrBotForbidden
	}
	owner, endReads := entstore.WithBotContentReadOperation(t.binding.bind(ctx))
	defer endReads()
	if _, nativeRead := t.botRunnableTool.(*botMandatedReadTool); nativeRead {
		owner = context.WithValue(owner, botNativeNetworkPreflightKey{}, endReads)
	}
	owner = preparedLeafContext(owner, ctx)
	bound := botBoundToolContext{Context: ctx, owner: owner}
	if err := t.binding.check(bound); err != nil {
		return nil, err
	}
	identity := t.Name()
	if t.identity != "" {
		identity = t.identity
	}
	sharedMCP, isSharedMCP := agent.LazyMCPToolBinding(t.botRunnableTool)
	if isSharedMCP {
		identity = sharedMCP.Identity()
	}
	if t.binding.req.Mandate != nil {
		if err := t.binding.req.Mandate.Authorization.Authorize(execution.CapabilityToolExecute, identity); err != nil {
			return nil, err
		}
	}
	if t.binding.req.Run.TitleConfirmation != nil && t.binding.req.Run.TitleConfirmation.State == store.BotTitleConfirmationNeedsQuestion {
		if t.Name() != "ask_user" || ctx.SessionID() != t.binding.req.Run.SessionID.String() {
			return nil, store.ErrBotForbidden
		}
		if err := validateBotTitleQuestionArgs(args); err != nil {
			return nil, err
		}
		if t.binding.req.Authorization != nil {
			if err := t.binding.req.Authorization.Authorize(bound, execution.CapabilityToolExecute); err != nil {
				return nil, err
			}
		}
		if t.binding.req.Mandate != nil {
			if err := t.binding.req.Mandate.Authorization.Authorize(execution.CapabilityToolExecute, "ask_user"); err != nil {
				return nil, err
			}
		}
		if err := t.binding.req.Budget.ReserveTool(bound, "ask_user"); err != nil {
			return nil, err
		}
		endReads()
		return t.binding.progressTool(bound, t.botRunnableTool, args, t.botRunnableTool.Run)
	}
	if t.binding.synthesisReserve.synthesis() {
		return botSynthesisStoppedResult(bound, t.Name(), args), nil
	}
	if err := t.binding.checkResearchDetail(bound, ctx.SessionID(), t.Name(), args); err != nil {
		return nil, err
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
	if isSharedMCP {
		// The accounting wrapper owns authorization once; the genuine lazy proxy
		// consumes only the private terminal handoff and rechecks live authority.
		bound.owner = agent.PreserveBotInvocationHandoff(bound.owner, ctx)
		prepared, err := sharedMCP.Prepare(bound, args, t.binding.checkDuringGeneration)
		if err != nil {
			return nil, err
		}
		bound.owner = prepared
	} else if err := agent.AuthorizeBotBoundTool(bound, ctx, identity, args, targetFingerprint...); err != nil {
		return nil, err
	}
	if err := t.binding.req.Budget.ReserveTool(bound, identity); err != nil {
		return nil, err
	}
	// Concrete native reads end this operation at their network boundary, after
	// progress journaling and adapter validation. Other tools end it here.
	if _, nativeRead := t.botRunnableTool.(*botMandatedReadTool); nativeRead {
		bound.owner = context.WithValue(bound.owner, botNativeNetworkPreflightKey{}, endReads)
	} else {
		endReads()
	}
	if emailAuth.target != nil {
		invocation := botBoundToolContext{Context: bound, owner: context.WithValue(bound, botEmailAuthorizedKey{}, emailAuth)}
		return t.binding.progressTool(invocation, emailAuth.target, args, emailAuth.target.Run)
	}
	if mcpAuth.target != nil {
		invocation := botBoundToolContext{Context: bound, owner: context.WithValue(bound, botMCPAuthorizedKey{}, mcpAuth)}
		return t.binding.progressTool(invocation, mcpAuth.target, args, mcpAuth.target.Run)
	}
	result, err := t.binding.progressTool(bound, t.botRunnableTool, args, func(c tool.Context, a any) (map[string]any, error) {
		result, err := t.binding.runSynthesisTool(c, botDispatchTarget(t.botRunnableTool, t.next), a)
		if t.botRunnableTool != t.binding.publicFetch {
			recordBotSharedNodeObservation(c, t.botRunnableTool, a, result, err)
		}
		return result, err
	})
	if t.binding.tasks != nil && substantiveTaskTool(t.botRunnableTool) {
		if captureErr := t.binding.tasks.observe(bound, t.Name(), args, result, err); captureErr != nil {
			return result, errors.Join(err, captureErr)
		}
	}
	return result, err
}

// Tool request processors may otherwise observe viewer credentials while adding
// their declarations. Supply the same captured owner scope as Run.
func (t *botBoundTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) (err error) {
	// A failed declaration processor ends this logical operation too.
	defer func() {
		if err != nil {
			t.binding.declarations.close()
			endBotStartupReads(t.binding.owner)
		}
	}()
	if ctx == nil {
		return store.ErrBotForbidden
	}
	binding := t.binding.declarations.bind(t.binding, req)
	bound := botBoundToolContext{Context: ctx, owner: binding.bind(ctx)}
	if err := binding.check(bound); err != nil {
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
