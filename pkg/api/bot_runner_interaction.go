package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/question"
	"reflect"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Capture the existing Chat event rather than emitting a competing card stream.
func (c *botApprovalCapture) captureQuestion(ctx context.Context, payload json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.approval != nil {
		c.conflict = true
		return store.ErrBotConflict
	}
	var q struct {
		QuestionID string `json:"questionId"`
	}
	if json.Unmarshal(payload, &q) != nil {
		return store.ErrBotInvalid
	}
	interaction := &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: q.QuestionID, Payload: payload}
	if err := interaction.Validate(); err != nil {
		return err
	}
	action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: payload, ConstraintsVerified: true}
	digest, err := action.Digest()
	if err != nil {
		return err
	}
	saved, err := c.store.RequestApproval(ctx, c.req.Run.Lease, store.BotApproval{BotID: c.req.Bot.ID, RunID: c.req.Run.ID, Version: c.req.Run.Version, Action: action, ActionDigest: digest, Interaction: interaction, ExpiresAt: time.Now().Add(24 * time.Hour)})
	if err != nil {
		return err
	}
	c.approval = saved
	return nil
}

// A decision is input, never a tool grant. The ordinary factory installs fresh
// policy/fencing and the shared cumulative budget for the resumed ChatRunner.
func (a *BotChatRuntimeAdapter) resumeInteraction(ctx context.Context, req bot.RunRequest, approval *store.BotApproval, sink bot.EventSink) (bot.RunResult, error) {
	if approval.Interaction == nil || approval.Interaction.ValidateResponse(approval.Response) != nil || approval.DecidedBy == nil || *approval.DecidedBy != req.Bot.OwnerID {
		return botChatFailure(bot.ErrorPermission, store.ErrBotForbidden)
	}
	ctx = context.WithValue(ctx, botResumeInteractionKey{}, approval)
	// Never submit the original task as another user turn.
	req.Instructions = "Continue the saved conversation from the owner's answer. Do not repeat completed work. The answer does not authorize additional tool permissions."
	result, err := a.runChat(ctx, req, sink, true)
	// A restart must not erase evidence (especially uncertain prior effects).
	if req.Work == nil { // work controller already aggregates the prior ledger
		result.Report.Changed = append(req.Run.Report.Changed, result.Report.Changed...)
		result.Report.Unchanged = append(req.Run.Report.Unchanged, result.Report.Unchanged...)
		result.Report.Artifacts = append(req.Run.Report.Artifacts, result.Report.Artifacts...)
		result.Report.Unverified = append(req.Run.Report.Unverified, result.Report.Unverified...)
	}
	if result.Status == store.RunSucceeded && len(result.Report.Unverified) > 0 {
		result.Status = store.RunPartial
	}
	return result, err
}

type botResumeInteractionKey struct{}

// botInteractionAnswerMessage correlates a typed owner event with the original
// persisted tool invocation. ask_user already returned a successful card result;
// appending a second FunctionResponse for that call would create an invalid tool
// transcript for some providers. Instead persist a structured user event with
// both IDs, keeping the original call and response intact. Missing, ambiguous or
// subsequently-used transcripts fail closed rather than regenerating the task.
func botInteractionAnswerMessage(events session.Events, approval *store.BotApproval) (*genai.Content, error) {
	if events == nil || approval == nil || approval.Interaction == nil || approval.Interaction.ValidateResponse(approval.Response) != nil {
		return nil, store.ErrBotInvalid
	}
	var payload map[string]any
	if err := json.Unmarshal(approval.Interaction.Payload, &payload); err != nil {
		return nil, err
	}
	var lastCall *genai.FunctionCall
	var callInvocation, matchedCall, matchedInvocation string
	matches := 0
	for i := 0; i < events.Len(); i++ {
		e := events.At(i)
		if e == nil || e.Content == nil || e.Partial {
			continue
		}
		if matches > 0 && e.Author == "user" {
			return nil, store.ErrBotConflict
		}
		for _, p := range e.Content.Parts {
			if p == nil {
				continue
			}
			if p.FunctionCall != nil {
				lastCall, callInvocation = p.FunctionCall, e.InvocationID
			}
			r := p.FunctionResponse
			if r == nil || r.Name != "ask_user" || r.Response["questionId"] != approval.Interaction.QuestionID {
				continue
			}
			if lastCall == nil || lastCall.Name != "ask_user" || lastCall.ID == "" || r.ID != lastCall.ID || e.InvocationID != callInvocation || r.Response["status"] != "ok" {
				return nil, store.ErrBotConflict
			}
			// JSON round-trip normalizes option slices across in-memory and DB stores.
			raw, err := json.Marshal(r.Response)
			if err != nil {
				return nil, err
			}
			var response map[string]any
			if err := json.Unmarshal(raw, &response); err != nil {
				return nil, err
			}
			for key, value := range payload {
				if !reflect.DeepEqual(value, response[key]) {
					return nil, store.ErrBotConflict
				}
			}
			matches++
			matchedCall, matchedInvocation = r.ID, e.InvocationID
		}
	}
	if matches != 1 || lastCall == nil || lastCall.ID != matchedCall || callInvocation != matchedInvocation {
		return nil, store.ErrBotConflict
	}
	var q question.Question
	if err := json.Unmarshal(approval.Interaction.Payload, &q); err != nil {
		return nil, err
	}
	label := approval.Response.Answer
	for _, option := range q.Options {
		if option.ID == label && option.Label != "" {
			label = option.Label
			break
		}
	}
	// Preserve the model's structured correlation contract. Display is additive
	// presentation metadata; it never replaces the response IDs or answer value.
	raw, err := json.Marshal(map[string]any{
		"type": "bot_owner_answer", "approval_id": approval.ID,
		"invocation_id": matchedInvocation, "tool_call_id": matchedCall,
		"tool": "ask_user", "response": approval.Response,
		"display": map[string]string{"prompt": q.Prompt, "answer": label},
	})
	if err != nil {
		return nil, err
	}
	return genai.NewContentFromText(string(raw), "user"), nil
}
