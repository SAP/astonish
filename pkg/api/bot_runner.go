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
	"github.com/SAP/astonish/pkg/store"
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
	Authorization  execution.BotTurnAuthorizer
	Transcript     store.BotTranscriptStore
	Agent          *agent.ChatAgent
	SessionService session.Service
	AppName        string
	Cleanup        func()
	// PolicyRuntime is prepared by the trusted factory and must be the same
	// runtime installed on its bound tools. Nil preserves explicit context wiring.
	PolicyRuntime *agent.BotPolicyRuntime
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
	failure := &bot.RunError{Class: class, Message: cause.Error(), Cause: cause, TerminalForAttempt: true}
	status := store.RunFailed
	if class == bot.ErrorInterrupted {
		status = store.RunInterrupted
	}
	return bot.RunResult{Status: status, Failure: failure, Report: store.BotReport{Summary: "Run did not complete; execution requires review."}}, failure
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
	ctx, _ = bot.WithToolOutcomes(ctx)
	ctx = store.WithSessionID(ctx, req.Run.SessionID.String())
	ctx = store.WithUserID(ctx, req.Bot.OwnerID.String())
	ctx = store.WithOrgSlug(ctx, req.Principal.OrgSlug)
	ctx = store.WithTeamSlug(ctx, req.Principal.TeamSlug)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.Version.Budget.MaxRuntimeSeconds)*time.Second)
	defer cancel()
	a.mu.Lock()
	if _, exists := a.active[req.Run.ID]; exists {
		a.mu.Unlock()
		return botChatFailure(bot.ErrorBlocked, store.ErrBotConflict)
	}
	a.active[req.Run.ID] = botChatActiveRun{lease: req.Run.Lease, principal: req.Principal, cancel: cancel}
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.active, req.Run.ID); a.mu.Unlock() }()
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = botChatFailure(bot.ErrorPermanent, fmt.Errorf("bot chat runtime panic: %v", recovered))
		}
	}()
	if err := req.Budget.Check(ctx); err != nil {
		return botChatFailure(bot.ErrorBudget, err)
	}
	interaction, _ := ctx.Value(botResumeInteractionKey{}).(*store.BotApproval)
	var capture *botApprovalCapture
	if !resume || interaction != nil {
		if s := bot.ApprovalStoreFromContext(ctx); s != nil {
			capture = &botApprovalCapture{store: s, req: req}
			ctx = bot.WithInteractionOwner(ctx)
			runtime := agent.BotPolicyRuntimeFromContext(ctx)
			runtime.Approvals = capture
			ctx = agent.WithBotPolicyRuntime(ctx, runtime)
		}
	}
	resources, err := a.factory(ctx, req)
	if resources != nil && resources.Cleanup != nil {
		defer resources.Cleanup()
	}
	if err != nil {
		return botChatFailure(bot.ErrorPermanent, err)
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
	if req.Run.Intent != store.BotIntentRegular || req.Run.Presentation == store.BotRunPresentationHomeInteractive {
		transcript := resources.Transcript
		if transcript == nil {
			return botChatFailure(bot.ErrorPermission, store.ErrBotUnsupported)
		}
		conversation := botConversationSessions{Service: resources.SessionService, transcript: transcript, lease: req.Run.Lease, id: req.Run.SessionID, userEventID: req.Run.UserEventID}
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
	// The factory contract supplies the shared-budget model. Capture/resume
	// decorators below are parent-turn behavior and must not wrap auxiliary calls.
	ctx = agent.WithBotAuxiliaryLLM(ctx, resources.Agent.LLM)
	if capture != nil {
		resources.Agent.LLM = &botApprovalCaptureModel{LLM: resources.Agent.LLM, capture: capture}
	}
	if resume && interaction == nil {
		action, ok := ctx.Value(botResumeActionKey{}).(execution.ActionDescriptor)
		if !ok {
			return botChatFailure(bot.ErrorBlocked, store.ErrBotUnsupported)
		}
		resources.Agent.LLM = &botResumeModel{LLM: resources.Agent.LLM, action: action}
	}
	cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), resources.AppName, false)
	cr.cancel() // discard constructor's background context: Bot lifetime is worker-owned
	cr.ctx, cr.cancel = context.WithCancel(context.WithValue(ctx, appNameContextKey, resources.AppName))
	defer cr.cancel()
	turnID := uuid.New()
	var sequence int64
	cr.SetEventSink(func(eventCtx context.Context, event ChatEvent) error {
		payload, err := json.Marshal(event.Data)
		if err != nil {
			return err
		}
		if event.Type == string(store.BotInteractionQuestion) && capture != nil {
			if err := capture.captureQuestion(eventCtx, payload); err != nil {
				return err
			}
		}
		sequence++
		return sink(eventCtx, bot.RuntimeEvent{SessionID: req.Run.SessionID, RunID: req.Run.ID, TurnID: turnID, Sequence: sequence, Type: event.Type, Payload: payload})
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
			return botChatFailure(bot.ErrorBlocked, err)
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
			return botChatFailure(bot.ErrorBlocked, err)
		}
	}
	// No title model call and no interactive auto-approval in Bot mode.
	cr.Run(resources.Agent, resources.SessionService, resources.Agent.LLM, nil, message, req.Instructions, false, "", nil, false)
	completion := cr.Completion()
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
	if completion.Err != nil {
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
	result = bot.RunResult{Status: store.RunSucceeded, Report: observed}
	if len(result.Report.Unverified) > 0 {
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
	answerEventID string
}

// Read the existing home transcript into the normal ADK session view. The run
// retains its own identity/state and writes; no history is copied or re-persisted.
func (s botConversationSessions) Get(ctx context.Context, q *session.GetRequest) (*session.GetResponse, error) {
	result, err := s.Service.Get(ctx, q)
	if err != nil || s.home == uuid.Nil || q.SessionID != s.id.String() {
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
}

func (s botConversationSession) Events() session.Events {
	prior := append([]*session.Event(nil), s.prior...)
	for e := range s.Session.Events().All() {
		prior = append(prior, e)
	}
	prior = append(prior, s.appended...)
	return botConversationEvents{prior: prior}
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

func (s botConversationSessions) AppendEvent(ctx context.Context, sess session.Session, e *session.Event) error {
	if wrapped, ok := sess.(*botConversationSession); ok {
		if s.userEventID != "" && !e.Partial && e.Content != nil {
			copy := *e
			if e.Author == "user" && e.Actions.StateDelta["_astonish_turn_context"] != true {
				copy.ID = s.userEventID
				if s.answerEventID != "" {
					copy.ID = s.answerEventID
				}
			}
			if err := s.transcript.AppendBotRunEvent(ctx, s.lease, s.id, &copy); err != nil {
				return err
			}
			wrapped.appended = append(wrapped.appended, &copy)
			return nil
		}
		sess = wrapped.Session
	}
	if e.Content != nil && !e.Partial {
		if err := s.transcript.AppendBotRunEvent(ctx, s.lease, s.id, e); err != nil {
			return err
		}
	}
	return s.Service.AppendEvent(ctx, sess, e)
}
