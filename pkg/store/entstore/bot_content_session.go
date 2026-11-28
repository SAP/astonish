package entstore

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

// botSessionEventRecord binds physical Bot/session/event identity to the
// database bytes that hold Chat's serialized event.
type botSessionEventRecord struct {
	botRuntimeInline
	BotID     store.BotID `json:"bot_id"`
	SessionID string      `json:"session_id"`
	EventID   string      `json:"event_id"`
}

// botSessionEventContent decodes the stored session-event body (for backup
// artifact-closure inspection). The event is serialized via Chat's map codec.
type botSessionEventContent struct {
	BotID     store.BotID       `json:"bot_id"`
	SessionID string            `json:"session_id"`
	Event     *adksession.Event `json:"event"`
}

func validBotSessionEventIdentity(botID store.BotID, sessionID, eventID string) bool {
	return botID != uuid.Nil && sessionID != "" && eventID != "" &&
		len(sessionID) <= 256 && len(eventID) <= 256 && utf8.ValidString(sessionID) && utf8.ValidString(eventID)
}

func readBotSessionEventRecord(botID store.BotID, sessionID string, data map[string]any) (*botSessionEventRecord, error) {
	var record botSessionEventRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return nil, err
	}
	if err := validateRuntimePayload(record.botRuntimeInline); err != nil {
		return nil, err
	}
	if record.BotID != botID || record.SessionID != sessionID || !validBotSessionEventIdentity(botID, sessionID, record.EventID) {
		return nil, store.ErrBotInvalid
	}
	return &record, nil
}

// botSessionEventID permits identity queries/replacement without hydrating any
// event content. It validates only metadata; it is not proof of file integrity.
func botSessionEventID(botID store.BotID, sessionID string, data map[string]any) (string, error) {
	if _, ok := data[botCleanupRunStateKey]; ok {
		event, err := decodeCleanupRunEvent(botID, sessionID, data)
		if err != nil {
			return "", err
		}
		return event.ID, nil
	}
	record, err := readBotSessionEventRecord(botID, sessionID, data)
	if err != nil {
		return "", err
	}
	return record.EventID, nil
}

func (c *botContentCodec) encodeSessionEvent(ctx context.Context, botID store.BotID, sessionID string, event *adksession.Event) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if event == nil || !validBotSessionEventIdentity(botID, sessionID, event.ID) {
		return nil, store.ErrBotInvalid
	}
	mapped, err := adkEventToMap(event)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(mapped)
	if err != nil {
		return nil, err
	}
	return c.encodeRuntimeRecord(ctx, botID, &botSessionEventRecord{BotID: botID, SessionID: sessionID, EventID: event.ID, botRuntimeInline: runtimeInline(raw)})
}
func (c *botContentCodec) decodeSessionEvent(ctx context.Context, botID store.BotID, sessionID string, data map[string]any) (*adksession.Event, error) {
	if _, ok := data[botCleanupRunStateKey]; ok {
		return decodeCleanupRunEvent(botID, sessionID, data)
	}
	record, err := readBotSessionEventRecord(botID, sessionID, data)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	raw, err := c.readRuntimeBody(ctx, botID, record.botRuntimeInline)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, store.ErrBotInvalid
	}
	e, err := eventDataToADKEvent(payload)
	if err != nil || e == nil || e.ID != record.EventID {
		return nil, store.ErrBotConflict
	}
	return e, nil
}
