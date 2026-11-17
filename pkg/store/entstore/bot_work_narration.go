package entstore

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

type botWorkProgressKey struct{}

// Only the runtime's tool-boundary-released wire narration is public progress.
// Raw producer ADK content, thoughts, source responses and final text stay private.
// runBotChatAttempt holds text until a tool call proves it is interim; its final
// answer goes through the committed publication path instead.
func botWorkProgressEvent(r store.BotRun, work *store.BotWorkState, e *adksession.Event) bool {
	if work == nil || work.Protocol != store.BotWorkProtocolAgentLedV1 || work.EmployeeReviewPolicy != store.BotEmployeeReviewEvidenceBoundV1 || r.Presentation != store.BotRunPresentationHomeInteractive || e.Author != "bot_runtime" || e.Content != nil || e.Partial {
		return false
	}
	raw, err := json.Marshal(e.Actions.StateDelta["bot_runtime_event"])
	if err != nil {
		return false
	}
	var envelope bot.RuntimeEvent
	if json.Unmarshal(raw, &envelope) != nil || envelope.RunID != r.ID || envelope.SessionID != r.SessionID || envelope.TurnID == uuid.Nil || envelope.Sequence < 1 {
		return false
	}
	switch envelope.Type {
	case "text":
		return true
	default:
		return false
	}
}

// The answer is not public merely because a model supplied a user-role row or a
// JSON type marker. Require the deterministic owner-answer ID and the consumed,
// version-bound approval with the exact persisted typed answer.
func botWorkAnswerEvent(ctx context.Context, tx botDraftDB, r store.BotRun, e *adksession.Event) (bool, error) {
	if e.Partial || e.Author != "user" || e.Content == nil || e.Content.Role != "user" || len(e.Content.Parts) != 1 || e.Content.Parts[0] == nil || e.Actions.StateDelta["_astonish_turn_context"] == true || !strings.HasPrefix(e.ID, "bot-owner-answer:") {
		return false, nil
	}
	id, err := uuid.Parse(strings.TrimPrefix(e.ID, "bot-owner-answer:"))
	if err != nil {
		return false, nil
	}
	a, err := tx.ReadApproval(ctx, r.BotID, id)
	if err != nil {
		return false, err
	}
	if a.RunID != r.ID || a.Version != r.Version || a.Status != store.ApprovalConsumed || a.Interaction == nil || a.Interaction.ValidateResponse(a.Response) != nil {
		return false, nil
	}
	var answer struct {
		Type       string                       `json:"type"`
		ApprovalID uuid.UUID                    `json:"approval_id"`
		Tool       string                       `json:"tool"`
		Response   store.BotInteractionResponse `json:"response"`
	}
	if json.Unmarshal([]byte(e.Content.Parts[0].Text), &answer) != nil {
		return false, nil
	}
	return answer.Type == "bot_owner_answer" && answer.ApprovalID == id && answer.Tool == "ask_user" && answer.Response == *a.Response, nil
}
