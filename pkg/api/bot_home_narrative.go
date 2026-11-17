package api

import (
	"encoding/json"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// projectBotHomeNarrative is a read-only presentation boundary shared by live
// Studio, replay and the terminal. Keep the row identity/cursor even when an old
// Home row contains private telemetry. Run transcripts are never projected here.
func projectBotHomeNarrative(e *session.Event, homeID, sessionID uuid.UUID) *session.Event {
	if e == nil || homeID != sessionID {
		return e
	}
	projected := *e
	if e.Author == "bot_runtime" {
		raw, err := json.Marshal(e.Actions.StateDelta["bot_runtime_event"])
		var wire bot.RuntimeEvent
		if err == nil && json.Unmarshal(raw, &wire) == nil {
			switch wire.Type {
			case "text":
				// Released narration is already a public speech segment, not speculative
				// streaming text. Use the existing complete-content projection so clients
				// show it immediately without needing a visible tool to finalize the bubble.
				var text struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(wire.Payload, &text) == nil && text.Text != "" {
					projected.Content = genai.NewContentFromText(text.Text, "model")
					projected.Actions = session.EventActions{}
					return &projected
				}
			case "tool_call", "tool_result":
				var tool struct {
					Name string `json:"name"`
				}
				if json.Unmarshal(wire.Payload, &tool) == nil && tool.Name == "ask_user" {
					return e
				}
				projected.Content = nil
				projected.Actions = session.EventActions{}
				return &projected
			case "thinking", "subtask_progress":
				projected.Content = nil
				projected.Actions = session.EventActions{}
				return &projected
			}
		}
	}
	if e.Content != nil {
		content := *e.Content
		content.Parts = nil
		for _, p := range e.Content.Parts {
			if p == nil || p.Thought || p.FunctionCall != nil && p.FunctionCall.Name != "ask_user" || p.FunctionResponse != nil && p.FunctionResponse.Name != "ask_user" {
				continue
			}
			content.Parts = append(content.Parts, p)
		}
		projected.Content = &content
	}
	return &projected
}
