package api

import (
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

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
					hide = true
				}
			}
		}
		// Compare the typed publication renderer, not a prefix of arbitrary text.
		// Only the known waiting receipt (including its historical ask_user warning)
		// is redundant. Any additional failure/evidence remains visible.
		if e.Author == "assistant" && e.InvocationID == "bot-report:"+a.RunID.String() && len(e.Content.Parts) == 1 {
			waiting := store.BotRun{ID: a.RunID, Status: store.RunWaitingApproval, Report: store.BotReport{Summary: "Question awaits the explicit Bot owner's typed answer; no execution resources are held. Continuation uses the saved conversation under current tool policy.", NeedsApproval: []uuid.UUID{a.ID}}}
			p := e.Content.Parts[0]
			if p != nil && p.Text == bot.RenderReport(waiting) {
				hide = true
			}
			waiting.Report.Unverified = []string{"Tool ask_user returned without independently verified outcome metadata."}
			if p != nil && p.Text == bot.RenderReport(waiting) {
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
