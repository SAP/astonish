package launcher

import (
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	adksession "google.golang.org/adk/session"
)

const botRuntimeStateKey = "bot_runtime_event"
const botTurnContextStateKey = "_astonish_turn_context"

// Hide model-only context at presentation, never by text prefix or ADK role.
// The durable row and its transport cursor remain intact.
func botHiddenTurnContext(e *adksession.Event) bool {
	hidden, _ := e.Actions.StateDelta[botTurnContextStateKey].(bool)
	return hidden
}

// botTranscriptMapper selects the ordered Chat wire projection as soon as the
// runtime announces itself. ChatRunner emits session before running ADK, so this
// happens before any of its model/tool rows, on replay as well as live. ADK user
// rows remain the input source (ChatRunner does not emit user_message). Legacy
// transcripts without wire envelopes keep their existing ADK rendering.
// Do not deduplicate by text/tool values: repeated calls and equal text chunks
// are legitimate. Raw model rows also contain unredacted/internal tool details;
// mixing them with the wire projection would bypass Chat's UI normalization.
// One mapper lives for the whole Watch, including transport reconnects.
type botTranscriptMapper struct {
	runtimeTurns      map[string]bool
	activeRuntimeTurn string // bounded session → done window for ADK invocation binding
	sequences         map[string]int64
}

func (m *botTranscriptMapper) mapFrame(frame client.SSEEvent) ([]events.Event, error) {
	var row adksession.Event
	if err := json.Unmarshal([]byte(frame.Data), &row); err != nil {
		return nil, fmt.Errorf("decode Bot transcript: %w", err)
	}
	if botHiddenTurnContext(&row) {
		return nil, nil
	}
	if raw, ok := row.Actions.StateDelta[store.BotOperationKey]; ok {
		data, err := json.Marshal(raw)
		var operation store.BotOperationView
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(data, &operation) != nil || operation.Validate() != nil {
			return nil, nil
		}
		return []events.Event{{Kind: events.KindBotOperation, BotOperation: &operation}}, nil
	}
	if raw, ok := row.Actions.StateDelta[store.BotRunStateKey]; ok {
		data, err := json.Marshal(raw)
		var state store.BotRunState
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &state); err != nil {
			return nil, err
		}
		return []events.Event{{Kind: events.KindBotRunState, BotRunState: &state}}, nil
	}
	if raw, ok := row.Actions.StateDelta[botRuntimeStateKey]; ok {
		wire, err := decodeBotRuntimeEnvelope(raw)
		if err != nil {
			return nil, err
		}
		if wire.TurnID == "" {
			wire.TurnID = row.InvocationID
		}
		// Only conversation projections suppress matching raw ADK rows.
		// Control/reference metadata does not mean a turn was rendered.
		switch wire.Type {
		case "session", "text", "tool_call", "tool_result":
			if wire.TurnID != "" {
				if m.runtimeTurns == nil {
					m.runtimeTurns = map[string]bool{}
				}
				m.runtimeTurns[wire.TurnID] = true
			}
		}
		if wire.TurnID != "" && wire.Sequence > 0 {
			if m.sequences == nil {
				m.sequences = map[string]int64{}
			}
			if wire.Sequence <= m.sequences[wire.TurnID] {
				return nil, nil
			}
			m.sequences[wire.TurnID] = wire.Sequence
		}
		if wire.Type == "session" {
			m.activeRuntimeTurn = wire.TurnID
		}
		if wire.Type == "done" || wire.Type == "turn_complete" {
			if m.activeRuntimeTurn == wire.TurnID {
				m.activeRuntimeTurn = ""
			}
		}
		var scope struct {
			InvocationID string `json:"invocationId"`
		}
		if json.Unmarshal(wire.Payload, &scope) == nil && scope.InvocationID != "" && m.runtimeTurns[wire.TurnID] {
			m.runtimeTurns[scope.InvocationID] = true
		}
		return renderBotRuntimeEnvelope(wire), nil
	}
	// ADK assigns a different invocation ID from the runtime turn UUID. Its
	// user row precedes model/tool rows, so bind it within session → done only.
	if m.activeRuntimeTurn != "" && row.Author == "user" && row.InvocationID != "" {
		m.runtimeTurns[row.InvocationID] = true
	}
	if row.Author != "user" && (m.runtimeTurns[row.InvocationID] || (row.InvocationID == "" && m.activeRuntimeTurn != "")) {
		return nil, nil
	}
	return botTranscriptEvents(frame)
}

// Keep this transport DTO local: payloads are interpreted only by Chat's shared
// mapper, not a second evolving set of artifact/delegation/usage DTOs.
type botRuntimeEnvelope struct {
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
	TurnID   string          `json:"turn_id"`
	Sequence int64           `json:"sequence"`
}

func decodeBotRuntimeEnvelope(raw any) (botRuntimeEnvelope, error) {
	var wire botRuntimeEnvelope
	data, err := json.Marshal(raw)
	if err == nil {
		err = json.Unmarshal(data, &wire)
	}
	if err == nil && (wire.Type == "" || len(wire.Payload) == 0 || string(wire.Payload) == "null") {
		err = fmt.Errorf("missing type or payload")
	}
	if err != nil {
		return wire, fmt.Errorf("decode Bot runtime envelope: %w", err)
	}
	return wire, nil
}

func mapBotRuntimeEnvelope(raw any) ([]events.Event, error) {
	wire, err := decodeBotRuntimeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	return renderBotRuntimeEnvelope(wire), nil
}

func renderBotRuntimeEnvelope(wire botRuntimeEnvelope) []events.Event {
	switch wire.Type {
	case "approval", "plan_approval", "auto_approved":
		// Replay is observation, never an executable approval. The durable, bound
		// Bot approval inbox alone supplies decisions and current validity.
		return nil
	case "chat_question":
		return []events.Event{events.NewSystem("Bot requested an answer. Use /bots approvals for the current durable interaction.")}
	case "user_message":
		var p struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(wire.Payload, &p) == nil && p.Text != "" {
			return []events.Event{events.NewUser(p.Text)}
		}
		return nil
	case "turn_complete":
		return []events.Event{events.NewDone()}
	default:
		return mapSSEToEvents(&client.SSEEvent{Type: wire.Type, Data: string(wire.Payload)}, false)
	}
}
