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
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
)

// BotWorkerConfig is trusted daemon composition. Prepare must verify isolation;
// no ordinary Chat factory or host-tool fallback is used. ChannelManager is
// consulted only for delivery of already-authorized publication targets.
type BotWorkerConfig struct {
	MandatePolicy store.BotMandatePolicyResolver
	TurnRegistry  *execution.BotTurnAuthorizationRegistry
	Backend       store.PlatformBackend
	Sandbox       sandbox.Backend
	Pool          sandbox.ToolNodePool
	Prepare       func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error)
	ResolveModel  func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error)
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
	config            BotWorkerConfig
	runtime           bot.Runtime
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	mandateCleanupMu  sync.Mutex
	mandateCleanup    map[execution.BotRunIdentity]*bot.MandateExecution
}

func NewBotWorker(c BotWorkerConfig) (*BotWorker, error) {
	if c.Backend == nil || c.Sandbox == nil || c.Pool == nil || c.Prepare == nil || c.ResolveModel == nil || c.WorkerID == "" || c.ContextWindow <= 0 || c.ModelOptions.MaxOutputTokens <= 0 {
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
	if c.ChannelManager == nil {
		c.ChannelManager = api.GetChannelManager
	}
	c.Publish = installBotEventPublisher(c.Publish)
	w := &BotWorker{config: c}
	factory, err := launcher.NewBotChatAgentFactory(launcher.BotChatFactoryConfig{LoadScope: w.loadScope, Prepare: w.prepareAuthorizedSchedules, ContextWindow: c.ContextWindow, WrapModel: func(ctx context.Context, llm model.LLM, b execution.RunBudget) (model.LLM, error) {
		return provider.NewBudgetLLM(llm, b, c.ModelOptions)
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
		return w.config.ResolveModel(c, r.Principal, r.Bot.Scope, p, m)
	}}
	return &launcher.BotChatScope{Services: svc, Sessions: ss, Models: resolver, OwnerEmail: w.ownerEmailResolver(r), Cleanup: func() {}}, nil
}

func (w *BotWorker) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(w.config.PollInterval)
		defer ticker.Stop()
		for {
			if err := w.Poll(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[bots] poll: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (w *BotWorker) Stop() {
	w.activationStopped.Store(true)
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

// Poll enumerates trusted organization membership independently of messaging
// channels. Repository authorization is repeated for every read/claim/action.
func (w *BotWorker) Poll(ctx context.Context) error {
	cleanupErr := w.retryMandateCleanup(ctx)
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
					if err := w.pollStore(owner, repo, ss); err != nil {
						failures = append(failures, err)
					}
				}
			}
		}
	}
	return errors.Join(failures...)
}
func (w *BotWorker) pollStore(ctx context.Context, repo store.BotStore, ss store.SessionStore) error {
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
			// Reconcile committed intents even without queued work, including
			// outcomes committed just before pause or worker restart.
			if !b.PermanentDelete {
				if err := w.reconcileAuthorizedOperations(ctx, repo, b.ID); err != nil {
					failures = append(failures, err)
				}
				if err := w.reconcilePublications(ctx, repo, b.ID); err != nil {
					failures = append(failures, err)
				}
			}
			// Paused/stopped Bots still need expired attempts recovered; recovery
			// records uncertainty and never requeues the run.
			// Traverse all pages per lane: teardown first, owner turns next,
			// autonomous work last. UUID pagination must not decide priority.
			for lane := 0; lane < 3; lane++ {
				runPage := store.BotPage{Limit: 200}
				for {
					runs, err := repo.ListRuns(ctx, b.ID, runPage)
					if err != nil {
						return err
					}
					for _, r := range runs {
						runLane := 0
						if r.Status == store.RunQueued || r.Status == store.RunWaitingApproval {
							runLane = 2
							if r.Presentation == store.BotRunPresentationHomeInteractive && r.Trigger != store.TriggerCron {
								runLane = 1
							}
						}
						if runLane != lane {
							continue
						}
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
					if len(runs) < runPage.Limit {
						break
					}
					runPage.After = runs[len(runs)-1].ID.String()
				}
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
	if u.RuntimeNanos == 0 {
		if in.Run.StartedAt == nil || in.Run.StartedAt.After(now) {
			return nil, bot.ErrBudgetReservation
		}
		u.RuntimeNanos = int64(now.Sub(*in.Run.StartedAt))
	}
	return bot.RestoreRunBudget(in.Version.Budget, now, u)
}

func (w *BotWorker) execute(ctx context.Context, repo store.BotStore, ss store.SessionStore, id store.BotID, runID store.RunID) error {
	if err := w.prepareInteractiveHome(ctx, repo, id, runID); err != nil {
		return err
	}
	homes := &launcher.BotHomeManager{Backend: w.config.Sandbox.(sandbox.BotHomeBackend), Pool: w.config.Pool, Bots: repo}
	supervisor, err := bot.NewSupervisor(bot.SupervisorConfig{Store: repo, Runtime: w.runtime, Homes: homes, WorkerID: w.config.WorkerID, Ready: func(c context.Context, in store.BotRunInput) (bot.RunPreparation, error) {
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
	return ReconcileBotPublications(ctx, repo, id, notifier)
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
