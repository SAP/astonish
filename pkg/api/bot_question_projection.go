package api

import (
	"encoding/json"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

// projectBotReviewQuestion enriches only the durable, approval-bound reference.
// Scoped approval reads remain authority; model text and runtime questions do not.
func projectBotReviewQuestion(e *session.Event, approvals []store.BotApproval, botID, sessionID uuid.UUID) *session.Event {
	if e == nil || e.Author != "bot_runtime" || e.Content != nil {
		return e
	}
	raw, err := json.Marshal(e.Actions.StateDelta["bot_runtime_event"])
	if err != nil {
		return e
	}
	var envelope bot.RuntimeEvent
	if json.Unmarshal(raw, &envelope) != nil || envelope.Type != "bot_review" || envelope.SessionID != sessionID || envelope.RunID == uuid.Nil {
		return e
	}
	var ref struct {
		Kind     string    `json:"kind"`
		ReviewID uuid.UUID `json:"review_id"`
	}
	if json.Unmarshal(envelope.Payload, &ref) != nil || ref.Kind != "approval" || ref.ReviewID == uuid.Nil {
		return e
	}
	identity := uuid.NewSHA1(ref.ReviewID, []byte("bot-review:approval"))
	if e.ID != identity.String() || e.InvocationID != identity.String() || envelope.TurnID != identity || envelope.Sequence != 1 {
		return e
	}
	for _, a := range approvals {
		if a.ID != ref.ReviewID || a.BotID != botID || a.RunID != envelope.RunID || a.Interaction == nil || a.Interaction.Validate() != nil {
			continue
		}
		envelope.Payload, err = json.Marshal(struct {
			Kind     string          `json:"kind"`
			ReviewID uuid.UUID       `json:"review_id"`
			Question json.RawMessage `json:"question"`
		}{ref.Kind, ref.ReviewID, a.Interaction.Payload})
		if err != nil {
			return e
		}
		projected := *e
		projected.Actions.StateDelta = make(map[string]any, len(e.Actions.StateDelta))
		for k, v := range e.Actions.StateDelta {
			projected.Actions.StateDelta[k] = v
		}
		projected.Actions.StateDelta["bot_runtime_event"] = envelope
		return &projected
	}
	return e
}

// Presentation only. Approval creation atomically appends the canonical review.
// Preserve all durable rows, diagnostic run history, and transport cursor positions.
func projectBotQuestionTranscript(e *session.Event, approvals []store.BotApproval) *session.Event {
	if e == nil || e.Content == nil || e.Content.Role != "model" || e.Author == "user" {
		return e
	}
	hide := false
	for _, a := range approvals {
		if a.ID == uuid.Nil || a.Interaction == nil || a.Interaction.Validate() != nil {
			continue
		}
		if e.Author == "model" && e.InvocationID == "" && len(e.Content.Parts) == 1 {
			if p := e.Content.Parts[0]; p != nil {
				if q := tryParseChatQuestionMessage(p.Text); q != nil && q.QuestionID == a.Interaction.QuestionID {
					// Retain this row's exact position as a reference only. The saved
					// approval, not this old marker, still owns answer authority.
					payload, _ := json.Marshal(struct {
						Kind     string    `json:"kind"`
						ReviewID uuid.UUID `json:"review_id"`
					}{"approval", a.ID})
					projected := *e
					projected.Content = nil
					projected.Author = "bot_runtime"
					projected.Actions = session.EventActions{StateDelta: map[string]any{
						"bot_runtime_event": bot.RuntimeEvent{Type: "bot_review", RunID: a.RunID, Payload: payload},
					}}
					return &projected
				}
			}
		}
		// Compare both current and historical typed renderers, not arbitrary prefixes.
		// Saved diagnostic receipts remain recognizable after employee prose changes.
		// Only the known waiting receipt (including its historical ask_user warning)
		// is redundant. Any additional failure/evidence remains visible.
		if e.Author == "assistant" && e.InvocationID == "bot-report:"+a.RunID.String() && len(e.Content.Parts) == 1 {
			waiting := store.BotRun{ID: a.RunID, Status: store.RunWaitingApproval, Report: store.BotReport{Summary: "Question awaits the explicit Bot owner's typed answer; no execution resources are held. Continuation uses the saved conversation under current tool policy.", NeedsApproval: []uuid.UUID{a.ID}}}
			p := e.Content.Parts[0]
			if p != nil && (p.Text == bot.RenderReport(waiting) || p.Text == bot.RenderDiagnosticReport(waiting)) {
				hide = true
			}
			waiting.Report.Unverified = []string{"Tool ask_user returned without independently verified outcome metadata."}
			if p != nil && (p.Text == bot.RenderReport(waiting) || p.Text == bot.RenderDiagnosticReport(waiting)) {
				hide = true
			}
		}
	}
	if !hide {
		return e
	}
	projected := *e
	projected.Content = nil
	return &projected
}
