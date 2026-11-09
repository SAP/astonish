package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
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
}

func (c *botApprovalCapture) RequestApproval(ctx context.Context, id execution.BotRunIdentity, a execution.ActionDescriptor, digest string) (uuid.UUID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id != c.req.Policy.Identity() {
		return uuid.Nil, store.ErrBotForbidden
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

func (m *botApprovalCaptureModel) GenerateContent(ctx context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
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
		for response, err := range m.LLM.GenerateContent(ctx, serialReq, false) {
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
		for _, response := range responses {
			if !yield(response, nil) {
				return
			}
		}
	}
}

type botResumeModel struct {
	model.LLM
	action execution.ActionDescriptor
	first  bool
}

func (m *botResumeModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if m.first {
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
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: m.action.Tool, Args: args}}}}}, nil)
	}
}

type botResumeGuard struct {
	mu        sync.Mutex
	live      execution.PolicyGuard
	store     store.BotStore
	req       bot.RunRequest
	approval  store.BotApproval
	attempted bool
}

func (g *botResumeGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	digest, err := a.Digest()
	if err != nil {
		return execution.PolicyDecision{}, err
	}
	if g.attempted || digest != g.approval.ActionDigest {
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
	if err := g.store.ConsumeApproval(ctx, store.BotApprovalConsumption{Lease: g.req.Run.Lease, ApprovalID: g.approval.ID, Version: g.req.Run.Version, ActionDigest: digest}); err != nil {
		return execution.PolicyDecision{}, err
	}
	g.attempted = true
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest, GrantID: g.approval.ID}, nil
}

func (a *BotChatRuntimeAdapter) resume(ctx context.Context, req bot.RunRequest, sink bot.EventSink) (bot.RunResult, error) {
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
	if req.Run.IsInteractiveChat() {
		return botChatFailure(bot.ErrorPermission, store.ErrBotForbidden)
	}
	// A production factory prepares its live guard after scope/isolation setup.
	// BindBotChatPolicyRuntime attaches it before any model/tool invocation.
	runtime := agent.BotPolicyRuntimeFromContext(ctx)
	policy, err := req.Policy.WithGrant(bot.ApprovalGrant(req.Policy.Identity(), *approval))
	if err != nil {
		return botChatFailure(bot.ErrorPermission, err)
	}
	req.Policy = policy
	ctx = execution.WithRunPolicy(ctx, policy)
	guard := &botResumeGuard{live: runtime.Guard, store: s, req: req, approval: *approval}
	runtime.Guard = guard
	runtime.Approvals = nil
	ctx = agent.WithBotPolicyRuntime(ctx, runtime)
	ctx = context.WithValue(ctx, botResumeActionKey{}, approval.Action)
	req.Instructions = "Execute only the saved approved action once, then give a final report. Do not repeat earlier work or call any additional tools."
	// runChat is shared resource setup, not Start: it receives a constrained model
	// continuation and retains the original checkpoint for failure evidence.
	result, err := a.runChat(ctx, req, sink, true)
	if err != nil && guard.attempted {
		result.Status = store.RunPartial
		result.Checkpoint = req.Run.Checkpoint
		result.Failure = &bot.RunError{Class: bot.ErrorPermanent, Message: "Approved action outcome requires reconciliation; never replay.", Cause: err, UncertainEffect: true, TerminalForAttempt: true}
		return result, result.Failure
	}
	if err == nil && !guard.attempted {
		return botChatFailure(bot.ErrorBlocked, errors.New("approved action was not executed"))
	}
	// A model report is not independent effect verification. Preserve that
	// evidence and use the store's partial outcome instead of claiming success.
	if err == nil && len(result.Report.Unverified) > 0 {
		result.Status = store.RunPartial
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
		if resume.attempted {
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
