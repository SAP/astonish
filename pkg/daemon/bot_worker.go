package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
)

// BotWorkerConfig is trusted daemon composition. Prepare must verify isolation;
// no ordinary Chat factory or host-tool fallback is used. ChannelManager is
// consulted only for delivery of already-authorized publication targets.
type BotWorkerConfig struct {
	ObserveExecution  func(entstore.BotExecutionTiming)
	KnowledgeSafety   bot.KnowledgeExtractionSafety
	KnowledgeRedactor func(context.Context, execution.Principal, store.BotScope) (*credentials.Redactor, error)
	MandatePolicy     store.BotMandatePolicyResolver
	TurnRegistry      *execution.BotTurnAuthorizationRegistry
	Backend           store.PlatformBackend
	Sandbox           sandbox.Backend
	Pool              sandbox.ToolNodePool
	Prepare           func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error)
	ResolveModel      func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error)
	// Zero selects capabilities from the scoped model (or finite fallbacks).
	// Positive values remain explicit caller/operator overrides.
	ModelOptions  provider.BudgetLLMOptions
	ContextWindow int
	WorkerID      string
	PollInterval  time.Duration
	Publish       bot.EventSink
	// ChannelManager resolves the live transport after channel reloads. Nil uses
	// the daemon's synchronized getter; no manager leaves notifications pending.
	ChannelManager func() *channels.ChannelManager
}
type BotWorker struct {
	schedulerReady    atomic.Bool
	activationStopped atomic.Bool
	progressDrain     atomic.Bool
	lifecycleMu       sync.Mutex
	pollCancels       map[uint64]context.CancelFunc
	pollSerial        uint64
	started           bool
	config            BotWorkerConfig
	runtime           bot.Runtime
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	loopCancel        context.CancelFunc
	pending           map[botDispatchKey]botDispatchHint
	activeBots        map[store.BotID]bool
	activeCancels     map[store.BotID]context.CancelFunc
	maintenanceBots   map[store.BotID]bool
	maintenanceCancel map[store.BotID]context.CancelFunc
	activeSlots       int
	ownerSlots        int
	autonomousSlots   int
	dispatchWake      chan struct{}
	dispatchOverflow  atomic.Uint64
	mandateCleanupMu  sync.Mutex
	mandateCleanup    map[execution.BotRunIdentity]*bot.MandateExecution
}

func NewBotWorker(c BotWorkerConfig) (*BotWorker, error) {
	if c.Backend == nil || c.Sandbox == nil || c.Pool == nil || c.Prepare == nil || c.ResolveModel == nil || c.WorkerID == "" || c.ContextWindow < 0 || c.ModelOptions.MaxOutputTokens < 0 {
		return nil, store.ErrBotUnsupported
	}
	if _, ok := c.Sandbox.(sandbox.BotHomeBackend); !ok {
		return nil, store.ErrBotUnsupported
	}
	if _, ok := c.Pool.(sandbox.BotHomePool); !ok {
		return nil, store.ErrBotUnsupported
	}
	if c.PollInterval == 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.PollInterval < 0 {
		return nil, store.ErrBotInvalid
	}
	if c.KnowledgeSafety == nil {
		c.KnowledgeSafety = botKnowledgeSafety
	}
	if c.KnowledgeRedactor == nil {
		c.KnowledgeRedactor = productionKnowledgeRedactor(c.Backend, nil)
	}
	if c.ChannelManager == nil {
		c.ChannelManager = api.GetChannelManager
	}
	c.Publish = installBotEventPublisher(c.Publish)
	w := &BotWorker{config: c}
	factory, err := launcher.NewBotChatAgentFactory(launcher.BotChatFactoryConfig{LoadScope: w.loadScope, Prepare: w.prepareAuthorizedSchedules, ContextWindow: c.ContextWindow, ResolveContextWindow: func(ctx context.Context, llm model.LLM) int {
		return botModelContextWindow(ctx, llm)
	}, WrapModel: func(ctx context.Context, llm model.LLM, b execution.RunBudget) (model.LLM, error) {
		return provider.NewBudgetLLM(llm, b, botModelBudgetOptions(llm, c.ModelOptions))
	}})
	if err != nil {
		return nil, err
	}
	adapter, err := api.NewBotChatRuntimeAdapter(factory)
	if err != nil {
		return nil, err
	}
	w.runtime = botMeteredRuntime{Runtime: adapter}
	if installer, ok := c.Backend.(store.BotActivationInstaller); ok {
		installer.SetBotActivationReadiness(w.activationReady)
	}
	return w, nil
}

// authenticateOwner establishes identity from live directory membership, not an
// HTTP viewer, a job OwnerID, or credentials left in a request context.
func (w *BotWorker) authenticateOwner(ctx context.Context, s BotSchedulerScope) (context.Context, store.OrgDataStore, error) {
	u, err := w.config.Backend.Users().GetByID(ctx, s.UserID)
	if err != nil {
		return nil, nil, err
	}
	if u == nil || u.ID != s.UserID || u.Status != "active" {
		return nil, nil, store.ErrBotForbidden
	}
	org, err := w.config.Backend.Organizations().GetBySlug(ctx, s.OrgSlug)
	if err != nil {
		return nil, nil, err
	}
	if org == nil || org.Status != "active" {
		return nil, nil, store.ErrBotForbidden
	}
	role, err := w.config.Backend.Organizations().GetMemberRole(ctx, u.ID, org.ID)
	if err != nil || role == "" {
		return nil, nil, store.ErrBotForbidden
	}
	data, err := w.config.Backend.ForOrg(org.Slug)
	if err != nil {
		return nil, nil, err
	}
	if data == nil || data.Teams() == nil {
		return nil, nil, fmt.Errorf("bots: org store unavailable (org=%s)", org.Slug)
	}
	member, err := data.Teams().IsTeamMember(ctx, u.ID, s.TeamSlug)
	if err != nil || !member {
		return nil, nil, store.ErrBotForbidden
	}
	const daemonAuthentication execution.AuthMethod = "daemon_owner"
	const daemonSurface execution.Surface = "daemon"
	owner, err := execution.WithPrincipal(botWorkerContext{ctx}, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: daemonAuthentication, Surface: daemonSurface, Subject: u.ID, Actor: w.config.WorkerID, OrgSlug: org.Slug, TeamSlug: s.TeamSlug, Authenticated: true})
	if err != nil {
		return nil, nil, err
	}
	owner = store.WithUserID(owner, u.ID)
	owner = store.WithOrgSlug(owner, org.Slug)
	owner = store.WithTeamSlug(owner, s.TeamSlug)
	// Preserve only the bounded immutable-read operation across the value-free
	// identity boundary. Directory membership and authority are always rechecked.
	owner = entstore.CopyBotExecutionObserver(owner, ctx)
	if w.config.ObserveExecution != nil {
		owner = entstore.WithBotExecutionObserver(owner, w.config.ObserveExecution)
	}
	owner = entstore.WithBotContentReadOperationFromContext(owner, ctx)
	owner = store.WithBotRunStateNotifier(owner, w.NotifyRunState)
	return owner, data, nil
}

// currentInteractivePrincipal checks live directory membership while retaining the
// original authentication method and scopes. A daemon identity is never a ceiling.
func (w *BotWorker) currentInteractivePrincipal(ctx context.Context, original execution.Principal) (execution.Principal, error) {
	_, data, err := w.authenticateOwner(ctx, BotSchedulerScope{original.OrgSlug, original.TeamSlug, original.Subject})
	if err != nil {
		return execution.Principal{}, err
	}
	org, err := w.config.Backend.Organizations().GetBySlug(ctx, original.OrgSlug)
	if err != nil || org == nil {
		return execution.Principal{}, store.ErrBotForbidden
	}
	role, err := w.config.Backend.Organizations().GetMemberRole(ctx, original.Subject, org.ID)
	if err != nil || role == "" || role == "viewer" {
		return execution.Principal{}, store.ErrBotForbidden
	}
	// Team roles are independent of organization membership. A live demotion to
	// viewer must revoke execution even when the organization role is member.
	team, err := data.Teams().GetTeamBySlug(ctx, original.TeamSlug)
	if err != nil || team == nil {
		return execution.Principal{}, store.ErrBotForbidden
	}
	teamRole, err := data.Teams().GetMemberRole(ctx, original.Subject, team.ID)
	if err != nil || teamRole == "" || teamRole == "viewer" {
		return execution.Principal{}, store.ErrBotForbidden
	}
	original.Roles = []string{role}
	return original, nil
}

type botMeteredRuntime struct{ bot.Runtime }
type botControlGuard struct{}

func (botControlGuard) Authorize(context.Context, execution.ActionDescriptor) (execution.PolicyDecision, error) {
	return execution.PolicyDecision{Kind: execution.PolicyDeny, Reason: "control conversation cannot execute tools"}, store.ErrBotForbidden
}

type botEventLeaseKey struct{}

func (r botMeteredRuntime) Start(ctx context.Context, q bot.RunRequest, s bot.EventSink) (out bot.RunResult, err error) {
	ctx, endStartupReads := launcher.WithBotStartupReads(ctx)
	defer endStartupReads()
	defer func() {
		if b, ok := q.Budget.(interface{ Usage() store.BotUsage }); ok {
			out.Usage = b.Usage()
		}
	}()
	if q.Run.ControlOnly() && !q.Run.IsInteractiveChat() {
		ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Guard: botControlGuard{}, Classifier: launcher.BotCatalogueClassifier{}})
	}
	return r.Runtime.Start(context.WithValue(api.WithBotOperationWakeups(ctx), botEventLeaseKey{}, q.Run.Lease), q, s)
}

func (r botMeteredRuntime) Resume(ctx context.Context, q bot.RunRequest, s bot.EventSink) (out bot.RunResult, err error) {
	ctx, endStartupReads := launcher.WithBotStartupReads(ctx)
	defer endStartupReads()
	defer func() {
		if b, ok := q.Budget.(interface{ Usage() store.BotUsage }); ok {
			out.Usage = b.Usage()
		}
	}()
	return r.Runtime.Resume(context.WithValue(api.WithBotOperationWakeups(ctx), botEventLeaseKey{}, q.Run.Lease), q, s)
}

type botWorkerContext struct{ context.Context }

func (botWorkerContext) Value(any) any { return nil }

func (w *BotWorker) loadScope(ctx context.Context, r bot.RunRequest) (*launcher.BotChatScope, error) {
	_, org, err := w.authenticateOwner(ctx, BotSchedulerScope{r.Principal.OrgSlug, r.Principal.TeamSlug, r.Bot.OwnerID.String()})
	if err != nil {
		return nil, err
	}
	svc, err := launcher.BotChatServicesForOwner(org, r.Principal, r.Bot.Scope)
	if err != nil {
		return nil, err
	}
	// Shared Chat captures these authoritative directories before stripping the
	// tenant router from team tool services. Admission-time membership alone is
	// not sufficient: each subsequent model/tool dispatch must recheck it.
	svc.Platform, svc.TenantRouter = w.config.Backend, w.config.Backend
	svc.BotFilesFactory = w.filesForRequest
	svc.BotRunStateNotify = w.NotifyRunState
	svc.PlatformSettings, svc.OrgSettings = w.config.Backend.PlatformSettings(), w.config.Backend.OrgSettings(r.Principal.OrgSlug)
	svc.MCPServers = org.OrgMCPServers()
	if networks, ok := w.config.Backend.(interface {
		PlatformNetworkPolicies() store.NetworkPolicyStore
	}); ok {
		svc.PlatformNetworkPolicies = networks.PlatformNetworkPolicies()
	} else if r.Mandate != nil {
		return nil, store.ErrBotUnsupported
	}
	svc.BotAuthorizedOperationFactory = func(_ context.Context, services *store.Services, scope store.BotScope) (store.BotAuthorizedOperationStore, error) {
		repo := services.Bots
		if scope == store.BotScopePersonal {
			repo = services.PersonalBots
		} else if scope != store.BotScopeTeam {
			return nil, store.ErrBotForbidden
		}
		return w.AuthorizedOperations(repo)
	}
	svc.NetworkPolicies = org.OrgNetworkPolicies()
	svc.TeamNetworkPolicies = org.ForTeam(r.Principal.TeamSlug).NetworkPolicies()
	ss := svc.Sessions
	var pin func(context.Context, string) (*store.SessionPin, error)
	if r.Bot.Scope == store.BotScopePersonal {
		personal := org.ForUser(r.Principal.Subject)
		if personal == nil {
			return nil, fmt.Errorf("bots: personal store unavailable")
		}
		ss = svc.PersonalSessions
		pin = personal.SessionPin
	} else {
		team := org.ForTeam(r.Principal.TeamSlug)
		if team == nil {
			return nil, fmt.Errorf("bots: team store unavailable")
		}
		pin = team.SessionPin
	}
	// The persistent home pin overrides owner-scoped inherited defaults. A run
	// session pin is never a configurable override; failed settings reads fail closed.
	resolver := launcher.BotChatModelResolver{SessionPin: func(c context.Context, _ string) (*store.SessionPin, error) {
		selected, err := pin(c, r.Bot.HomeSessionID.String())
		if err != nil {
			return nil, err
		}
		resolved, err := store.ResolveBotModelSelection(c, selected, w.config.Backend.PlatformSettings(), w.config.Backend.OrgSettings(r.Principal.OrgSlug), svc.Settings, svc.PersonalSettings)
		return &resolved, err
	}, ResolveProvider: func(c context.Context, p, m string) (model.LLM, error) {
		if r.Mandate != nil {
			if err := r.Mandate.Check(c); err != nil {
				return nil, err
			}
		}
		llm, err := w.config.ResolveModel(c, r.Principal, r.Bot.Scope, p, m)
		if err != nil {
			return llm, err
		}
		if configured, ok := llm.(*botGenerationModel); ok {
			copy := *configured
			copy.LLM = observeBotResolvedModel(r, w.config.WorkerID, p, m, configured.LLM)
			return &copy, nil
		}
		return observeBotResolvedModel(r, w.config.WorkerID, p, m, llm), nil
	}}
	return &launcher.BotChatScope{Services: svc, Sessions: ss, Models: resolver, OwnerEmail: w.ownerEmailResolver(r), Cleanup: func() {}}, nil
}

func (w *BotWorker) Start(ctx context.Context) {
	w.lifecycleMu.Lock()
	if w.started || w.activationStopped.Load() {
		w.lifecycleMu.Unlock()
		return
	}
	w.started = true
	ctx, w.cancel = context.WithCancel(ctx)
	loops, cancel := context.WithCancel(ctx)
	w.loopCancel = cancel
	w.initDispatchLocked()
	w.wg.Add(3)
	w.lifecycleMu.Unlock()
	go func() {
		defer w.wg.Done()
		for {
			select {
			case <-loops.Done():
				return
			case <-w.dispatchWake:
			}
			w.dispatchPending(ctx)
		}
	}()
	go w.workerLoop(loops, func(c context.Context) error { return w.scan(c, botScanDiscovery) })
	go w.workerLoop(loops, func(c context.Context) error { return w.scan(c, botScanMaintenance) })
}

func (w *BotWorker) workerLoop(ctx context.Context, work func(context.Context) error) {
	defer w.wg.Done()
	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()
	for {
		if err := work(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[bots] discovery/maintenance: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Drain requests a durable handoff at the next settled progress boundary.
// Legacy work is allowed to finish; caller deadline then falls back to Stop's
// cancellation semantics, never claiming an unsafe checkpoint was committed.
func (w *BotWorker) Drain(ctx context.Context) error {
	w.lifecycleMu.Lock()
	w.activationStopped.Store(true)
	w.progressDrain.Store(true)
	if w.loopCancel != nil {
		w.loopCancel()
	}
	w.lifecycleMu.Unlock()
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		w.Stop()
		return nil
	case <-ctx.Done():
		w.Stop()
		return ctx.Err()
	}
}

func (w *BotWorker) Stop() {
	w.lifecycleMu.Lock()
	w.activationStopped.Store(true)
	if w.cancel != nil {
		w.cancel()
	}
	for _, cancel := range w.activeCancels {
		cancel()
	}
	for _, cancel := range w.maintenanceCancel {
		cancel()
	}
	for _, cancel := range w.pollCancels {
		cancel()
	}
	w.lifecycleMu.Unlock()
	w.wg.Wait()
}

// Poll enumerates trusted organization membership independently of messaging
// channels. Repository authorization is repeated for every read/claim/action.
func (w *BotWorker) Poll(ctx context.Context) error {
	w.lifecycleMu.Lock()
	if w.activationStopped.Load() {
		w.lifecycleMu.Unlock()
		return context.Canceled
	}
	ctx, cancel := context.WithCancel(ctx)
	w.pollSerial++
	serial := w.pollSerial
	if w.pollCancels == nil {
		w.pollCancels = make(map[uint64]context.CancelFunc)
	}
	w.pollCancels[serial] = cancel
	w.wg.Add(1)
	w.lifecycleMu.Unlock()
	defer func() {
		cancel()
		w.lifecycleMu.Lock()
		delete(w.pollCancels, serial)
		w.lifecycleMu.Unlock()
		w.wg.Done()
	}()
	return w.scan(ctx, botScanSynchronous)
}

func (w *BotWorker) scan(ctx context.Context, mode botScanMode) error {
	ctx = context.WithValue(ctx, botScanModeKey{}, mode)
	var cleanupErr error
	if mode != botScanDiscovery {
		cleanupErr = w.retryMandateCleanup(ctx)
	}
	orgs, err := w.config.Backend.Organizations().List(ctx)
	if err != nil {
		return errors.Join(cleanupErr, err)
	}
	var failures []error
	if cleanupErr != nil {
		failures = append(failures, cleanupErr)
	}
	for _, org := range orgs {
		if org.Status != "active" {
			continue
		}
		data, err := w.config.Backend.ForOrg(org.Slug)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if data == nil || data.Teams() == nil {
			failures = append(failures, fmt.Errorf("bots: org store unavailable (org=%s)", org.Slug))
			continue
		}
		members, err := w.config.Backend.Organizations().ListMembers(ctx, org.ID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, member := range members {
			teams, err := data.Teams().ListTeamsForUser(ctx, member.ID)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			for _, team := range teams {
				owner, _, err := w.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, member.ID})
				if err != nil {
					failures = append(failures, fmt.Errorf("bots: authenticate owner (org=%s team=%s owner=%s): %w", org.Slug, team.Slug, member.ID, err))
					continue
				}
				for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
					var repo store.BotStore
					var ss store.SessionStore
					if scope == store.BotScopePersonal {
						personal := data.ForUser(member.ID)
						if personal != nil {
							repo, ss = personal.Bots(), personal.Sessions()
						}
					} else {
						teamData := data.ForTeam(team.Slug)
						if teamData != nil {
							repo, ss = teamData.Bots(), teamData.Sessions()
						}
					}
					if repo == nil || ss == nil {
						failures = append(failures, fmt.Errorf("bots: %s store unavailable (org=%s team=%s owner=%s)", scope, org.Slug, team.Slug, member.ID))
						continue // Never substitute another scope or treat unavailable data as empty.
					}
					if err := w.scanStore(owner, repo, ss, mode); err != nil {
						failures = append(failures, err)
					}
				}
			}
		}
	}
	return errors.Join(failures...)
}
func (w *BotWorker) pollStore(ctx context.Context, repo store.BotStore, ss store.SessionStore) error {
	return w.scanStore(ctx, repo, ss, botScanSynchronous)
}
func (w *BotWorker) scanStore(ctx context.Context, repo store.BotStore, ss store.SessionStore, mode botScanMode) error {
	p, authenticated := execution.PrincipalFromContext(ctx)
	if !authenticated || p.Validate() != nil {
		return store.ErrBotForbidden
	}
	var failures []error
	page := store.BotPage{Limit: 200}
	for {
		bots, err := repo.List(ctx, page)
		if err != nil {
			return err
		}
		for _, b := range bots {
			if b.OwnerID.String() != p.Subject {
				continue
			}
			if mode == botScanDiscovery {
				if !b.PermanentDelete {
					if err := w.discoverBot(ctx, repo, b); err != nil {
						failures = append(failures, err)
					}
				}
				continue
			}
			if mode == botScanMaintenance {
				w.lifecycleMu.Lock()
				w.initDispatchLocked()
				busy := w.activeBots[b.ID] || w.maintenanceBots[b.ID]
				var maintenanceCtx context.Context
				var cancel context.CancelFunc
				if !busy {
					maintenanceCtx, cancel = context.WithCancel(ctx)
					w.maintenanceBots[b.ID] = true
					w.maintenanceCancel[b.ID] = cancel
				}
				w.lifecycleMu.Unlock()
				if busy {
					continue
				}
				err := w.maintainBot(maintenanceCtx, repo, ss, b)
				cancel()
				w.lifecycleMu.Lock()
				delete(w.maintenanceBots, b.ID)
				delete(w.maintenanceCancel, b.ID)
				w.wakeDispatchLocked()
				w.lifecycleMu.Unlock()
				if err != nil {
					failures = append(failures, err)
				}
				continue
			}
			// Permanent deletion is a control-state cleanup path. After physical
			// cleanup, run content may already be gone while SQL purge is pending.
			// Never hydrate that content before consuming the durable cleanup receipt.
			if b.PermanentDelete {
				if err := w.reconcileRetention(ctx, repo, b); err != nil {
					failures = append(failures, err)
				}
				continue
			}
			// Reconcile committed intents even without queued work, including
			// outcomes committed just before pause or worker restart.
			if err := w.reconcileAuthorizedOperations(ctx, repo, b.ID); err != nil {
				failures = append(failures, err)
			}
			if err := w.reconcilePublications(ctx, repo, b.ID); err != nil {
				failures = append(failures, err)
			}
			// Paused/stopped Bots still need expired attempts recovered; recovery
			// records uncertainty and never requeues the run.
			// Traverse all pages per lane: teardown first, owner turns next,
			// autonomous work last. UUID pagination must not decide priority.
			var lanes [3][]store.BotRun
			runPage := store.BotPage{Limit: 200}
			for {
				runs, err := repo.ListRuns(ctx, b.ID, runPage)
				if err != nil {
					return err
				}
				for _, r := range runs {
					lane := dispatchLane(r)
					lanes[lane] = append(lanes[lane], r)
				}
				if len(runs) < runPage.Limit {
					break
				}
				runPage.After = runs[len(runs)-1].ID.String()
			}
			for lane := botDispatchTeardown; lane <= botDispatchAutonomous; lane++ {
				runs := lanes[lane]
				for _, r := range runs {
					if r.MandateRef == nil && r.IsInteractiveChat() && r.CallerCeiling != nil && (r.Status == store.RunQueued || r.Status == store.RunWaitingApproval) {
						original := r.CallerCeiling.Principal
						// Personal stores are enumerated under every team. Only the
						// original caller's team can inspect or dispatch this turn.
						if original.Subject != p.Subject || original.OrgSlug != p.OrgSlug || original.TeamSlug != p.TeamSlug {
							continue
						}
						identity := execution.BotRunIdentity{BotID: b.ID, RunID: r.ID, Version: r.Version, ActorID: original.Subject, OrgSlug: original.OrgSlug, TeamSlug: original.TeamSlug}
						auth, authErr := w.config.TurnRegistry.Authorizer(identity, *r.CallerCeiling, w.currentInteractivePrincipal)
						if authErr == nil {
							caller, e := execution.WithPrincipal(botWorkerContext{ctx}, original)
							if e != nil {
								authErr = e
							} else {
								authErr = auth.Authorize(execution.WithBotRunIdentity(caller, identity), execution.CapabilityChat)
							}
						}
						if authErr != nil {
							continuations, ok := repo.(store.BotInteractiveContinuationStore)
							if !ok {
								failures = append(failures, store.ErrBotUnsupported)
							} else if _, e := continuations.RequireInteractiveContinuation(ctx, b.ID, r.ID); e != nil && !errors.Is(e, store.ErrBotConflict) {
								failures = append(failures, e)
							}
							continue
						}
					}
					if r.Status == store.RunInterrupted {
						if err := w.confirmCancelledRun(ctx, repo, b.ID, r.ID); err != nil && !errors.Is(err, store.ErrBotConflict) && !errors.Is(err, store.ErrBotUnsupported) {
							failures = append(failures, err)
						}
						continue // Verify teardown only; never replay interrupted work.
					}
					if r.Status == store.RunRunning && !time.Now().Before(r.Lease.ExpiresAt) {
						// The repository rechecks authoritative expiry and fence.
						recovery, ok := repo.(interface {
							RecoverRun(context.Context, store.BotLease) error
						})
						if !ok {
							failures = append(failures, store.ErrBotUnsupported)
							continue
						}
						if err := recovery.RecoverRun(ctx, r.Lease); err != nil && !errors.Is(err, store.ErrBotConflict) {
							failures = append(failures, err)
						}
						continue // Never automatically retry uncertain effects.
					}
					if r.Intent.AllowsStatus(b.Status) && r.Status == store.RunQueued {
						if err := w.execute(ctx, repo, ss, b.ID, r.ID); err != nil && !errors.Is(err, store.ErrBotConflict) {
							log.Printf("[bots] run %s: %v", r.ID, err)
						}
					}
				}

			}
			if err := w.reconcileKnowledge(ctx, repo, ss, b); err != nil {
				failures = append(failures, err)
			}
			if err := w.reconcileRetention(ctx, repo, b); err != nil {
				failures = append(failures, err)
			}
		}
		if len(bots) < page.Limit {
			return errors.Join(failures...)
		}
		page.After = bots[len(bots)-1].ID.String()
	}
}

// botRunBudget restores previously committed totals. Fresh admissions have no
// prior lease/checkpoint; a legacy resumed ledger without runtime fails closed
// against elapsed time rather than granting another full runtime allowance.
func botRunBudget(in store.BotRunInput, now time.Time) (*bot.SharedRunBudget, error) {
	u := in.Run.Usage
	if in.Run.Lease.Fence == 0 && in.Run.StartedAt == nil && len(in.Run.Checkpoint) == 0 && u == (store.BotUsage{}) {
		return bot.NewRunBudget(in.Version.Budget, now)
	}
	if u.RuntimeNanos == 0 && !in.Run.RuntimeAccountingRecorded {
		if in.Run.StartedAt == nil || in.Run.StartedAt.After(now) {
			return nil, bot.ErrBudgetReservation
		}
		u.RuntimeNanos = int64(now.Sub(*in.Run.StartedAt))
	}
	return bot.RestoreRunBudget(in.Version.Budget, now, u)
}

const (
	botWorkerLeaseDuration = 5 * time.Minute
	botWorkerRenewInterval = 20 * time.Second
)

func (w *BotWorker) execute(ctx context.Context, repo store.BotStore, ss store.SessionStore, id store.BotID, runID store.RunID) error {
	// Preflight, readiness, claim and Home binding are one synchronous startup
	// operation. Do not carry its immutable bytes into the runtime/model loop.
	ctx, endStartupReads := entstore.WithBotContentReadOperation(ctx)
	defer endStartupReads() // includes every pre-runtime failure
	ctx = w.knowledgeCommitContext(ctx, repo, ss)
	homeTiming := entstore.ObserveBotExecutionStage(ctx, entstore.BotStageHomeSession)
	if err := w.prepareExistingBotHomeSession(ctx, repo, id); err != nil {
		homeTiming(err)
		return err
	}
	if err := w.prepareInteractiveHome(ctx, repo, id, runID); err != nil {
		homeTiming(err)
		return err
	}
	homeTiming(nil)
	homes := &launcher.BotHomeManager{Backend: w.config.Sandbox.(sandbox.BotHomeBackend), Pool: w.config.Pool, Bots: repo}
	supervisor, err := bot.NewSupervisor(bot.SupervisorConfig{BeginHeartbeatReads: entstore.WithBotContentReadOperation, ProgressYieldRequested: w.progressDrain.Load, Store: repo, Runtime: botStartupReadRuntime{Runtime: w.runtime, end: endStartupReads}, Homes: homes, WorkerID: w.config.WorkerID, LeaseDuration: botWorkerLeaseDuration, RenewInterval: botWorkerRenewInterval, Ready: func(c context.Context, in store.BotRunInput) (bot.RunPreparation, error) {
		identity, _ := execution.BotRunIdentityFromContext(c)
		if !in.Run.ExecutionMode.Valid() {
			return bot.RunPreparation{}, store.ErrBotForbidden
		}
		if in.Run.MandateRef != nil {
			return w.prepareMandate(c, repo, in, identity)
		}
		if in.Run.IsInteractiveChat() {
			if in.Run.CallerCeiling == nil {
				return bot.RunPreparation{}, store.ErrBotForbidden
			}
			auth, err := w.config.TurnRegistry.Authorizer(identity, *in.Run.CallerCeiling, w.currentInteractivePrincipal)
			if err != nil {
				return bot.RunPreparation{}, fmt.Errorf("continuation_required: %w", err)
			}
			if err := auth.Authorize(c, execution.CapabilityChat); err != nil {
				return bot.RunPreparation{}, err
			}
			budget, err := botRunBudget(in, time.Now())
			return bot.RunPreparation{Authorization: auth, Budget: budget}, err
		}
		allowed := in.Version.Autonomy.AllowedTools
		if in.Run.ControlOnly() {
			allowed = nil
		}
		policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: in.Version.Autonomy.Level, AllowedTools: allowed, DeniedTools: in.Version.Autonomy.DeniedTools})
		if err != nil {
			return bot.RunPreparation{}, err
		}
		budget, err := botRunBudget(in, time.Now())
		return bot.RunPreparation{Policy: policy, Budget: budget}, err
	}, Events: NewBotDurableEventSink(repo, ss, w.config.Publish)})
	if err != nil {
		return err
	}
	_, err = supervisor.Execute(ctx, id, runID)
	// Execute may commit a failed/waiting outcome and still return an error.
	// A cancelled context leaves the intent for the next authenticated poll.
	return errors.Join(err, w.confirmCancelledRun(ctx, repo, id, runID), w.reconcileAuthorizedOperations(ctx, repo, id), w.reconcilePublications(ctx, repo, id))
}

func (w *BotWorker) reconcilePublications(ctx context.Context, repo store.BotStore, id store.BotID) error {
	var notifier bot.Notifier
	if w.config.ChannelManager != nil && w.config.ChannelManager() != nil {
		authority, _ := repo.(store.BotDeliveryAuthorizer)
		notifier = botChannelNotifier{getManager: w.config.ChannelManager, authority: authority}
	}
	return ReconcileBotPublications(ctx, repo, id, notifier, func(c context.Context, p *store.BotPublication) error { return w.indexPublication(c, repo, p) })
}

// ChannelManager has no provider-receipt API. Never invent a provider ID from
// the attempt ID: even a successful Send is durably uncertain and not retried.
// Resolving the getter per send avoids retaining a manager across reloads.
type botChannelNotifier struct {
	getManager func() *channels.ChannelManager
	authority  store.BotDeliveryAuthorizer
}

func (n botChannelNotifier) Send(ctx context.Context, q bot.NotificationRequest) (bot.NotificationReceipt, error) {
	if q.Decision != store.NotificationSend || q.Target.ID == uuid.Nil || q.Target.Channel == "" || q.Target.ChatID == "" || q.ID == uuid.Nil {
		return bot.NotificationReceipt{}, store.ErrBotInvalid
	}
	if n.getManager == nil {
		return bot.NotificationReceipt{}, store.ErrBotUnsupported
	}
	manager := n.getManager()
	if manager == nil {
		return bot.NotificationReceipt{}, store.ErrBotUnsupported
	}
	err := manager.SendBotNotification(ctx, n.authority, q.BotID, q.Target, channels.OutboundMessage{Text: q.Body, ThreadID: q.Target.ThreadID, Format: channels.FormatText})
	return bot.NotificationReceipt{Uncertain: true}, err
}

// NewBotDurableEventSink stores the original wire envelope in the existing
// session transcript before optional publication. It never creates a session.
func NewBotDurableEventSink(repo store.BotStore, ss store.SessionStore, publish bot.EventSink) bot.EventSink {
	return func(ctx context.Context, e bot.RuntimeEvent) error {
		id, ok := execution.BotRunIdentityFromContext(ctx)
		p, authenticated := execution.PrincipalFromContext(ctx)
		if !ok || !authenticated || repo == nil || ss == nil || id.RunID != e.RunID || p.Subject != id.ActorID || e.Sequence < 1 || e.TurnID == uuid.Nil || !json.Valid(e.Payload) {
			return store.ErrBotForbidden
		}
		lease, fenced := ctx.Value(botEventLeaseKey{}).(store.BotLease)
		if !fenced || lease.BotID != id.BotID || lease.RunID != e.RunID {
			return store.ErrBotConflict
		}
		atomicStore, supported := repo.(store.BotTranscriptStore)
		if !supported {
			return fmt.Errorf("%w: atomic fenced Bot transcript persistence required", store.ErrBotUnsupported)
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		event := &session.Event{ID: uuid.NewSHA1(e.TurnID, []byte(fmt.Sprint(e.Sequence))).String(), InvocationID: e.TurnID.String(), Author: "bot_runtime", Timestamp: time.Now(), Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": json.RawMessage(raw)}}}
		if err := atomicStore.AppendBotRunEvent(ctx, lease, e.SessionID, event); err != nil {
			return err
		}
		if publish != nil {
			return publish(ctx, e)
		}
		return nil
	}
}

func (w *BotWorker) SchedulerCallbacks() BotSchedulerCallbacks {
	return BotSchedulerCallbacks{installed: func() { w.schedulerReady.Store(true) }, Runtime: w.runtime, Resolve: func(ctx context.Context, r scheduler.BotTriggerRequest) (context.Context, store.BotScheduledRunStore, error) {
		route, ok := BotSchedulerScopeFromContext(ctx)
		if !ok {
			return nil, nil, store.ErrBotForbidden
		}
		org, err := w.config.Backend.ForOrg(route.OrgSlug)
		if err != nil {
			return nil, nil, err
		}
		// Team schedules have no trusted owner in their routing hint. Resolve from
		// the durable Bot using live members; request OwnerID is never authority.
		members, err := w.config.Backend.Organizations().GetBySlug(ctx, route.OrgSlug)
		if err != nil {
			return nil, nil, err
		}
		if members == nil {
			return nil, nil, store.ErrBotForbidden
		}
		users, err := w.config.Backend.Organizations().ListMembers(ctx, members.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, u := range users {
			if route.UserID != "" && route.UserID != u.ID {
				continue
			}
			candidate := route
			candidate.UserID = u.ID
			owner, _, err := w.authenticateOwner(ctx, candidate)
			if err != nil {
				continue
			}
			repo := org.ForTeam(route.TeamSlug).Bots()
			if route.UserID != "" {
				repo = org.ForUser(u.ID).Bots()
			}
			b, err := repo.Get(owner, r.BotID)
			if err != nil || b == nil || b.OwnerID.String() != u.ID {
				continue
			}
			scheduled, ok := repo.(store.BotScheduledRunStore)
			if !ok {
				return nil, nil, store.ErrBotUnsupported
			}
			return store.WithBotMandateAdmission(owner, w.config.MandatePolicy), scheduled, nil
		}
		return nil, nil, store.ErrBotForbidden
	}}
}

// botDispatchLane is scheduling priority only; repository claims remain authority.
type botDispatchLane uint8

const (
	botDispatchTeardown botDispatchLane = iota
	botDispatchOwner
	botDispatchAutonomous
)

type botScanModeKey struct{}
type botScanMode uint8

const (
	botScanSynchronous botScanMode = iota
	botScanDiscovery
	botScanMaintenance
)

type botDispatchKey struct {
	botID store.BotID
	runID store.RunID
}
type botDispatchHint struct {
	key      botDispatchKey
	route    BotSchedulerScope
	scope    store.BotScope
	sequence int64
	lane     botDispatchLane
}

const botDispatchMailboxCapacity = 256

func (w *BotWorker) initDispatchLocked() {
	if w.pending == nil {
		w.pending = make(map[botDispatchKey]botDispatchHint)
	}
	if w.activeBots == nil {
		w.activeBots = make(map[store.BotID]bool)
	}
	if w.activeCancels == nil {
		w.activeCancels = make(map[store.BotID]context.CancelFunc)
	}
	if w.maintenanceBots == nil {
		w.maintenanceBots = make(map[store.BotID]bool)
	}
	if w.maintenanceCancel == nil {
		w.maintenanceCancel = make(map[store.BotID]context.CancelFunc)
	}
	if w.dispatchWake == nil {
		w.dispatchWake = make(chan struct{}, 1)
	}
}
func (w *BotWorker) wakeDispatchLocked() {
	select {
	case w.dispatchWake <- struct{}{}:
	default:
	}
}

// NotifyRunState copies routing identifiers only. In particular no request context,
// principal, content or authentication evidence survives this postcommit callback.
func (w *BotWorker) NotifyRunState(ctx context.Context, state store.BotRunState) {
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || state.BotID == uuid.Nil || state.RunID == uuid.Nil {
		return
	}
	hint := botDispatchHint{key: botDispatchKey{state.BotID, state.RunID}, route: BotSchedulerScope{p.OrgSlug, p.TeamSlug, p.Subject}, sequence: state.StateSequence, lane: botDispatchAutonomous}
	if state.Presentation == store.BotRunPresentationHomeInteractive {
		hint.lane = botDispatchOwner
	}
	w.enqueueDispatch(hint, state.Status == store.RunQueued)
}
func (w *BotWorker) enqueueDispatch(h botDispatchHint, queued bool) {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if w.activationStopped.Load() {
		return
	}
	w.initDispatchLocked()
	old, exists := w.pending[h.key]
	if exists && h.sequence < old.sequence {
		return
	}
	if !queued {
		delete(w.pending, h.key)
		return // a newer nonqueued state invalidates acceleration, not effects
	}
	if !exists && len(w.pending) >= botDispatchMailboxCapacity {
		w.dispatchOverflow.Add(1)
		return // only acceleration is lost; independent discovery reads durable queues
	}
	w.pending[h.key] = h
	if h.lane == botDispatchOwner {
		if cancel := w.maintenanceCancel[h.key.botID]; cancel != nil {
			cancel() // owner acceleration interrupts reconciliation, never execution
		}
		log.Printf("[bots] dispatch enqueue bot_id=%s run_id=%s lane=%d sequence=%d active=%d owner=%d autonomous=%d pending=%d", h.key.botID, h.key.runID, h.lane, h.sequence, w.activeSlots, w.ownerSlots, w.autonomousSlots, len(w.pending))
	}
	w.wakeDispatchLocked()
}

func dispatchLane(r store.BotRun) botDispatchLane {
	if r.Status != store.RunQueued && r.Status != store.RunWaitingApproval {
		return botDispatchTeardown
	}
	if r.Presentation == store.BotRunPresentationHomeInteractive && r.Trigger != store.TriggerCron {
		return botDispatchOwner
	}
	return botDispatchAutonomous
}

// discoverBot scans each page once, partitioning metadata into typed lanes. No
// runtime or cleanup is performed on the discovery goroutine.
func (w *BotWorker) discoverBot(ctx context.Context, repo store.BotStore, b store.Bot) error {
	p, _ := execution.PrincipalFromContext(ctx)
	page := store.BotPage{Limit: 200}
	for {
		runs, err := repo.ListRuns(ctx, b.ID, page)
		if err != nil {
			return err
		}
		for _, r := range runs {
			if r.CallerCeiling != nil && r.MandateRef == nil {
				original := r.CallerCeiling.Principal
				if original.Subject != p.Subject || original.OrgSlug != p.OrgSlug || original.TeamSlug != p.TeamSlug {
					continue
				}
			}
			if r.Status != store.RunQueued {
				continue
			}
			w.enqueueDispatch(botDispatchHint{key: botDispatchKey{b.ID, r.ID}, route: BotSchedulerScope{p.OrgSlug, p.TeamSlug, p.Subject}, scope: b.Scope, sequence: r.StateSequence, lane: dispatchLane(r)}, true)
		}
		if len(runs) < page.Limit {
			return nil
		}
		page.After = runs[len(runs)-1].ID.String()
	}
}

const (
	botDispatchBaseSlots  = 2
	botDispatchOwnerBurst = 1
)

func (w *BotWorker) dispatchPending(ctx context.Context) {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	for !w.activationStopped.Load() && ctx.Err() == nil {
		var chosen botDispatchHint
		found := false
		for _, h := range w.pending {
			if w.activeBots[h.key.botID] || (h.lane == botDispatchAutonomous && w.autonomousSlots >= 1) {
				continue
			}
			limit := botDispatchBaseSlots
			if h.lane == botDispatchOwner {
				limit += botDispatchOwnerBurst
			}
			if w.activeSlots >= limit {
				continue
			}
			if !found || h.lane < chosen.lane {
				chosen, found = h, true
			}
		}
		if !found {
			return
		}
		delete(w.pending, chosen.key)
		w.activeBots[chosen.key.botID] = true
		w.activeSlots++
		if chosen.lane == botDispatchOwner {
			w.ownerSlots++
		}
		if chosen.lane == botDispatchAutonomous {
			w.autonomousSlots++
		}
		log.Printf("[bots] dispatch start bot_id=%s run_id=%s lane=%d active=%d owner=%d autonomous=%d pending=%d", chosen.key.botID, chosen.key.runID, chosen.lane, w.activeSlots, w.ownerSlots, w.autonomousSlots, len(w.pending))
		runCtx, cancel := context.WithCancel(ctx)
		w.activeCancels[chosen.key.botID] = cancel
		w.wg.Add(1) // admission and Stop/Drain's closed gate use the same mutex
		go func(h botDispatchHint) {
			defer w.wg.Done()
			defer func() {
				cancel()
				w.lifecycleMu.Lock()
				delete(w.activeBots, h.key.botID)
				delete(w.activeCancels, h.key.botID)
				w.activeSlots--
				if h.lane == botDispatchOwner {
					w.ownerSlots--
				}
				if h.lane == botDispatchAutonomous {
					w.autonomousSlots--
				}
				log.Printf("[bots] dispatch release bot_id=%s run_id=%s lane=%d active=%d owner=%d autonomous=%d pending=%d", h.key.botID, h.key.runID, h.lane, w.activeSlots, w.ownerSlots, w.autonomousSlots, len(w.pending))
				w.wakeDispatchLocked()
				w.lifecycleMu.Unlock()
			}()
			if err := w.executeHint(runCtx, h); err != nil && runCtx.Err() == nil && !errors.Is(err, store.ErrBotConflict) {
				log.Printf("[bots] dispatch: %v", err)
			}
		}(chosen)
	}
}

func (w *BotWorker) executeHint(ctx context.Context, h botDispatchHint) error {
	owner, org, err := w.authenticateOwner(ctx, h.route)
	if err != nil {
		return err
	}
	scopes := []store.BotScope{h.scope}
	if h.scope == "" {
		scopes = []store.BotScope{store.BotScopePersonal, store.BotScopeTeam}
	}
	for _, scope := range scopes {
		var repo store.BotStore
		var ss store.SessionStore
		if scope == store.BotScopePersonal {
			data := org.ForUser(h.route.UserID)
			if data != nil {
				repo, ss = data.Bots(), data.Sessions()
			}
		} else if scope == store.BotScopeTeam {
			data := org.ForTeam(h.route.TeamSlug)
			if data != nil {
				repo, ss = data.Bots(), data.Sessions()
			}
		}
		if repo == nil || ss == nil {
			continue
		}
		b, err := repo.Get(owner, h.key.botID)
		if err != nil || b == nil || b.OwnerID.String() != h.route.UserID || b.PermanentDelete {
			continue
		}
		r, err := repo.GetRun(owner, b.ID, h.key.runID)
		if err != nil {
			return err
		}
		if r.Status != store.RunQueued || !r.Intent.AllowsStatus(b.Status) {
			return nil
		}
		// Presentation is only an acceleration hint: a cron occurrence sharing
		// the Home must not consume the slot reserved for owner continuations.
		if lane := dispatchLane(*r); lane != h.lane {
			h.lane, h.scope, h.sequence = lane, b.Scope, r.StateSequence
			w.enqueueDispatch(h, true)
			return nil
		}
		if r.CallerCeiling != nil && r.MandateRef == nil {
			original := r.CallerCeiling.Principal
			if original.Subject != h.route.UserID || original.OrgSlug != h.route.OrgSlug || original.TeamSlug != h.route.TeamSlug {
				return store.ErrBotForbidden
			}
			identity := execution.BotRunIdentity{BotID: b.ID, RunID: r.ID, Version: r.Version, ActorID: original.Subject, OrgSlug: original.OrgSlug, TeamSlug: original.TeamSlug}
			auth, authErr := w.config.TurnRegistry.Authorizer(identity, *r.CallerCeiling, w.currentInteractivePrincipal)
			if authErr == nil {
				caller, e := execution.WithPrincipal(entstore.CopyBotExecutionObserver(botWorkerContext{owner}, owner), original)
				authErr = e
				if e == nil {
					authErr = auth.Authorize(execution.WithBotRunIdentity(caller, identity), execution.CapabilityChat)
				}
			}
			if authErr != nil {
				if continuations, ok := repo.(store.BotInteractiveContinuationStore); ok {
					_, err = continuations.RequireInteractiveContinuation(owner, b.ID, r.ID)
					return err
				}
				return authErr
			}
		}
		// Same-Bot teardown dependencies are mandatory even for an accelerated
		// owner hint. Another worker's live claim cannot be bypassed locally.
		page := store.BotPage{Limit: 200}
		for {
			runs, err := repo.ListRuns(owner, b.ID, page)
			if err != nil {
				return err
			}
			for _, old := range runs {
				if old.ID == r.ID {
					continue
				}
				if old.Status == store.RunRunning {
					if time.Now().Before(old.Lease.ExpiresAt) {
						return store.ErrBotConflict
					}
					// Owner admission must not wait for the maintenance ticker. Recover
					// the expired predecessor under its exact durable lease/fence, then
					// require the same physical teardown receipt as normal maintenance.
					recovery, ok := repo.(interface {
						RecoverRun(context.Context, store.BotLease) error
					})
					if !ok {
						return store.ErrBotUnsupported
					}
					if err := recovery.RecoverRun(owner, old.Lease); err != nil {
						return err
					}
					old.Status = store.RunInterrupted
				}
				if old.Status == store.RunInterrupted {
					if err := w.confirmCancelledRun(owner, repo, b.ID, old.ID); err != nil {
						return err
					}
				}
			}
			if len(runs) < page.Limit {
				break
			}
			page.After = runs[len(runs)-1].ID.String()
		}
		return w.execute(owner, repo, ss, b.ID, r.ID)
	}
	return store.ErrBotNotFound
}

// maintainBot owns only this Bot's control/teardown dependencies. Discovery and
// admission of unrelated Bots never join this maintenance call.
func (w *BotWorker) maintainBot(ctx context.Context, repo store.BotStore, ss store.SessionStore, b store.Bot) error {
	if b.PermanentDelete {
		return w.reconcileRetention(ctx, repo, b)
	}
	var failures []error
	page := store.BotPage{Limit: 200}
	for {
		runs, err := repo.ListRuns(ctx, b.ID, page)
		if err != nil {
			return err
		}
		for _, r := range runs {
			if r.Status == store.RunInterrupted {
				if err := w.confirmCancelledRun(ctx, repo, b.ID, r.ID); err != nil && !errors.Is(err, store.ErrBotConflict) && !errors.Is(err, store.ErrBotUnsupported) {
					failures = append(failures, err)
				}
			}
			if r.Status == store.RunRunning && !time.Now().Before(r.Lease.ExpiresAt) {
				if recovery, ok := repo.(interface {
					RecoverRun(context.Context, store.BotLease) error
				}); ok {
					if err := recovery.RecoverRun(ctx, r.Lease); err != nil && !errors.Is(err, store.ErrBotConflict) {
						failures = append(failures, err)
					}
				} else {
					failures = append(failures, store.ErrBotUnsupported)
				}
			}
		}
		if len(runs) < page.Limit {
			break
		}
		page.After = runs[len(runs)-1].ID.String()
	}
	failures = append(failures, w.reconcileAuthorizedOperations(ctx, repo, b.ID), w.reconcilePublications(ctx, repo, b.ID), w.reconcileKnowledge(ctx, repo, ss, b), w.reconcileRetention(ctx, repo, b))
	return errors.Join(failures...)
}
