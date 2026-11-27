package entstore

import (
	"context"
	"encoding/json"
	"strings"

	personalevent "github.com/SAP/astonish/ent/personal/sessionevent"
	teamevent "github.com/SAP/astonish/ent/team/sessionevent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

type botWorkProgressKey struct{}

func botEmployeeInterimBinding(b store.Bot, r store.BotRun, e *adksession.Event) *store.BotEmployeeInterim {
	if e == nil || e.ID == "" || e.Author != "bot_runtime" || e.Content != nil || e.Partial {
		return nil
	}
	raw, err := json.Marshal(e.Actions.StateDelta["bot_runtime_event"])
	var wire bot.RuntimeEvent
	var payload struct {
		Text string `json:"text"`
	}
	if err != nil || json.Unmarshal(raw, &wire) != nil || wire.Type != "text" || wire.RunID != r.ID || wire.SessionID != r.SessionID || wire.TurnID == uuid.Nil || wire.Sequence < 1 || json.Unmarshal(wire.Payload, &payload) != nil || payload.Text == "" {
		return nil
	}
	return &store.BotEmployeeInterim{BotID: b.ID, RunID: r.ID, SourceSessionID: r.SessionID, HomeSessionID: b.HomeSessionID, EventID: e.ID, TurnID: wire.TurnID, Sequence: wire.Sequence, TextDigest: store.BotEmployeeInterimDigest(payload.Text), EnvelopeDigest: store.BotEmployeeInterimDigest(wire)}
}

func (s *personalBotStore) ReadBotEmployeeInterim(ctx context.Context, b store.BotID, run store.RunID, event string) (*store.BotEmployeeInterim, error) {
	return s.repository().readBotEmployeeInterim(ctx, b, run, event)
}
func (s *teamBotStore) ReadBotEmployeeInterim(ctx context.Context, b store.BotID, run store.RunID, event string) (*store.BotEmployeeInterim, error) {
	return s.repository().readBotEmployeeInterim(ctx, b, run, event)
}
func (r *botRepository) readBotEmployeeInterim(ctx context.Context, id store.BotID, runID store.RunID, eventID string) (*store.BotEmployeeInterim, error) {
	b, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	run, err := r.GetRun(ctx, id, runID)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	switch d := r.db.(type) {
	case personalBotDB:
		row, e := d.client.SessionEvent.Query().Where(personalevent.SessionIDEQ(b.HomeSessionID.String()), personalevent.EventIDEQ(eventID)).Only(ctx)
		if e != nil {
			return nil, store.ErrBotNotFound
		}
		data = row.EventData
	case teamBotDB:
		row, e := d.client.SessionEvent.Query().Where(teamevent.SessionIDEQ(b.HomeSessionID.String()), teamevent.EventIDEQ(eventID)).Only(ctx)
		if e != nil {
			return nil, store.ErrBotNotFound
		}
		data = row.EventData
	default:
		return nil, store.ErrBotUnsupported
	}
	var event *adksession.Event
	switch d := r.db.(type) {
	case personalBotDB:
		event, err = d.contentCodec().decodeSessionEvent(ctx, id, b.HomeSessionID.String(), data)
	case teamBotDB:
		event, err = d.contentCodec().decodeSessionEvent(ctx, id, b.HomeSessionID.String(), data)
	}
	if err != nil {
		return nil, err
	}
	binding := botEmployeeInterimBinding(*b, *run, event)
	var saved store.BotEmployeeInterim
	raw, _ := json.Marshal(event.Actions.StateDelta[store.BotEmployeeInterimKey])
	if binding == nil || json.Unmarshal(raw, &saved) != nil || saved != *binding {
		return nil, store.ErrBotNotFound
	}
	return binding, nil
}

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
func botWorkAnswerEvent(ctx context.Context, tx botDB, r store.BotRun, e *adksession.Event) (bool, error) {
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
