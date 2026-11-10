package launcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const botProposalToolName = "propose_bot_control"
const botProposalAttemptLimit = 4

type botProposalArgs struct {
	Payload store.BotControlPayload `json:"payload" jsonschema:"Exact control payload; mandate decides routine execution versus bounded authorization; never applied by this capture tool"`
}

// botProposalTool is a separate, concrete capture capability. It is never added
// to the external tool index, classifier or policy allowlist. A tool with the
// same name cannot inherit this capability or its captured owner/lease.
type botProposalTool struct {
	botRunnableTool
	binding       *botFactoryBinding
	repo          store.BotControlProposalStore
	mu            sync.Mutex
	attempts      int
	validateInput func(any) error
}

func newBotProposalTool(b *botFactoryBinding) (tool.Tool, error) {
	svc := store.FromContext(b.owner)
	if svc == nil {
		return nil, nil
	}
	repo := svc.Bots
	if b.req.Bot.Scope == store.BotScopePersonal {
		repo = svc.PersonalBots
	}
	proposals, ok := repo.(store.BotControlProposalStore)
	if !ok {
		return nil, nil
	}
	schema := botProposalInputSchema()
	if b.req.Mandate != nil {
		schema = botMandatedProposalInputSchema()
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, err
	}
	t := &botProposalTool{binding: b, repo: proposals, validateInput: resolved.Validate}
	f, err := functiontool.New(functiontool.Config{Name: botProposalToolName, InputSchema: schema, Description: "Capture a Bot control. Mandated routine responsibility/persona/goals/memory changes become durable authorized operations; pending is not applied. Out-of-ceiling operations require bounded authorization. Non-mandated controls remain explicit owner-review proposals."}, func(ctx tool.Context, args botProposalArgs) (map[string]any, error) { return t.capture(ctx, args) })
	if err != nil {
		return nil, err
	}
	t.botRunnableTool = f.(botRunnableTool)
	return t, nil
}
func (t *botProposalTool) capture(ctx tool.Context, args botProposalArgs) (map[string]any, error) {
	if err := args.Payload.Validate(); err != nil {
		return nil, err
	}
	if t.binding.req.Mandate != nil && args.Payload.Operation == store.BotControlSchedulePause {
		return nil, fmt.Errorf("%w: use priority_pause to pause all background work, or update_scheduled_job with enabled=false for a selected schedule; schedule_pause here is a legacy review route", store.ErrBotInvalid)
	}
	if t.binding.req.Mandate != nil && args.Payload.Operation == store.BotControlResume {
		return nil, fmt.Errorf("%w: use resume_tasks with resume_targets.schedule_ids from current Bot control state for explicitly requested tasks; legacy resume cannot resume mandated recurrence", store.ErrBotInvalid)
	}
	if t.binding.req.Mandate != nil && entstore.RoutineControlResource(args.Payload.Operation) != "" {
		// Goal ownership is bound by the concrete capability, never guessed by
		// the model. Reject an explicit cross-Bot target rather than overwriting it.
		if args.Payload.Goal != nil {
			goal := args.Payload.Goal.Goal
			if goal.BotID != (store.BotID{}) && goal.BotID != t.binding.req.Bot.ID {
				return nil, store.ErrBotForbidden
			}
			goal.BotID = t.binding.req.Bot.ID
			copyGoal := *args.Payload.Goal
			copyGoal.Goal = goal
			args.Payload.Goal = &copyGoal
		}
		svc := store.FromContext(t.binding.owner)
		if svc == nil || svc.BotAuthorizedOperationFactory == nil {
			return nil, store.ErrBotUnsupported
		}
		runtime, err := svc.BotAuthorizedOperationFactory(ctx, svc, t.binding.req.Bot.Scope)
		if err != nil {
			return nil, err
		}
		if runtime == nil {
			return nil, store.ErrBotUnsupported
		}
		ref := t.binding.req.Mandate.Authorization.Ref()
		q := store.BotAuthorizedOperationRequest{Lease: t.binding.req.Run.Lease, MandateRef: ref, IdempotencyKey: ctx.FunctionCallID(), Payload: args.Payload}
		op, err := runtime.EnqueueAuthorizedOperation(ctx, q)
		if err != nil {
			var targetErr *store.BotResumeTargetError
			if errors.As(err, &targetErr) {
				return map[string]any{"status": "error", "error_code": store.BotResumeInvalidTargetCode, "field": "resume_targets.schedule_ids", "invalid_target": targetErr.ScheduleID.String(), "message": targetErr.Error()}, nil
			}
			return nil, err
		}
		if op == nil {
			return nil, store.ErrBotConflict
		}
		saved, err := runtime.ReadAuthorizedOperation(ctx, ref, op.ID)
		if err != nil {
			return nil, err
		}
		if saved == nil || saved.Provenance == nil || op.Provenance == nil || saved.Provenance.PayloadDigest != op.Provenance.PayloadDigest || saved.Status != op.Status || saved.Decision != op.Decision {
			return nil, store.ErrBotConflict
		}
		if op.Status == store.BotOperationPending {
			if err := verifyRoutineCapture(q, op, saved); err != nil {
				return nil, err
			}
			payloadJSON, err := json.Marshal(saved.Payload)
			if err != nil {
				return nil, err
			}
			capture := bot.RoutineCapture{PayloadJSON: string(payloadJSON), OperationID: saved.ID, BotID: ref.BotID, SourceRunID: q.Lease.RunID, OwnerID: t.binding.req.Bot.OwnerID, Operation: saved.Payload.Operation, Status: saved.Status, Revision: saved.Revision, ObservedAt: time.Now().UTC()}
			bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true, RoutineCapture: &capture})
			return map[string]any{"operation_id": saved.ID.String(), "bot_id": ref.BotID.String(), "digest": saved.Provenance.PayloadDigest, "status": saved.Status, "decision": saved.Decision, "message": capture.Acknowledgment()}, nil
		}
		// Only priority_pause has an immediate-apply contract. A routine capture
		// must never turn an unexpected applied result into a success claim.
		if op.Status == store.BotOperationApplied && args.Payload.Operation != store.BotControlPriorityPause {
			return nil, store.ErrBotConflict
		}
		summary := "Captured durable routine operation; not applied"
		if op.Status == store.BotOperationApplied {
			summary = "Priority pause applied; autonomous work remains paused"
		}
		bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true, Changed: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: summary, Source: "bot-operation://" + op.ID.String(), ObservedAt: time.Now().UTC(), ActorID: t.binding.req.Bot.OwnerID}}})
		message := "Captured under the current mandate; pending is not applied. The worker commits after the source turn ends."
		if op.Status == store.BotOperationApplied {
			message = "Priority pause applied: autonomous work remains paused and schedules are disabled. Repeating pause on an already-paused Bot leaves explicit conversations running; do not repeat it to verify the pause."
		}
		if op.Decision == store.BotOperationRequireAuthorization {
			message = "Outside the current mandate: bounded owner authorization is required for this exact operation. Do not retry or claim applied."
		}
		return map[string]any{"operation_id": op.ID.String(), "bot_id": ref.BotID.String(), "digest": op.Provenance.PayloadDigest, "status": op.Status, "decision": op.Decision, "message": message}, nil
	}
	// The store verifies this fence against the LIVE lease, actor and run. Expiry
	// and idempotency are server-selected, never model supplied.
	p, err := t.repo.ProposeControl(ctx, store.BotControlProposalRequest{Lease: t.binding.req.Run.Lease, IdempotencyKey: ctx.FunctionCallID(), ExpiresAt: time.Now().Add(24 * time.Hour), Payload: args.Payload})
	if err != nil {
		return nil, err
	}
	// Verify durable capture, never application of the proposed behavior.
	saved, readErr := t.repo.ReadControlProposal(ctx, t.binding.req.Bot.ID, p.ID)
	if readErr == nil && saved != nil && saved.ID == p.ID && saved.BotID == p.BotID && saved.Digest == p.Digest && saved.Status == store.BotControlProposed {
		bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true, Changed: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "Saved control proposal for review; not applied", Source: "bot-proposal://" + p.ID.String(), ObservedAt: time.Now().UTC(), ActorID: t.binding.req.Bot.OwnerID}}})
	}
	return map[string]any{"proposal_id": p.ID.String(), "bot_id": p.BotID.String(), "digest": p.Digest, "status": p.Status, "message": "Proposed, not applied. Review the saved proposal and explicitly confirm after this conversation finishes."}, nil
}
func (t *botProposalTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil || ctx.SessionID() != t.binding.req.Run.SessionID.String() || ctx.FunctionCallID() == "" {
		return nil, store.ErrBotForbidden
	}
	bound := botBoundToolContext{Context: ctx, owner: t.binding.bind(ctx)}
	if err := t.binding.check(bound); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.attempts++
	n := t.attempts
	t.mu.Unlock()
	if n > botProposalAttemptLimit {
		return nil, store.ErrBotConflict
	}
	if err := t.binding.req.Budget.ReserveTool(bound, botProposalToolName); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, store.ErrBotInvalid
	}
	if len(raw) > 32768 {
		return nil, store.ErrBotInvalid
	}
	// Validate wire presence before Go decoding can turn an omitted or null
	// expected_revision into zero. Use exactly the schema shown to the model.
	var wire any
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, store.ErrBotInvalid
	}
	if err := t.validateInput(wire); err != nil {
		failure := fmt.Errorf("%w: proposal input: %v; provide required fields from current context, never guess revisions", store.ErrBotInvalid, err)
		// Preserve the trusted pre-dispatch error, not merely the absence of a
		// success receipt. A later successful capture must not erase this failure.
		bot.RecordToolOutcome(bound, ctx.FunctionCallID(), bot.ToolOutcome{Unverified: []string{failure.Error()}})
		return nil, failure
	}
	var parsed botProposalArgs
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&parsed) != nil || d.Decode(new(any)) != io.EOF {
		return nil, store.ErrBotInvalid
	}
	return t.capture(bound, parsed)
}
func (t *botProposalTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
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
	if req != nil && req.Tools != nil {
		req.Tools[botProposalToolName] = t
	}
	return nil
}
