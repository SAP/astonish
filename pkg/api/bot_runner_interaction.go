package api

import (
	"context"
	"encoding/json"
	"log/slog"
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
		slog.Error("bot question approval capture failed", "bot_id", c.req.Bot.ID, "run_id", c.req.Run.ID, "error", err)
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
	if c := req.Run.TitleConfirmation; c != nil {
		if c.ValidateBinding(req.Run, req.Bot.OwnerID) != nil || c.State != store.BotTitleConfirmationConfirmed {
			return botChatFailure(bot.ErrorPermission, store.ErrBotForbidden)
		}
		// Only the initial confirmation replaces the assignment. Later questions
		// resume their own correlated answer without re-executing the title.
		if c.ApprovalID == approval.ID {
			req.Instructions = c.ConfirmedAssignment
		}
	}
	if approval.Interaction.Kind == store.BotInteractionPlanDefinition {
		req.PlanMode = false
		req.Instructions = "Execute the owner's approved plan in this original session. Use update_plan for verified progress and announce_completion for Results. Consent is not a tool permission grant."
	}
	return a.runChat(ctx, req, sink, true)
}

type botResumeInteractionKey struct{}

// botInteractionAnswerMessage correlates a typed owner event with the original
// persisted tool invocation. ask_user already returned a successful card result;
// appending a second FunctionResponse for that call would create an invalid tool
// transcript for some providers. Instead persist a structured user event with
// both IDs, keeping the original call and response intact. Missing, ambiguous or
// subsequently-used transcripts fail closed rather than regenerating the task.
func botInteractionAnswerMessage(events session.Events, approval *store.BotApproval) (*genai.Content, error) {
	if events == nil {
		return nil, store.ErrBotInvalid
	}
	var source []*session.Event
	for event := range events.All() {
		source = append(source, event)
	}
	return store.BotInteractionAnswerMessage(source, approval)
}
