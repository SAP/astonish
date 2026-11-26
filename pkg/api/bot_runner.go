package api

import (
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
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/question"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// BotChatResources transfers ownership of a freshly wired agent and its cleanup
// to one run. Factory implementations must call NewWiredChatAgent per invocation,
// never copy a singleton: tool closures, compaction, redaction and child managers
// must belong to this request. SessionService is the existing scoped store; the
// adapter never creates/replaces sessions or changes their execution owner.
type BotChatResources struct {
	// RecoverFileDelivery saves existing complete text once, without model or tools.
	RecoverFileDelivery func(context.Context, string) ([]store.BotArtifact, error)
	// RepairFileDelivery creates a new immutable Markdown revision under the same grant.
	RepairFileDelivery func(context.Context, string, int) ([]store.BotArtifact, error)
	// PublishedFiles reads trusted run-local snapshot receipts, never model JSON.
	// ArtifactBody opens only an exact trusted run-local immutable receipt.
	ArtifactBody   func(context.Context, store.BotArtifact) ([]byte, error)
	PublishedFiles func() []store.BotArtifact
	// ProgressDispatch attests that the factory wired this exact capability to
	// all models and executable tool views; nil keeps progress fail-closed.
	ProgressDispatch bot.ProgressDispatch
	// ModelSelection records the successful scoped resolver result, never ambient defaults.
	ModelSelection *store.SessionPin
	// AssessmentContext is a trusted, owner-bound read, never a model tool.
	// Callbacks are factory-bound to this owner/Bot/run; callers cannot choose scope.
	PriorRun                 func(context.Context) (*bot.BotPriorRunContext, error)
	PreviousWork             func(context.Context) (*store.BotWorkProjection, error)
	ValidateWorkContinuation func(context.Context, store.BotWorkContinuationKind, uuid.UUID) error
	EvidenceReader           store.BotTranscriptReader
	AssessmentContext        func(context.Context) (json.RawMessage, store.BotEvidence, error)
	Skills                   store.BotSkillStore
	Tasks                    store.BotTaskStore
	TaskWorkspace            bot.TaskWorkspace
	Redact                   func(string) string
	Work                     store.BotWorkStore
	Authorization            execution.BotTurnAuthorizer
	Transcript               store.BotTranscriptStore
	Agent                    *agent.ChatAgent
	SessionService           session.Service
	AppName                  string
	Cleanup                  func()
	// PolicyRuntime is prepared by the trusted factory and must be the same
	// runtime installed on its bound tools. Nil preserves explicit context wiring.
	PolicyRuntime *agent.BotPolicyRuntime
	// ResearchFactory constructs a fresh producer with only the configured public
	// read tools. The adapter installs its continuation runner automatically.
	ResearchFactory func(context.Context, BotEmployeeResearchRequest) (*BotChatResources, error)
}

// BotChatAgentFactory reverses the launcher -> api dependency. The authenticated
// context contains identity, policy and the shared budget before construction.
// Composition must wire budgeted models (including auxiliary/child calls), live
// policy guards, scoped stores and sandbox resources before returning the agent.
// It must clean partially constructed resources itself on failure, or return a
// resource with Cleanup alongside the error.
type BotChatAgentFactory func(context.Context, bot.RunRequest) (*BotChatResources, error)

type botChatBudgetKey struct{}

// BotChatRunBudgetFromContext exposes the mandatory shared budget to injected
// resource constructors without package globals. It is not a replacement for
// reservation/settlement at every model/tool boundary.
func BotChatRunBudgetFromContext(ctx context.Context) execution.RunBudget {
	b, _ := ctx.Value(botChatBudgetKey{}).(execution.RunBudget)
	return b
}

type botChatActiveRun struct {
	lease     store.BotLease
	principal execution.Principal
	cancel    context.CancelFunc
}

// BotChatRuntimeAdapter implements Runtime using the production ChatRunner. It
// is deliberately not automatically installed by any daemon. A trusted factory
// and supervisor readiness proof are required before enabling Bot execution.
type BotChatRuntimeAdapter struct {
	factory BotChatAgentFactory
	mu      sync.Mutex
	active  map[store.RunID]botChatActiveRun
}

var _ bot.Runtime = (*BotChatRuntimeAdapter)(nil)

func NewBotChatRuntimeAdapter(factory BotChatAgentFactory) (*BotChatRuntimeAdapter, error) {
	if factory == nil {
		return nil, store.ErrBotUnsupported
	}
	return &BotChatRuntimeAdapter{factory: factory, active: make(map[store.RunID]botChatActiveRun)}, nil
}

func botChatFailure(class bot.RunErrorClass, cause error) (bot.RunResult, error) {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		class = bot.ErrorInterrupted
	}
	report := store.BotReport{Summary: "I couldn't complete the requested work.", Failure: &store.BotReportFailure{Kind: store.BotReportFailureOperational, TechnicalDetails: "An operational failure stopped the requested work."}}
	if message, ok := bot.BudgetFailureMessage(cause); ok && class != bot.ErrorInterrupted {
		class = bot.ErrorBudget
		report.Summary = message
		report.Failure = &store.BotReportFailure{Kind: store.BotReportFailureBudget, TechnicalDetails: bot.BudgetFailureDetails(cause)}
	}
	if message, ok := bot.CheckpointFailureMessage(cause); ok && class != bot.ErrorInterrupted {
		class = bot.ErrorPermanent
		report.Summary = message
		report.Failure = &store.BotReportFailure{Kind: store.BotReportFailureExecutionFormat, TechnicalDetails: bot.CheckpointFailureDetails(cause)}
	}
	failure := &bot.RunError{Class: class, Message: report.Summary, Cause: cause, TerminalForAttempt: true}
	status := store.RunFailed
	if class == bot.ErrorInterrupted {
		status = store.RunInterrupted
		report.Failure.Kind = store.BotReportFailureInterrupted
	}
	var existing *bot.RunError
	if errors.As(cause, &existing) && existing != nil && existing.UncertainEffect {
		// Preserve the no-replay outcome and diagnostic wrappers without mutating
		// the caller's error, even when its cause is cancellation or a budget stop.
		preserved := *existing
		preserved.Cause = cause
		preserved.Message = "I couldn't confirm the outcome of the requested work."
		failure = &preserved
		status = store.RunPartial
		report.EffectState = store.BotReportEffectsUncertain
	}
	message := bot.EmployeeMessageFallback(status, report, cause)
	report.EmployeeMessage = &message
	return bot.RunResult{Status: status, Failure: failure, Report: report}, failure
}

// botChatFactoryFailure distinguishes a setup dependency's deadline from the
// worker-owned run deadline. Keep this at the factory boundary: no model/tool
// execution has started, and a dependency timeout must not imply a canceled run.
func botChatFactoryFailure(ctx context.Context, cause error) (bot.RunResult, error) {
	if ctx.Err() == nil && errors.Is(cause, context.DeadlineExceeded) && !errors.Is(cause, context.Canceled) {
		const message = "Run could not start because a setup dependency timed out."
		// Preserve the cause for diagnostics without putting endpoint URLs or
		// credentials from HTTP errors into the report or public error message.
		failure := &bot.RunError{Class: bot.ErrorPermanent, Message: message, Cause: cause, TerminalForAttempt: true}
		report := store.BotReport{Summary: message, Failure: &store.BotReportFailure{Kind: store.BotReportFailureSetup, TechnicalDetails: message}}
		envelope := bot.EmployeeMessageFallback(store.RunFailed, report, nil)
		report.EmployeeMessage = &envelope
		return bot.RunResult{Status: store.RunFailed, Failure: failure, Report: report}, failure
	}
	result, err := botChatFailure(bot.ErrorPermanent, cause)
	if result.Status == store.RunFailed && result.Report.Failure.Kind == store.BotReportFailureOperational {
		result.Report.Failure.Kind = store.BotReportFailureSetup
		message := bot.EmployeeMessageFallback(result.Status, result.Report, nil)
		result.Report.EmployeeMessage = &message
	}
	return result, err
}

func validateBotChatRequest(ctx context.Context, req bot.RunRequest, sink bot.EventSink) error {
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || !reflect.DeepEqual(p, req.Principal) || p.Subject != req.Bot.OwnerID.String() {
		return store.ErrBotForbidden
	}
	id := execution.BotRunIdentity{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Version.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
	if id.Validate() != nil || !req.Run.ExecutionMode.Valid() || (req.Run.RequiresAutonomousPolicy() && req.Policy == nil) || (req.Policy != nil && req.Policy.Identity() != id) {
		return store.ErrBotForbidden
	}
	if prior, ok := execution.BotRunIdentityFromContext(ctx); ok && prior != id {
		return store.ErrBotForbidden
	}
	if prior := execution.RunPolicyFromContext(ctx); prior != nil && prior != req.Policy {
		return store.ErrBotForbidden
	}
	if c := req.Run.TitleConfirmation; c != nil {
		if c.ValidateBinding(req.Run, req.Bot.OwnerID) != nil || (c.State != store.BotTitleConfirmationNeedsQuestion && c.State != store.BotTitleConfirmationConfirmed) {
			return store.ErrBotInvalid
		}
	}
	if err := botProgressActivationGuard(req); err != nil {
		return err
	}
	if req.Budget == nil || sink == nil {
		return store.ErrBotUnsupported
	}
	if !req.Run.Intent.AllowsStatus(req.Bot.Status) || req.Run.Status != store.RunRunning || req.Run.BotID != req.Bot.ID || req.Version.BotID != req.Bot.ID || req.Run.Version != req.Version.Version || req.Bot.Version != req.Version.Version || req.Run.SessionID == uuid.Nil || strings.TrimSpace(req.Instructions) == "" || (!req.Run.IsInteractiveChat() && req.Interactive) || len(req.Run.Checkpoint) != 0 {
		return store.ErrBotInvalid
	}
	if req.Run.MandateRef != nil && req.Mandate == nil {
		return store.ErrBotForbidden
	}
	if req.Mandate != nil {
		if req.Policy == nil || req.Interactive || req.Run.MandateRef == nil || *req.Run.MandateRef != req.Mandate.Authorization.Ref() || req.Mandate.Identity != id || !reflect.DeepEqual(p, req.Mandate.Authorization.Principal(p.Surface)) {
			return store.ErrBotForbidden
		}
		if err := req.Mandate.Check(ctx); err != nil {
			return err
		}
	}
	if req.Run.IsInteractiveChat() && req.Mandate == nil {
		if req.Run.CallerCeiling == nil || !reflect.DeepEqual(req.Run.CallerCeiling.Principal, req.Principal) || !time.Now().Before(req.Run.CallerCeiling.ExpiresAt) || req.Authorization == nil {
			return store.ErrBotForbidden
		}
		if err := req.Authorization.Authorize(ctx, execution.CapabilityChat); err != nil {
			return err
		}
	}
	lease := req.Run.Lease
	if lease.BotID != req.Bot.ID || lease.RunID != req.Run.ID || lease.Fence <= 0 || lease.WorkerID == "" || !time.Now().Before(lease.ExpiresAt) {
		return store.ErrBotConflict
	}
	if req.Version.Budget.MaxRuntimeSeconds <= 0 || req.Version.Budget.MaxRuntimeSeconds > int64((1<<63-1)/int64(time.Second)) {
		return store.ErrBotInvalid
	}
	return nil
}

func (a *BotChatRuntimeAdapter) Start(ctx context.Context, req bot.RunRequest, sink bot.EventSink) (bot.RunResult, error) {
	return a.runChat(ctx, req, sink, false)
}

func (a *BotChatRuntimeAdapter) runChat(ctx context.Context, req bot.RunRequest, sink bot.EventSink, resume bool) (result bot.RunResult, err error) {
	startup := entstore.ObserveBotExecutionStage(ctx, entstore.BotStageStartup)
	defer func() { startup(err) }()
	ownerSetup := entstore.ObserveBotExecutionStageSince(ctx, entstore.BotStageOwnerSetup, req.Run.CreatedAt)
	defer func() {
		if err != nil {
			ownerSetup(err)
		} else {
			ownerSetup(store.ErrBotConflict)
		}
	}() // a successful run with no committed text is not an acknowledgment
	originalSink := sink
	if sink != nil {
		sink = func(c context.Context, event bot.RuntimeEvent) error {
			err := originalSink(c, event)
			if err == nil && event.Type == "text" {
				ownerSetup(nil) // only after durable transcript sink acknowledgment
			}
			return err
		}
	}
	if req.Run.TitleConfirmation != nil && req.Run.TitleConfirmation.State == store.BotTitleConfirmationNeedsQuestion {
		req.Work = nil // clarification has no producer, journal or Task publication
	}
	validation := req
	if resume {
		validation.Run.Checkpoint = nil
	}
	if err := validateBotChatRequest(ctx, validation, sink); err != nil {
		return botChatFailure(bot.ErrorPermission, err)
	}
	ctx = execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Version.Version, ActorID: req.Principal.Subject, OrgSlug: req.Principal.OrgSlug, TeamSlug: req.Principal.TeamSlug})
	ctx = execution.WithRunPolicy(ctx, req.Policy)
	if req.Run.IsInteractiveChat() && req.Mandate == nil {
		ctx = execution.WithBotTurnAuthorizer(ctx, req.Authorization)
	}
	ctx = context.WithValue(ctx, botChatBudgetKey{}, req.Budget)
	ctx = entstore.WithImmutableReadWindow(ctx)
	defer entstore.EndImmutableReadWindow(ctx)
	ctx, _ = bot.WithToolOutcomes(ctx)
	ctx = store.WithSessionID(ctx, req.Run.SessionID.String())
	ctx = store.WithUserID(ctx, req.Bot.OwnerID.String())
	ctx = store.WithOrgSlug(ctx, req.Principal.OrgSlug)
	ctx = store.WithTeamSlug(ctx, req.Principal.TeamSlug)
	var expiry time.Time
	if req.Mandate != nil {
		expiry = req.Mandate.Authorization.Snapshot().ExpiresAt
	} else if req.Run.CallerCeiling != nil {
		expiry = req.Run.CallerCeiling.ExpiresAt
	}
	ctx, cancel, err := bot.RunContext(ctx, req.Version.Budget, req.Budget, req.Run.Usage, expiry)
	if err != nil {
		return botChatFailure(bot.ErrorBudget, err)
	}
	defer cancel()
	a.mu.Lock()
	if _, exists := a.active[req.Run.ID]; exists {
		a.mu.Unlock()
		return botChatFailure(bot.ErrorBlocked, store.ErrBotConflict)
	}
	a.active[req.Run.ID] = botChatActiveRun{lease: req.Run.Lease, principal: req.Principal, cancel: cancel}
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.active, req.Run.ID); a.mu.Unlock() }()
	var modelSelection *store.SessionPin
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = botChatFailure(bot.ErrorPermanent, fmt.Errorf("bot chat runtime panic: %v", recovered))
		}
		result.ModelSelection = modelSelection
	}()
	if err := req.Budget.Check(ctx); err != nil {
		return botChatFailure(bot.ErrorBudget, err)
	}
	var progress *botProgressRuntime
	ordinaryResume := resume && req.Progress != nil
	if bot.ProgressExecutionEnabled(req) {
		progress = &botProgressRuntime{req: req, state: req.Progress.Clone(), pending: make(map[uuid.UUID]bool)}
		if progress.state != nil {
			progress.cursor = progress.state.Checkpoint.DispatchLedgerCursor
		}
		ctx = bot.WithProgressDispatch(ctx, progress)
		ctx = bot.WithProgressController(ctx, &bot.ProgressController{Boundary: progress.Boundary})
	}
	interaction, _ := ctx.Value(botResumeInteractionKey{}).(*store.BotApproval)
	preparedResume, _ := ctx.Value(botPreparedPlanResumeKey{}).(*store.BotPreparedPlan)
	var capture *botApprovalCapture
	if !resume || interaction != nil || ordinaryResume || preparedResume != nil {
		if s := bot.ApprovalStoreFromContext(ctx); s != nil {
			capture = &botApprovalCapture{store: s, req: req}
			ctx = bot.WithInteractionOwner(ctx)
			runtime := agent.BotPolicyRuntimeFromContext(ctx)
			runtime.Approvals = capture
			ctx = agent.WithBotPolicyRuntime(ctx, runtime)
		}
	}
	if plans, ok := bot.ApprovalStoreFromContext(ctx).(store.BotPreparedPlanStore); ok {
		ctx = context.WithValue(ctx, botPreparedPlanSessionKey{}, &BotPreparedPlanSession{Request: req, Store: plans, Resume: preparedResume, capture: capture})
	}
	var workService *botAgentWorkService
	if req.Work != nil && req.Work.Protocol == store.BotWorkProtocolAgentLedV1 && (!resume || interaction != nil || ordinaryResume) {
		workService = &botAgentWorkService{req: req}
		ctx = bot.WithBotWorkServices(ctx, workService)
	}
	// Setup is bounded independently; stop its watchdog once resources are ready.
	// Resources keep the run context, never a soon-cancelled setup child context.
	setupTimeout := 120 * time.Second
	if timeout, ok := req.Budget.(interface{ ModelCallTimeout() time.Duration }); ok && timeout.ModelCallTimeout() > 0 {
		setupTimeout = timeout.ModelCallTimeout()
	}
	setupTimer := time.AfterFunc(setupTimeout, cancel)
	defer setupTimer.Stop()
	overrides := agent.NewBotPromptOverrides(agent.PromptOverridesFromContext(ctx), req)
	overrides.PlanMode = req.PlanMode
	overrides.GraphPlanMode, overrides.AskMode = false, false
	overrides.ApprovedPlanExecution, overrides.ApprovedPlanExecutionExplicit = false, false
	if interaction != nil && interaction.Interaction != nil && interaction.Interaction.Kind == store.BotInteractionPlanDefinition {
		overrides.ApprovedPlanExecution, overrides.ApprovedPlanExecutionExplicit = true, true
	}
	if req.PlanMode {
		overrides.SessionContext += "\n" + agent.PlanModeSystemContext
	}
	ctx = agent.WithPromptOverrides(ctx, overrides)
	factoryTiming := entstore.ObserveBotExecutionStage(ctx, entstore.BotStageFactory)
	resources, err := a.factory(ctx, req)
	factoryTiming(err)
	if setupTimer != nil {
		setupTimer.Stop()
	}
	if err == nil && resources != nil && resources.ModelSelection != nil {
		selected := *resources.ModelSelection
		modelSelection = &selected
	}
	if resources != nil && resources.Cleanup != nil {
		defer resources.Cleanup()
	}
	if err != nil {
		return botChatFactoryFailure(ctx, err)
	}
	if resources == nil || resources.Agent == nil || resources.Agent.LLM == nil || resources.SessionService == nil || resources.AppName == "" || resources.Cleanup == nil {
		return botChatFailure(bot.ErrorPermanent, store.ErrBotUnsupported)
	}
	if req.Run.IsInteractiveChat() && req.Mandate == nil {
		if resources.Authorization == nil || resources.Authorization != req.Authorization {
			return botChatFailure(bot.ErrorPermission, store.ErrBotForbidden)
		}
		ctx = execution.WithBotTurnAuthorizer(ctx, resources.Authorization)
	}
	if resources.PolicyRuntime != nil {
		ctx = agent.WithBotPolicyRuntime(ctx, *resources.PolicyRuntime)
	}
	// Do not fall back to a new in-memory session or a factory's unrelated store.
	if resources.Agent.SessionService != resources.SessionService {
		return botChatFailure(bot.ErrorPermission, store.ErrBotForbidden)
	}
	if interaction != nil || req.Run.Intent != store.BotIntentRegular || req.Run.Presentation == store.BotRunPresentationHomeInteractive {
		transcript := resources.Transcript
		if transcript == nil && interaction != nil && capture != nil {
			transcript, _ = capture.store.(store.BotTranscriptStore)
		}
		if transcript == nil {
			return botChatFailure(bot.ErrorPermission, store.ErrBotUnsupported)
		}
		conversation := botConversationSessions{Service: resources.SessionService, transcript: transcript, lease: req.Run.Lease, id: req.Run.SessionID, userEventID: req.Run.UserEventID, internalInput: ordinaryResume}
		if interaction != nil {
			conversation.answerEventID = "bot-owner-answer:" + interaction.ID.String()
		}
		if req.Run.UsesHomeHistory() {
			conversation.home = req.Bot.HomeSessionID
		}
		resources.SessionService = conversation
		resources.Agent.SessionService = resources.SessionService
	}
	existing, err := resources.SessionService.Get(ctx, &session.GetRequest{AppName: resources.AppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
	if err != nil {
		return botChatFailure(bot.ErrorPermission, err)
	}
	if existing == nil || existing.Session == nil || existing.Session.ID() != req.Run.SessionID.String() || existing.Session.UserID() != req.Bot.OwnerID.String() || existing.Session.AppName() != resources.AppName {
		return botChatFailure(bot.ErrorPermission, store.ErrBotForbidden)
	}
	if preparedResume != nil {
		startup(nil)
		return executePreparedPlan(ctx, req, resources, capture, existing.Session.State(), preparedResume)
	}
	if progress != nil {
		ctx, err = progress.compose(ctx, resources, existing.Session)
		if err != nil {
			return botChatFailure(bot.ErrorBlocked, err)
		}
	}
	// The factory contract supplies the shared-budget model. Capture/resume
	// decorators below are parent-turn behavior and must not wrap auxiliary calls.
	auxiliary := resources.Agent.LLM
	if workService != nil {
		workService.resources, workService.llm = resources, auxiliary
	}
	ctx = agent.WithBotAuxiliaryLLM(ctx, auxiliary)
	if capture != nil {
		resources.Agent.LLM = &botApprovalCaptureModel{LLM: resources.Agent.LLM, capture: capture}
	}
	if resume && interaction == nil && !ordinaryResume {
		action, ok := ctx.Value(botResumeActionKey{}).(execution.ActionDescriptor)
		if !ok {
			return botChatFailure(bot.ErrorBlocked, store.ErrBotUnsupported)
		}
		guard, _ := agent.BotPolicyRuntimeFromContext(ctx).Guard.(*botResumeGuard)
		resources.Agent.LLM = &botResumeModel{LLM: resources.Agent.LLM, action: action, prepared: preparedResume != nil, guard: guard}
	}
	var sequence int64
	if resources.ResearchFactory != nil {
		ctx = WithBotEmployeeResearchRunner(ctx, func(researchCtx context.Context, directed BotEmployeeResearchRequest) (BotEmployeeResearchResult, error) {
			// Preserve the original assignment; the handoff adds review guidance.
			directed.Request.Instructions = req.Instructions + "\n\n" + directed.Request.Instructions
			fresh, err := resources.ResearchFactory(researchCtx, directed)
			if fresh != nil && fresh.Cleanup != nil {
				defer fresh.Cleanup()
			}
			if err != nil {
				return BotEmployeeResearchResult{}, err
			}
			if fresh == nil || fresh.Agent == nil || fresh.Agent.LLM == nil || fresh.Cleanup == nil || fresh.PolicyRuntime == nil || fresh.SessionService == nil {
				return BotEmployeeResearchResult{}, store.ErrBotUnsupported
			}
			// Keep the adapter's existing conversation wrapper, session and event
			// sequence; never register a new run or capture an approval continuation.
			fresh.SessionService = resources.SessionService
			fresh.Agent.SessionService = fresh.SessionService
			if conversation, ok := fresh.SessionService.(botConversationSessions); ok {
				conversation.internalInput = true
				fresh.SessionService = conversation
				fresh.Agent.SessionService = conversation
			}
			researchCtx = agent.WithBotPolicyRuntime(researchCtx, directed.PolicyRuntime)
			privateSink := func(c context.Context, event bot.RuntimeEvent) error {
				if event.Type == "text" || event.Type == "done" {
					return nil
				}
				return sink(c, event)
			}
			var completion ChatCompletion
			result, err := runBotChatAttempt(researchCtx, directed.Request, fresh, privateSink, nil, existing, nil, false, &sequence, &completion)
			return BotEmployeeResearchResult{Result: result, Completion: completion}, err
		})
	}
	startup(nil)
	produce := func(attemptReq bot.RunRequest, attemptInteraction *store.BotApproval, completion *ChatCompletion) (bot.RunResult, error) {
		return runBotChatAttempt(ctx, attemptReq, resources, sink, capture, existing, attemptInteraction, resume, &sequence, completion)
	}
	if req.Run.TitleConfirmation != nil && req.Run.TitleConfirmation.State == store.BotTitleConfirmationNeedsQuestion {
		if capture == nil {
			return botChatFailure(bot.ErrorBlocked, store.ErrBotUnsupported)
		}
		var completion ChatCompletion
		result, err := produce(req, nil, &completion)
		if err != nil {
			return result, err
		}
		if result.Status != store.RunWaitingApproval || len(result.Checkpoint) == 0 {
			return botChatFailure(bot.ErrorPermanent, errors.New("title clarification generation failed: no persisted question checkpoint"))
		}
		return result, nil
	}
	result, runErr := runBotWork(ctx, req, resources, auxiliary, resume && interaction == nil && !ordinaryResume, func(attemptReq bot.RunRequest, completion *ChatCompletion) (bot.RunResult, error) {
		result, err := produce(attemptReq, interaction, completion)
		if err != nil {
			return result, fmt.Errorf("produce interaction=%t capture=%t work_revision=%d: %w", interaction != nil, capture != nil, func() int64 {
				if attemptReq.Work != nil {
					return attemptReq.Work.Revision
				}
				return 0
			}(), err)
		}
		interaction = nil // an owner answer is consumed only once
		if conversation, ok := resources.SessionService.(botConversationSessions); ok {
			conversation.internalInput = true
			resources.SessionService = conversation
			resources.Agent.SessionService = conversation
		}
		return result, err
	})
	// Auxiliary employee assessment calls also reserve durable dispatches. Their
	// returned iterators have drained now; settle them before the supervisor's
	// final commit, not only at the last producer/tool boundary.
	completionTiming := entstore.ObserveBotExecutionStage(ctx, entstore.BotStageCompletion)
	defer func() { completionTiming(err) }()
	if result.ProgressCheckpoint == nil {
		if controller := bot.ProgressControllerFromContext(ctx); controller != nil && controller.Boundary != nil {
			runErr = errors.Join(runErr, controller.Boundary(ctx))
		}
	}
	return result, runErr
}

func runBotChatAttempt(ctx context.Context, req bot.RunRequest, resources *BotChatResources, sink bot.EventSink, capture *botApprovalCapture, existing *session.GetResponse, interaction *store.BotApproval, resume bool, sequence *int64, completed *ChatCompletion) (result bot.RunResult, err error) {
	if capture != nil {
		ctx = context.WithValue(ctx, botApprovalCaptureKey{}, capture)
	}
	if req.Work != nil && req.Work.Protocol == store.BotWorkProtocolAgentLedV1 {
		journal := &botAgentReadJournal{}
		if progress, ok := bot.ProgressDispatchFromContext(ctx).(*botProgressRuntime); ok {
			journal.restoreProgress(progress.restoredJournal)
			progress.restoredJournal = nil // only the first resumed producer inherits it
		}
		ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
		// Wrap the producer only, outside capture/exact-action resume decorators.
		// The fresh factory agent is request-owned; restore it between attempts so
		// auxiliary models and subsequent work cannot inherit this journal.
		llm := resources.Agent.LLM
		resources.Agent.LLM = &botAgentReadModel{LLM: llm, journal: journal}
		defer func() { resources.Agent.LLM = llm }()
	}
	cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), resources.AppName, false)
	cr.cancel() // discard constructor's background context: Bot lifetime is worker-owned
	cr.ctx, cr.cancel = context.WithCancel(context.WithValue(ctx, appNameContextKey, resources.AppName))
	defer cr.cancel()
	turnID := uuid.New()
	// Hold producer text until a tool boundary proves it is interim narration.
	// The last response is published only by the reviewed, committed report.
	var pendingText []ChatEvent
	guardFinal := req.Work != nil && req.Work.EmployeeReviewPolicy == store.BotEmployeeReviewEvidenceBoundV1
	publish := func(eventCtx context.Context, event ChatEvent) error {
		payload, err := json.Marshal(event.Data)
		if err != nil {
			return err
		}
		*sequence++
		return sink(eventCtx, bot.RuntimeEvent{SessionID: req.Run.SessionID, RunID: req.Run.ID, TurnID: turnID, Sequence: *sequence, Type: event.Type, Payload: payload})
	}
	cr.SetEventSink(func(eventCtx context.Context, event ChatEvent) error {
		if guardFinal {
			if event.Type == "text" {
				pendingText = append(pendingText, event)
				return nil
			}
			if event.Type == "done" {
				return nil
			}
			if event.Type == "tool_call" || event.Type == "subtask_progress" || event.Type == string(store.BotInteractionQuestion) {
				for _, interim := range pendingText {
					if err := publish(eventCtx, interim); err != nil {
						return err
					}
				}
				pendingText = nil
			}
		}
		payload, err := json.Marshal(event.Data)
		if err != nil {
			return err
		}
		if event.Type == string(store.BotInteractionQuestion) && capture != nil {
			if err := capture.captureQuestion(eventCtx, payload); err != nil {
				return err
			}
		}
		*sequence++
		return sink(eventCtx, bot.RuntimeEvent{SessionID: req.Run.SessionID, RunID: req.Run.ID, TurnID: turnID, Sequence: *sequence, Type: event.Type, Payload: payload})
	})
	cr.ctx = agent.WithPromptOverrides(cr.ctx, agent.NewBotPromptOverrides(agent.PromptOverridesFromContext(cr.ctx), req))
	message := genai.NewContentFromText(req.Instructions, "user")
	if !resume && len(req.Attachments) > 0 {
		message = botAttachmentContent(req.Instructions, req.Attachments)
		cr.InjectChatFiles(req.Attachments)
	}
	if interaction != nil {
		message, err = botInteractionAnswerMessage(existing.Session.Events(), interaction)
		if err != nil {
			if transcript, ok := capture.store.(store.BotTranscriptReader); ok {
				if rows, readErr := transcript.ReadBotTranscript(ctx, req.Run.SessionID, 0, 200); readErr == nil {
					events := make([]*session.Event, 0, len(rows))
					for _, row := range rows {
						if row.Event != nil {
							events = append(events, row.Event)
						}
					}
					message, err = store.BotInteractionAnswerMessage(events, interaction)
				}
			}
			if err != nil && req.Run.TitleConfirmation != nil && req.Run.TitleConfirmation.State == store.BotTitleConfirmationConfirmed && req.Run.TitleConfirmation.ApprovalID == interaction.ID {
				// Initial title confirmation may be resumed after Home provisioning
				// moved the source transcript to retained storage. Its confirmed
				// assignment and approval binding are already repository-validated;
				// construct the same typed owner event without weakening later-question
				// call/response correlation.
				var q question.Question
				if json.Unmarshal(interaction.Interaction.Payload, &q) == nil {
					label := interaction.Response.Answer
					for _, option := range q.Options {
						if option.ID == label && option.Label != "" {
							label = option.Label
						}
					}
					raw, marshalErr := json.Marshal(map[string]any{"type": "bot_owner_answer", "approval_id": interaction.ID, "tool": "ask_user", "response": interaction.Response, "display": map[string]string{"prompt": q.Prompt, "answer": label}})
					if marshalErr == nil {
						message, err = genai.NewContentFromText(string(raw), "user"), nil
					}
				}
			}
			if err != nil {
				return botChatFailure(bot.ErrorBlocked, fmt.Errorf("build owner answer message: %w", err))
			}
		}
		if err := req.Budget.Check(ctx); err != nil {
			return botChatFailure(bot.ErrorBudget, err)
		}
		// Consumption is fenced and durable before any resumed model/tool call.
		// If persistence or execution fails afterwards, never replay this answer.
		if capture == nil {
			return botChatFailure(bot.ErrorBlocked, store.ErrBotUnsupported)
		}
		if err := capture.store.ConsumeApproval(ctx, store.BotApprovalConsumption{Lease: req.Run.Lease, ApprovalID: interaction.ID, Version: req.Run.Version, ActionDigest: interaction.ActionDigest}); err != nil {
			return botChatFailure(bot.ErrorBlocked, fmt.Errorf("consume owner answer: %w", err))
		}
	}
	// No title model call and no interactive auto-approval in Bot mode.
	cr.Run(resources.Agent, resources.SessionService, resources.Agent.LLM, nil, message, req.Instructions, false, "", nil, false)
	completion := cr.Completion()
	if completion.Err != nil {
		completion.Err = fmt.Errorf("chat runner: %w", completion.Err)
	}
	if journal, ok := ctx.Value(botAgentReadJournalKey{}).(*botAgentReadJournal); ok {
		completion.Report.EffectState = journal.effectState()
	}
	if controller := bot.ProgressControllerFromContext(ctx); controller != nil && controller.Boundary != nil {
		completion.Err = errors.Join(completion.Err, controller.Boundary(ctx))
	}
	// Continuations must assess the actual saved source responses and the owner
	// answer, not only this invocation's final prose. These are untrusted data.
	if resume && req.Work != nil && req.Work.Protocol != store.BotWorkProtocolAgentLedV1 {
		if completion.reportBodies == nil {
			completion.reportBodies = make(map[string]string)
		}
		for i := 0; i < existing.Session.Events().Len(); i++ {
			e := existing.Session.Events().At(i)
			if e == nil || e.Partial || e.Content == nil {
				continue
			}
			for _, part := range e.Content.Parts {
				if part == nil || part.FunctionResponse == nil {
					continue
				}
				raw, marshalErr := json.Marshal(part.FunctionResponse.Response)
				if marshalErr != nil {
					completion.Report.Unverified = append(completion.Report.Unverified, "Saved source response unavailable for assessment.")
					continue
				}
				completion.reportBodies["tool-response:"+part.FunctionResponse.ID] = string(raw)
			}
		}
		if interaction != nil {
			raw, marshalErr := json.Marshal(interaction.Response)
			if marshalErr != nil {
				completion.Report.Unverified = append(completion.Report.Unverified, "Owner answer unavailable for assessment.")
			} else {
				completion.reportBodies["tool-response:owner-answer:"+interaction.ID.String()] = string(raw)
			}
		}
	}
	// A suspended checkpoint has already atomically released the durable lease.
	// Never turn this nonterminal handoff into a failed/final report.
	if progress, ok := bot.ProgressDispatchFromContext(ctx).(*botProgressRuntime); ok {
		progress.mu.Lock()
		state := progress.state.Clone()
		progress.mu.Unlock()
		if state != nil && state.Phase == store.BotProgressSuspended {
			return bot.RunResult{Status: store.RunQueued, ProgressCheckpoint: state, Usage: state.Checkpoint.Usage}, nil
		}
	}
	if resources.PublishedFiles != nil {
		completion.Report.Artifacts = append(completion.Report.Artifacts, resources.PublishedFiles()...)
	}
	*completed = completion
	// Preserve observed accounting on every exit, including blocked/failed runs.
	// The shared budget remains authoritative for children and auxiliary calls.
	observed := botCompletionReport(completion, req)
	defer func() {
		result.Usage = completion.Usage
		if result.Status != store.RunSucceeded && result.Status != store.RunPartial {
			result.Report.Changed = observed.Changed
			result.Report.Unchanged = observed.Unchanged
			result.Report.Artifacts = observed.Artifacts
			result.Report.NextRunAt = observed.NextRunAt
			result.Report.Unverified = append(result.Report.Unverified, observed.Unverified...)
		}
	}()
	if capture != nil && completion.Blocked && completion.Err == nil {
		if waiting, ok := capture.checkpoint(); ok {
			observed = botQuestionWaitingEvidence(observed, capture)
			return waiting, nil
		}
	}
	if capture != nil && completion.Err != nil {
		var policyErr *agent.BotPolicyError
		if errors.As(completion.Err, &policyErr) && policyErr.ApprovalRequired() {
			if waiting, ok := capture.checkpoint(); ok {
				return waiting, nil
			}
		}
	}
	if completion.Blocked {
		return botChatFailure(bot.ErrorBlocked, errors.New("bot run blocked; approval or intervention required"))
	}
	retainReadOnlyFinal := botCanRetainReadOnlyFinal(completion, req, observed)
	if completion.Err != nil && !retainReadOnlyFinal {
		class := bot.ErrorPermanent
		var policyErr *agent.BotPolicyError
		if errors.As(completion.Err, &policyErr) {
			class = bot.ErrorBlocked
		} else if errors.Is(completion.Err, context.Canceled) || errors.Is(completion.Err, context.DeadlineExceeded) {
			class = bot.ErrorInterrupted
		}
		return botChatFailure(class, completion.Err)
	}
	if err := ctx.Err(); err != nil {
		return botChatFailure(bot.ErrorInterrupted, err)
	}
	if err := req.Budget.Check(ctx); err != nil {
		return botChatFailure(bot.ErrorBudget, err)
	}
	if !completion.Final {
		return botChatFailure(bot.ErrorPermanent, errors.New("chat ended without a final model answer"))
	}
	// Only a validated narrowed research policy plus live read classifier can
	// attest successful no-tool text. Ordinary empty journals remain unknown.
	policy, _ := ctx.Value(botEmployeeResearchPolicyKey{}).(*execution.RunPolicy)
	runtime := agent.BotPolicyRuntimeFromContext(ctx)
	_, readOnly := runtime.Classifier.(botEmployeeReadClassifier)
	if policy != nil && policy == req.Policy && policy == execution.RunPolicyFromContext(ctx) && runtime.Guard != nil && runtime.Approvals == nil && readOnly && req.Mandate != nil && !resume && capture == nil && interaction == nil {
		journal, _ := ctx.Value(botAgentReadJournalKey{}).(*botAgentReadJournal)
		journal.completeConstrainedText(completion)
		if journal.effectState() == store.BotReportEffectsReadOnly {
			completion.Report.EffectState = store.BotReportEffectsReadOnly
			observed.EffectState = store.BotReportEffectsReadOnly
			*completed = completion
		}
	}
	result = bot.RunResult{Status: store.RunSucceeded, Report: observed}
	if retainReadOnlyFinal || len(result.Report.Unverified) > 0 {
		// Read feedback does not erase completed employee communication, but is
		// not proof the full assignment succeeded. Preserve the exact final as
		// partial through the normal candidate and publication boundaries.
		result.Status = store.RunPartial
	}
	return result, nil
}

// Resume accepts exact-action and typed owner-interaction checkpoints. Neither
// resubmits the original task nor accepts a consumed decision for replay.
func (a *BotChatRuntimeAdapter) Resume(ctx context.Context, req bot.RunRequest, sink bot.EventSink) (bot.RunResult, error) {
	validation := req
	validation.Run.Checkpoint = nil
	if err := validateBotChatRequest(ctx, validation, sink); err != nil {
		return botChatFailure(bot.ErrorPermission, err)
	}
	return a.resume(ctx, req, sink)
}

// Cancel stops only the exact locally active fencing token, never a newer run.
// Durable revocation/fencing remains the supervisor/guard's responsibility.
func (a *BotChatRuntimeAdapter) Cancel(ctx context.Context, lease store.BotLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil {
		return store.ErrBotForbidden
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	active, exists := a.active[lease.RunID]
	if !exists {
		return nil
	}
	if !reflect.DeepEqual(p, active.principal) || lease.BotID != active.lease.BotID || lease.Fence != active.lease.Fence || lease.WorkerID != active.lease.WorkerID {
		return store.ErrBotForbidden
	}
	active.cancel()
	return nil
}

// Uses Chat's actual ADK content, not report summaries or a second transcript format.
type botConversationSessions struct {
	session.Service
	transcript  store.BotTranscriptStore
	lease       store.BotLease
	id          uuid.UUID
	home        uuid.UUID
	userEventID string
	// The admission ID remains the history boundary; an owner answer is a
	// distinct durable input, never a replacement for the admitted task.
	internalInput bool
	answerEventID string
}

// Read the existing home transcript into the normal ADK session view. The run
// retains its own identity/state and writes; no history is copied or re-persisted.
func (s botConversationSessions) Get(ctx context.Context, q *session.GetRequest) (*session.GetResponse, error) {
	result, err := s.Service.Get(ctx, q)
	if err == nil && result != nil && result.Session != nil && s.answerEventID != "" {
		// ADK appends the submitted input before composing its model request.
		// Hide only this already-durable input in the producer view, so ADK
		// reuses it once instead of seeing both restored and submitted copies.
		result.Session = botAnswerInputSession{Session: result.Session, answerID: s.answerEventID}
	}
	if err != nil || s.home == uuid.Nil || q.SessionID != s.id.String() {
		if err == nil && s.answerEventID != "" && result != nil && result.Session != nil {
			result.Session = &botConversationSession{Session: result.Session}
		}
		return result, err
	}
	homeRequest := *q
	homeRequest.SessionID = s.home.String()
	home, err := s.Service.Get(ctx, &homeRequest)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Session == nil || home == nil || home.Session == nil {
		return nil, store.ErrBotForbidden
	}
	// The home contains mirrored current-run events too. Exclude their IDs so
	// each event reaches the model once, with prior history preceding this run.
	current := map[string]bool{}
	if s.answerEventID != "" {
		current[s.answerEventID] = true
	}
	for e := range result.Session.Events().All() {
		current[e.ID] = true
	}
	var prior []*session.Event
	admissionFound := false
	for e := range home.Session.Events().All() {
		// The admission event is the durable high-water boundary. Never load
		// this turn or future queued turns as prior model context.
		if s.userEventID != "" && e.ID == s.userEventID {
			admissionFound = true
			break
		}
		if !current[e.ID] {
			prior = append(prior, e)
		}
	}
	// Only a proven admission boundary permits historical payload reduction.
	// The active run, including resumed checkpoints, remains verbatim.
	if admissionFound {
		prior = projectBotHomeHistory(prior)
	}
	result.Session = &botConversationSession{Session: result.Session, prior: prior}
	return result, nil
}

type botConversationSession struct {
	session.Session
	prior    []*session.Event
	appended []*session.Event
	eventIDs map[string]struct{} // built once for idempotent canonical transcript appends
}

func (s botConversationSession) Events() session.Events {
	var current session.Events
	if s.Session != nil {
		current = s.Session.Events()
	}
	// Compose indexed views rather than copying every old event on each call.
	// Progress can now advance its cursor without rescanning irrelevant history.
	return botConversationEvents{prior: s.prior, current: botConversationAppendEvents{current: current, appended: s.appended}}
}

type botConversationAppendEvents struct {
	current  session.Events
	appended []*session.Event
}

func (e botConversationAppendEvents) Len() int {
	if e.current == nil {
		return len(e.appended)
	}
	return e.current.Len() + len(e.appended)
}
func (e botConversationAppendEvents) At(i int) *session.Event {
	if i < 0 || i >= e.Len() {
		return nil
	}
	if e.current != nil {
		if i < e.current.Len() {
			return e.current.At(i)
		}
		i -= e.current.Len()
	}
	return e.appended[i]
}
func (e botConversationAppendEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for i := 0; i < e.Len(); i++ {
			if !yield(e.At(i)) {
				return
			}
		}
	}
}

type botConversationEvents struct {
	prior   []*session.Event
	current session.Events
}

func (e botConversationEvents) Len() int {
	if e.current == nil {
		return len(e.prior)
	}
	return len(e.prior) + e.current.Len()
}
func (e botConversationEvents) At(i int) *session.Event {
	if i < 0 || i >= e.Len() {
		return nil
	}
	if i < len(e.prior) {
		return e.prior[i]
	}
	return e.current.At(i - len(e.prior))
}
func (e botConversationEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, event := range e.prior {
			if !yield(event) {
				return
			}
		}
		if e.current == nil {
			return
		}
		for event := range e.current.All() {
			if !yield(event) {
				return
			}
		}
	}
}

func (s *botConversationSession) appendCanonical(e *session.Event) {
	if s.eventIDs == nil {
		s.eventIDs = make(map[string]struct{})
		for existing := range s.Events().All() {
			if existing != nil {
				s.eventIDs[existing.ID] = struct{}{}
			}
		}
	}
	if _, exists := s.eventIDs[e.ID]; exists {
		return
	}
	s.eventIDs[e.ID] = struct{}{}
	s.appended = append(s.appended, e)
}

func (s botConversationSessions) AppendEvent(ctx context.Context, sess session.Session, e *session.Event) error {
	// Synthetic Chat failures are trusted source-run diagnostics, not Home
	// narration. Home receives the committed employee report exactly once.
	if isSyntheticRunError(e) {
		if wrapped, ok := sess.(*botConversationSession); ok {
			sess = wrapped.Session
		}
		if answer, ok := sess.(botAnswerInputSession); ok {
			sess = answer.Session
		}
		return s.Service.AppendEvent(ctx, sess, e)
	}
	if wrapped, ok := sess.(*botConversationSession); ok {
		if (s.userEventID != "" || s.answerEventID != "") && !e.Partial && e.Content != nil {
			copy := *e
			if e.Author == "user" && !s.internalInput && e.Actions.StateDelta["_astonish_turn_context"] != true {
				copy.ID = s.userEventID
				if s.answerEventID != "" {
					copy.ID = s.answerEventID
				}
			}
			if err := s.transcript.AppendBotRunEvent(ctx, s.lease, s.id, &copy); err != nil {
				return err
			}
			// Orientation may already have pinned this canonical owner event in
			// the run. The transcript write is idempotent; mirror that property
			// in the in-memory view so the producer sees the admission once.
			wrapped.appendCanonical(&copy)
			return nil
		}
		sess = wrapped.Session
	}
	// Read-only producer projections must never reach a concrete session store;
	// state-only events still update the original native session.
	if answer, ok := sess.(botAnswerInputSession); ok {
		sess = answer.Session
	}
	if e.Content != nil && !e.Partial {
		if err := s.transcript.AppendBotRunEvent(ctx, s.lease, s.id, e); err != nil {
			return err
		}
	}
	return s.Service.AppendEvent(ctx, sess, e)
}
