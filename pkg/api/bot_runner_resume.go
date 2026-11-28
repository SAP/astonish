package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Capture only the first blocked invocation. Multiple concurrent approval calls
// cannot be represented safely by this constrained continuation.
type botApprovalCapture struct {
	mu       sync.Mutex
	store    store.BotStore
	req      bot.RunRequest
	approval *store.BotApproval
	conflict bool
	// Only RequestApproval, reached before execution, can attest nonexecution.
	invocation      *genai.FunctionCall
	nonexecutedID   string
	nonexecutedTool string
	seenCallIDs     map[string]bool
}

type botApprovalCaptureKey struct{}

func (c *botApprovalCapture) takeNonexecution(id, name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id == "" || id != c.nonexecutedID || name != c.nonexecutedTool || c.conflict {
		return false
	}
	c.nonexecutedID = "" // one response, never a reusable exemption
	return true
}

func (c *botApprovalCapture) RequestApproval(ctx context.Context, id execution.BotRunIdentity, a execution.ActionDescriptor, digest string) (uuid.UUID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	want := execution.BotRunIdentity{BotID: c.req.Bot.ID, RunID: c.req.Run.ID, Version: c.req.Run.Version, ActorID: c.req.Principal.Subject, OrgSlug: c.req.Principal.OrgSlug, TeamSlug: c.req.Principal.TeamSlug}
	if (c.req.Run.TitleConfirmation != nil && c.req.Run.TitleConfirmation.State != store.BotTitleConfirmationConfirmed) || id != want {
		return uuid.Nil, store.ErrBotForbidden
	}
	if c.req.Policy != nil {
		if id != c.req.Policy.Identity() {
			return uuid.Nil, store.ErrBotForbidden
		}
	} else if !c.req.Run.IsInteractiveChat() || c.req.Mandate != nil || c.req.Authorization == nil {
		return uuid.Nil, store.ErrBotForbidden
	} else if err := c.req.Authorization.AuthorizeTool(ctx, a.Tool); err != nil {
		return uuid.Nil, err
	}
	if c.approval != nil {
		c.conflict = true
		return uuid.Nil, store.ErrBotConflict
	}
	// Pending in-memory secret tokens cannot be reconstructed after a restart.
	if stringsContainPendingSecret(a.Arguments) {
		return uuid.Nil, store.ErrBotUnsupported
	}
	saved, err := c.store.RequestApproval(ctx, c.req.Run.Lease, store.BotApproval{BotID: id.BotID, RunID: id.RunID, Version: id.Version, Action: a, ActionDigest: digest, ExpiresAt: time.Now().Add(24 * time.Hour)})
	if err != nil {
		return uuid.Nil, err
	}
	c.approval = saved
	// The policy guard returns immediately after this request, before the tool
	// body or budget reservation. Match its exact action to the serial model
	// invocation; model text and serialized response maps are not evidence.
	if call := c.invocation; call != nil && call.ID != "" {
		name, arguments := call.Name, call.Args
		var normalizeErr error
		if name == common.ExecuteToolName {
			name, arguments, normalizeErr = common.NormalizeExecuteToolArgs(call.Args)
		}
		args, marshalErr := json.Marshal(arguments)
		candidate := a
		candidate.Tool, candidate.Arguments = name, args
		want, wantErr := a.Digest()
		got, gotErr := candidate.Digest()
		callMatches := true
		if toolCtx, ok := ctx.(interface{ FunctionCallID() string }); ok {
			callMatches = toolCtx.FunctionCallID() == call.ID
		}
		if normalizeErr == nil && marshalErr == nil && wantErr == nil && gotErr == nil && want == digest && got == digest && callMatches {
			c.nonexecutedID, c.nonexecutedTool = call.ID, call.Name
		}
	}
	return saved.ID, nil
}
func (c *botApprovalCapture) checkpoint() (bot.RunResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.approval == nil || c.conflict {
		return bot.RunResult{}, false
	}
	a := c.approval
	format := bot.ApprovalCheckpointFormat
	summary := "Exact action awaits approval; continuation is limited to that action and a final report."
	if a.Interaction != nil {
		format = bot.InteractionCheckpointFormat
		summary = "Question awaits the explicit Bot owner's typed answer; no execution resources are held. Continuation uses the saved conversation under current tool policy."
		if a.Interaction.Kind == store.BotInteractionPlanDefinition {
			summary = "The persisted plan awaits exact owner execution consent in this original run and session. Consent does not grant tool permissions; no execution resources are held."
		}
	}
	checkpoint, _ := json.Marshal(bot.ApprovalCheckpoint{Format: format, ApprovalID: a.ID, BotID: a.BotID, RunID: a.RunID, SessionID: c.req.Run.SessionID, Version: a.Version, ActionDigest: a.ActionDigest, Fence: c.req.Run.Lease.Fence})
	return bot.RunResult{Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: summary, NeedsApproval: []uuid.UUID{a.ID}}}, true
}
func stringsContainPendingSecret(raw []byte) bool { return bytes.Contains(raw, []byte("SECRET_")) }

// Capture validates the complete provider response before any tool can execute.
// Checkpointed requests disable parallel generation where the provider supports
// it; a violating provider still fails closed at this boundary.
type botApprovalCaptureModel struct {
	model.LLM
	capture *botApprovalCapture
}

func (m *botApprovalCaptureModel) SupportsModelPreflight() bool {
	next, ok := m.LLM.(interface{ SupportsModelPreflight() bool })
	return ok && next.SupportsModelPreflight()
}

func (m *botApprovalCaptureModel) GenerateContent(ctx context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		end := sync.OnceFunc(func() { execution.EndModelPreflight(ctx) })
		defer end()
		originalYield := yield
		yield = func(r *model.LLMResponse, err error) bool { end(); return originalYield(r, err) }
		m.capture.mu.Lock()
		waiting := m.capture.approval != nil
		m.capture.mu.Unlock()
		if waiting {
			yield(nil, &agent.BotPolicyError{Decision: execution.PolicyDecision{Kind: execution.PolicyRequestApproval, Reason: "durable action checkpoint"}})
			return
		}
		// This limited checkpoint format supports serial tool invocations only.
		// Constrain generation as well as rejecting a batch before ANY call executes.
		// Clone request config: pooled models and ordinary Chat retain their defaults.
		serialReq := common.WithParallelToolCalls(r, false)
		var responses []*model.LLMResponse
		calls, size := 0, 0
		if !m.SupportsModelPreflight() {
			end() // Unknown decorators may create a provider iterator immediately.
		}
		for response, err := range m.LLM.GenerateContent(ctx, serialReq, false) {
			end()
			if err != nil {
				yield(nil, err)
				return
			}
			if response != nil && response.Content != nil {
				for _, part := range response.Content.Parts {
					if part.FunctionCall != nil {
						calls++
					}
				}
			}
			data, err := json.Marshal(response)
			if err != nil {
				yield(nil, err)
				return
			}
			size += len(data)
			// Keep the fail-closed serial checkpoint boundary, but identify which
			// response-shape constraint failed. Never include model arguments or
			// response text: those can contain credentials and untrusted content.
			if calls > 1 {
				yield(nil, &bot.CheckpointResponseError{Reason: bot.CheckpointResponseMultipleCalls, ToolCalls: calls})
				return
			}
			if size > 4<<20 {
				yield(nil, &bot.CheckpointResponseError{Reason: bot.CheckpointResponseTooLarge, ResponseBytes: size, LimitBytes: 4 << 20})
				return
			}
			responses = append(responses, response)
		}
		m.capture.mu.Lock()
		m.capture.invocation = nil
		for _, response := range responses {
			if response == nil || response.Content == nil {
				continue
			}
			for _, part := range response.Content.Parts {
				if part == nil || part.FunctionCall == nil {
					continue
				}
				call := part.FunctionCall
				if call.ID == "" {
					call.ID = uuid.NewString()
				}
				if m.capture.seenCallIDs == nil {
					m.capture.seenCallIDs = make(map[string]bool)
				}
				if !m.capture.seenCallIDs[call.ID] {
					// Freeze arguments before yielding control to callbacks.
					raw, _ := json.Marshal(call)
					var snapshot genai.FunctionCall
					decoder := json.NewDecoder(bytes.NewReader(raw))
					decoder.UseNumber()
					if decoder.Decode(&snapshot) == nil {
						m.capture.invocation = &snapshot
					}
				}
				m.capture.seenCallIDs[call.ID] = true
			}
		}
		m.capture.mu.Unlock()
		for _, response := range responses {
			if !yield(response, nil) {
				return
			}
		}
	}
}

type botResumeModel struct {
	model.LLM
	action   execution.ActionDescriptor
	prepared bool
	guard    *botResumeGuard
	first    bool
}

func (m *botResumeModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if m.first {
		if m.guard != nil && m.guard.unconsumed() {
			return func(yield func(*model.LLMResponse, error) bool) {
				yield(nil, store.ErrBotForbidden)
			}
		}
		if m.prepared {
			return func(yield func(*model.LLMResponse, error) bool) {
				yield(&model.LLMResponse{Content: genai.NewContentFromText("Prepared plan continuation returned; durable plan state records the outcome.", "model")}, nil)
			}
		}
		return m.LLM.GenerateContent(ctx, r, stream)
	}
	m.first = true
	return func(yield func(*model.LLMResponse, error) bool) {
		var args map[string]any
		decoder := json.NewDecoder(bytes.NewReader(m.action.Arguments))
		decoder.UseNumber() // never round exact approved integers through float64
		if err := decoder.Decode(&args); err != nil {
			yield(nil, err)
			return
		}
		call := &genai.FunctionCall{ID: uuid.NewString(), Name: m.action.Tool, Args: args}
		// Deferred tools use Chat's ordinary dispatch bridge. The inner descriptor
		// remains exact and is checked by botResumeGuard before execution.
		if (strings.Contains(call.Name, "/") && (r == nil || r.Tools[call.Name] == nil)) || (r != nil && r.Tools[call.Name] == nil && r.Tools[common.ExecuteToolName] != nil) {
			call = &genai.FunctionCall{ID: call.ID, Name: "execute_tool", Args: map[string]any{"name": m.action.Tool, "arguments": args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
	}
}

type botResumeGuard struct {
	mu                 sync.Mutex
	live               execution.PolicyGuard
	store              store.BotStore
	req                bot.RunRequest
	approval           store.BotApproval
	attempted          bool
	consumptionStarted bool
	drillCursor        int
}

func (g *botResumeGuard) unconsumed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.attempted && !g.consumptionStarted
}

func (g *botResumeGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	digest, err := a.Digest()
	if err != nil {
		return execution.PolicyDecision{}, err
	}
	if occurrence, ok := ctx.Value(capturedDrillOccurrenceKey{}).(*capturedDrillOccurrence); ok {
		want, e := occurrence.action.Digest()
		if e != nil || occurrence.guard != g || !g.attempted || want != digest || !occurrence.used.CompareAndSwap(false, true) || g.live == nil {
			return execution.PolicyDecision{}, store.ErrBotForbidden
		}
		var decision execution.PolicyDecision
		if occurrence.turn != nil {
			if g.req.Policy != nil || !g.req.Run.IsInteractiveChat() || g.req.Mandate != nil || g.req.Authorization == nil || execution.RunPolicyFromContext(ctx) != nil || a.Effect != execution.EffectRead || !a.ConstraintsVerified || a.SpendMinor != 0 {
				return decision, store.ErrBotForbidden
			}
			if err := occurrence.turn.AuthorizeTool(ctx, a.Tool); err != nil {
				return decision, err
			}
			ctx = execution.WithBotTurnAuthorizer(ctx, occurrence.turn)
		} else {
			// Restore only this occurrence's narrowed policy; never parent grants.
			if occurrence.policy == nil {
				return decision, store.ErrBotForbidden
			}
			ctx = execution.WithRunPolicy(ctx, occurrence.policy)
			decision = execution.EvaluateRunPolicy(ctx, a, time.Now())
			if decision.Kind != execution.PolicyAllow {
				return decision, store.ErrBotForbidden
			}
		}
		decision, err = g.live.Authorize(ctx, a)
		if err == nil && (decision.Kind != execution.PolicyAllow || decision.ActionDigest != digest) {
			err = store.ErrBotForbidden
		}
		return decision, err
	}
	if g.consumptionStarted || digest != g.approval.ActionDigest {
		return execution.PolicyDecision{}, store.ErrBotForbidden
	}
	if g.live == nil {
		return execution.PolicyDecision{}, store.ErrBotUnsupported
	}
	// The underlying current guard must allow only after current permissions,
	// sandbox readiness and budgets. It must not consume this adapter's grant.
	d, err := g.live.Authorize(ctx, a)
	if err != nil {
		return d, err
	}
	if d.Kind != execution.PolicyAllow || d.ActionDigest != digest {
		return d, store.ErrBotForbidden
	}
	// A failed consumption write may have committed; never call it again or
	// preserve such an attempt as an unused approval.
	g.consumptionStarted = true
	if err := g.store.ConsumeApproval(ctx, store.BotApprovalConsumption{Lease: g.req.Run.Lease, ApprovalID: g.approval.ID, Version: g.req.Run.Version, ActionDigest: digest}); err != nil {
		return execution.PolicyDecision{}, err
	}
	g.attempted = true
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest, GrantID: g.approval.ID}, nil
}

func (a *BotChatRuntimeAdapter) resume(ctx context.Context, req bot.RunRequest, sink bot.EventSink) (bot.RunResult, error) {
	if botHasProgressCheckpoint(req) {
		if err := botProgressActivationGuard(req); err != nil {
			return botChatFailure(bot.ErrorBlocked, err)
		}
		// Only the repository snapshot is eligible; raw checkpoint bytes are not
		// an authority or a substitute for the freshly claimed private state.
		if req.Progress == nil {
			return botChatFailure(bot.ErrorBlocked, store.ErrBotInvalid)
		}
		p := &botProgressRuntime{req: req, state: req.Progress}
		m, err := p.mutation(ctx)
		if err != nil {
			return botChatFailure(bot.ErrorBlocked, err)
		}
		id := execution.BotRunIdentity{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, ActorID: req.Principal.Subject, OrgSlug: req.Principal.OrgSlug, TeamSlug: req.Principal.TeamSlug}
		ctx = execution.WithBotRunIdentity(ctx, id)
		req.Progress, err = req.ProgressStore.ContinueProgress(ctx, store.BotProgressContinueRequest{Mutation: m, CheckpointRevision: m.ExpectedRevision, Reason: store.BotProgressReasonRecovery})
		if err != nil {
			return botChatFailure(bot.ErrorBlocked, err)
		}
		return a.runChat(ctx, req, sink, true)
	}
	s := bot.ApprovalStoreFromContext(ctx)
	rs, ok := s.(store.BotApprovalResumeStore)
	if !ok {
		return botChatFailure(bot.ErrorBlocked, store.ErrBotUnsupported)
	}
	checkpoint, err := bot.DecodeApprovalCheckpoint(req.Run.Checkpoint, req.Run)
	if err != nil {
		return botChatFailure(bot.ErrorBlocked, err)
	}
	identity := execution.BotRunIdentity{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Version.Version, ActorID: req.Principal.Subject, OrgSlug: req.Principal.OrgSlug, TeamSlug: req.Principal.TeamSlug}
	if plans, ok := s.(store.BotPreparedPlanStore); ok {
		prepared, loadErr := plans.LoadBotPlanByApproval(execution.WithBotRunIdentity(ctx, identity), req.Run.Lease, checkpoint.ApprovalID)
		if loadErr == nil {
			if prepared.Stage == store.BotPlanDispatchStarted || prepared.Stage == store.BotPlanUncertain {
				result, err := botChatFailure(bot.ErrorPermanent, &bot.RunError{Class: bot.ErrorPermanent, Message: "Prepared plan leaf requires reconciliation; never replay.", UncertainEffect: true, TerminalForAttempt: true})
				result.Checkpoint = bytes.Clone(req.Run.Checkpoint)
				return result, err
			}
			// Only a currently awaiting leaf needs a fresh approved grant. Settled
			// recovery uses private evidence, not LoadResumeApproval's pending filter.
			if prepared.Stage == store.BotPlanAwaitingApproval {
				approval, err := rs.LoadResumeApproval(execution.WithBotRunIdentity(ctx, identity), req.Run.Lease)
				if err != nil || approval == nil || !checkpoint.Matches(*approval) || approval.ID != prepared.ApprovalID {
					return botChatFailure(bot.ErrorBlocked, store.ErrBotConflict)
				}
				if req.Policy != nil {
					req.Policy, err = req.Policy.WithGrant(bot.ApprovalGrant(req.Policy.Identity(), *approval))
					if err != nil {
						return botChatFailure(bot.ErrorPermission, err)
					}
					ctx = execution.WithRunPolicy(ctx, req.Policy)
				} else if !req.Run.IsInteractiveChat() || req.Authorization == nil || req.Mandate != nil {
					return botChatFailure(bot.ErrorPermission, store.ErrBotForbidden)
				}
			}
			ctx = context.WithValue(ctx, botPreparedPlanResumeKey{}, prepared)
			ctx = context.WithValue(ctx, botResumeActionKey{}, prepared.Action)
			return a.runChat(ctx, req, sink, true)
		}
		if !errors.Is(loadErr, store.ErrBotNotFound) {
			return botChatFailure(bot.ErrorBlocked, loadErr)
		}
	}
	approval, err := rs.LoadResumeApproval(execution.WithBotRunIdentity(ctx, identity), req.Run.Lease)
	if err != nil {
		return botChatFailure(bot.ErrorBlocked, err)
	}
	if approval == nil || !checkpoint.Matches(*approval) {
		return botChatFailure(bot.ErrorBlocked, store.ErrBotConflict)
	}
	if checkpoint.Format == bot.InteractionCheckpointFormat {
		return a.resumeInteraction(ctx, req, approval, sink)
	}
	// Interactive exact-action resumes are admitted only after runChat restores
	// the server-owned worker ledger and matches this approval to one pending
	// dispatch. No model or tool invocation occurs before that ownership gate.
	// A production factory prepares its live guard after scope/isolation setup.
	// BindBotChatPolicyRuntime attaches it before any model/tool invocation.
	runtime := agent.BotPolicyRuntimeFromContext(ctx)
	if req.Policy != nil {
		req.Policy, err = req.Policy.WithGrant(bot.ApprovalGrant(req.Policy.Identity(), *approval))
		if err != nil {
			return botChatFailure(bot.ErrorPermission, err)
		}
		ctx = execution.WithRunPolicy(ctx, req.Policy)
	} else if !req.Run.IsInteractiveChat() || req.Authorization == nil || req.Mandate != nil {
		return botChatFailure(bot.ErrorPermission, store.ErrBotForbidden)
	}
	guard := &botResumeGuard{live: runtime.Guard, store: s, req: req, approval: *approval}
	runtime.Guard = guard
	runtime.Approvals = nil
	ctx = agent.WithBotPolicyRuntime(ctx, runtime)
	ctx = context.WithValue(ctx, botResumeActionKey{}, approval.Action)
	req.Instructions = "Execute only the saved approved action once, then give a final report. Do not repeat earlier work or call any additional tools."
	// runChat is shared resource setup, not Start: it receives a constrained model
	// continuation and retains the original checkpoint for failure evidence.
	result, err := a.runChat(ctx, req, sink, true)
	var policyDenied *agent.BotPolicyError
	if err != nil && guard.unconsumed() && errors.As(err, &policyDenied) &&
		(policyDenied.Decision.Kind == execution.PolicyDeny || policyDenied.Decision.Kind == execution.PolicyRequestApproval) {
		// Re-read durable authority under the current lease. A typed denial is
		// retryable only when this exact approved action is still unused; a failed
		// consumption or any actual dispatch stays terminal/uncertain below.
		fresh, loadErr := rs.LoadResumeApproval(execution.WithBotRunIdentity(ctx, identity), req.Run.Lease)
		if loadErr == nil && fresh != nil && checkpoint.Matches(*fresh) && fresh.Status == store.ApprovalApproved {
			result.Status = store.RunWaitingApproval
			result.Checkpoint = bytes.Clone(req.Run.Checkpoint)
			result.Failure = nil
			result.Report = store.BotReport{Summary: "The saved approved action was denied before dispatch. Restore current authorization and the exact target before explicitly continuing.", NeedsApproval: []uuid.UUID{approval.ID}}
			message := bot.EmployeeMessageFallback(result.Status, result.Report, nil)
			result.Report.EmployeeMessage = &message
			denied := &bot.RunError{Class: bot.ErrorBlocked, Message: "Saved action denied before dispatch; exact unused approval retained.", Cause: err}
			return result, denied
		}
	}
	if err != nil && !guard.unconsumed() {
		result.Status = store.RunPartial
		result.Report.EffectState = store.BotReportEffectsUncertain
		result.Checkpoint = req.Run.Checkpoint
		result.Failure = &bot.RunError{Class: bot.ErrorPermanent, Message: "Approved action outcome requires reconciliation; never replay.", Cause: err, UncertainEffect: true, TerminalForAttempt: true}
		// runChat finalized its envelope before the exact-action guard changed
		// the outcome. Bind fresh producer-owned text to the final partial facts.
		message := bot.EmployeeMessageFallback(result.Status, result.Report, result.Failure)
		result.Report.EmployeeMessage = &message
		return result, result.Failure
	}
	if err == nil && guard.unconsumed() {
		return botChatFailure(bot.ErrorBlocked, errors.New("approved action was not executed"))
	}
	// A model report is not independent effect verification. Preserve that
	// evidence and use the store's partial outcome instead of claiming success.
	if err == nil && len(result.Report.Unverified) > 0 && result.Status != store.RunPartial {
		result.Status = store.RunPartial
		message := bot.EmployeeMessageFallback(result.Status, result.Report, nil)
		result.Report.EmployeeMessage = &message
	}
	return result, err
}

type botResumeActionKey struct{}

// BindBotChatPolicyRuntime is the trusted factory handshake. It combines fresh
// isolation/fencing classification with the adapter's durable approval state.
// Call before capturing tool contexts, then return this same runtime in resources.
func BindBotChatPolicyRuntime(ctx context.Context, live agent.BotPolicyRuntime) (agent.BotPolicyRuntime, error) {
	if live.Guard == nil || live.Classifier == nil {
		return agent.BotPolicyRuntime{}, store.ErrBotUnsupported
	}
	pending := agent.BotPolicyRuntimeFromContext(ctx)
	if resume, ok := pending.Guard.(*botResumeGuard); ok {
		resume.mu.Lock()
		if resume.consumptionStarted || resume.attempted {
			resume.mu.Unlock()
			return agent.BotPolicyRuntime{}, store.ErrBotConflict
		}
		resume.live = live.Guard
		resume.mu.Unlock()
		live.Guard = resume
		live.Approvals = nil
	} else if capture, ok := pending.Approvals.(*botApprovalCapture); ok {
		live.Approvals = capture
	}
	return live, nil
}
