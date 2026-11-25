package launcher

import (
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
)

// botTranscriptMapper lives for the entire watch, including reconnects. Display
// identity is independent of cursor: several parts of a committed row can share
// a cursor, and separate events can legitimately contain identical text.
type botTranscriptMapper struct {
	seen map[string]bool
}

func (m *botTranscriptMapper) mapFrame(frame client.SSEEvent) ([]events.Event, error) {
	if frame.Type == "cursor" || frame.Type == client.BotReplayComplete {
		return nil, nil
	}
	var payload struct {
		Projection struct {
			EventID string `json:"event_id"`
		} `json:"_projection"`
	}
	if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
		return nil, fmt.Errorf("decode Bot presentation: %w", err)
	}
	if payload.Projection.EventID == "" {
		return nil, fmt.Errorf("Bot presentation missing event identity")
	}
	if m.seen[payload.Projection.EventID] {
		return nil, nil
	}
	out, err := botTranscriptEvents(frame)
	if err != nil {
		return nil, err
	}
	if m.seen == nil {
		m.seen = make(map[string]bool)
	}
	m.seen[payload.Projection.EventID] = true
	return out, nil
}

// Canonical execution payloads use Chat's mapper and sole Transcript reducer.
// Bot-only state is typed here; persisted observation never creates live Chat
// approval authority. Decisions still come from the scoped Bot approval inbox.
func botTranscriptEvents(frame client.SSEEvent) ([]events.Event, error) {
	data := []byte(frame.Data)
	if !json.Valid(data) {
		return nil, fmt.Errorf("decode Bot presentation: invalid JSON")
	}
	switch frame.Type {
	case "cursor", client.BotReplayComplete, "approval", "plan_approval", "auto_approved":
		return nil, nil
	case "transcript":
		return nil, fmt.Errorf("legacy Bot transcript is not a canonical Chat event")
	case "bot_review":
		var p struct {
			ReviewID     string `json:"reviewId"`
			EventID      string `json:"eventId"`
			InvocationID string `json:"invocationId"`
			Question     struct {
				Text string `json:"text"`
			} `json:"question"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		if p.ReviewID == "" {
			return nil, nil
		}
		text := "Bot requested a review. Use /bots approvals for the current durable interaction."
		if p.Question.Text != "" {
			text = p.Question.Text + "\n" + text
		}
		ev := events.NewSystem(text)
		ev.EventID, ev.InvocationID = p.EventID, p.InvocationID
		return []events.Event{ev}, nil
	case "chat_question":
		return []events.Event{events.NewSystem("Bot requested an answer. Use /bots approvals for the current durable interaction.")}, nil
	case "user_message":
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		if p.Text != "" {
			return []events.Event{events.NewUser(p.Text)}, nil
		}
		return nil, nil
	case "text":
		var p struct {
			Text         string `json:"text"`
			InvocationID string `json:"invocationId"`
			EventID      string `json:"eventId"`
			Projection   struct {
				EventID string `json:"event_id"`
			} `json:"_projection"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		if p.EventID == "" {
			p.EventID = p.Projection.EventID
		}
		if events.IsBotReportInvocation(p.InvocationID) && p.EventID != "" {
			return []events.Event{events.NewBotReport(p.InvocationID, p.EventID, p.Text)}, nil
		}
		return mapSSEToEvents(&frame, false), nil
	case "turn_complete":
		return []events.Event{events.NewDone()}, nil
	case store.BotOperationKey:
		var p store.BotOperationView
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, err
		}
		return []events.Event{{Kind: events.KindBotOperation, BotOperation: &p}}, nil
	case store.BotRunStateKey:
		var p store.BotRunState
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		return []events.Event{{Kind: events.KindBotRunState, BotRunState: &p}}, nil
	case store.BotFilePublicationKey:
		var p store.BotFilePublication
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, err
		}
		ev := events.NewBotReport("bot-report:"+p.RunID.String(), p.EventID, p.Summary)
		ev.BotFilePublication = &p
		return []events.Event{ev}, nil
	default:
		return mapSSEToEvents(&frame, false), nil
	}
}
