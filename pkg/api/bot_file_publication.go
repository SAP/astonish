package api

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Both finite reload and live catchup use the same committed receipt comparison.
// StateDelta is presentation data, never evidence that a publication committed.
func marshalCommittedBotTranscriptEvent(ctx context.Context, repo store.BotStore, event *session.Event, botID, sessionID uuid.UUID) ([]byte, error) {
	if event != nil {
		if value, ok := event.Actions.StateDelta[store.BotFilePublicationKey]; ok {
			p, err := decodeBotFilePublication(event, value, botID, sessionID)
			if err != nil {
				return nil, err
			}
			reader, ok := repo.(store.BotFilePublicationStore)
			if !ok {
				return nil, store.ErrBotUnsupported
			}
			committed, err := reader.ReadFilePublication(ctx, botID, p.RunID, p.ReportSequence)
			if err != nil {
				return nil, err
			}
			if !reflect.DeepEqual(p, committed) {
				return nil, store.ErrBotInvalid
			}
		}
	}
	return marshalBotTranscriptEvent(event, botID, sessionID)
}

func decodeBotFilePublication(event *session.Event, value any, botID, sessionID uuid.UUID) (*store.BotFilePublication, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var p store.BotFilePublication
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Validate() != nil || p.BotID != botID || p.HomeSessionID != sessionID || p.EventID != event.ID || event.InvocationID != "bot-report:"+p.RunID.String() || event.Author != "assistant" || event.Partial || event.Content == nil || event.Content.Role != "model" || len(event.Content.Parts) != 1 || event.Content.Parts[0] == nil || event.Content.Parts[0].Text != p.Summary || event.Content.Parts[0].Thought {
		return nil, store.ErrBotInvalid
	}
	return &p, nil
}

func marshalBotFilePublication(event *session.Event, value any, botID, sessionID uuid.UUID) ([]byte, error) {
	p, err := decodeBotFilePublication(event, value, botID, sessionID)
	if err != nil {
		return nil, err
	}
	// Construct a fresh event: unknown fields, content parts, actions, storage refs,
	// thought signatures and private response metadata cannot leak through replay.
	clean := &session.Event{ID: event.ID, InvocationID: event.InvocationID, Author: event.Author, Timestamp: event.Timestamp, LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(p.Summary, "model")}, Actions: session.EventActions{StateDelta: map[string]any{store.BotFilePublicationKey: p}}}
	return json.Marshal(clean)
}

func duplicateBotFilePublication(event *session.Event, seen map[string]bool) bool {
	if event == nil {
		return false
	}
	if _, ok := event.Actions.StateDelta[store.BotFilePublicationKey]; !ok {
		return false
	}
	if seen[event.ID] {
		return true
	}
	seen[event.ID] = true
	return false
}
