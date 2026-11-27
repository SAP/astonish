package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// projectBotHomeNarrative suppresses private prose before the generic metadata
// allowlist can erase its origin. Identity survives as a cursor-only frame.
// Publication text must have been verified by ProjectBot against its exact receipt.
func projectBotHomeNarrative(e *session.Event, homeID, sessionID uuid.UUID) *session.Event {
	return projectVerifiedBotHomeNarrative(e, homeID, sessionID, nil)
}

// ProjectHomeNarrative re-reads the immutable Home row through the scoped store;
// copied binding JSON and booleans alone are never presentation authority.
func (p *SessionEventProjector) ProjectHomeNarrative(ctx context.Context, e *session.Event) *session.Event {
	var binding *store.BotEmployeeInterim
	if e != nil && p.SessionID == p.HomeSessionID {
		raw, _ := json.Marshal(e.Actions.StateDelta["bot_runtime_event"])
		var wire bot.RuntimeEvent
		if json.Unmarshal(raw, &wire) == nil && wire.Type == "text" {
			if reader, ok := p.Repository.(store.BotEmployeeInterimReader); ok {
				binding, _ = reader.ReadBotEmployeeInterim(ctx, p.BotID, wire.RunID, e.ID)
				if binding != nil && binding.BotID != p.BotID {
					binding = nil
				}
			}
		}
	}
	return projectVerifiedBotHomeNarrative(e, p.HomeSessionID, p.SessionID, binding)
}

func projectVerifiedBotHomeNarrative(e *session.Event, homeID, sessionID uuid.UUID, binding *store.BotEmployeeInterim) *session.Event {
	if e == nil || homeID != sessionID {
		return e
	}
	projected := *e
	if e.Author == "bot_runtime" {
		if e.Content == nil && e.Actions.StateDelta[store.BotRunStateKey] != nil {
			// Canonical state is neutral product metadata, never Employee prose.
			projected.Actions = session.EventActions{StateDelta: map[string]any{store.BotRunStateKey: e.Actions.StateDelta[store.BotRunStateKey]}}
			return &projected
		}
		raw, err := json.Marshal(e.Actions.StateDelta["bot_runtime_event"])
		var wire bot.RuntimeEvent
		if err == nil && json.Unmarshal(raw, &wire) == nil {
			switch wire.Type {
			case "text":
				// Only the fenced repository writer may mark released Employee interim
				// narration. An unbound historical runtime row is diagnostic, not speech.
				if binding != nil && binding.HomeSessionID == homeID && binding.EventID == e.ID && binding.RunID == wire.RunID && binding.SourceSessionID == wire.SessionID && binding.TurnID == wire.TurnID && binding.Sequence == wire.Sequence && binding.EnvelopeDigest == store.BotEmployeeInterimDigest(wire) {
					var text struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(wire.Payload, &text) == nil && text.Text != "" && binding.TextDigest == store.BotEmployeeInterimDigest(text.Text) {
						projected.Content = genai.NewContentFromText(text.Text, "model")
						projected.Actions = session.EventActions{}
						return &projected
					}
				}
			case "chat_question", "approval", "plan_approval", "bot_review", "artifact", "bot_state", "session_title":
				return &projected
			}
			projected.Content = nil
			projected.Actions = session.EventActions{}
			return &projected
		}
	}
	if _, ok := e.Actions.StateDelta[store.BotFilePublicationKey]; ok {
		return &projected
	}
	if e.Content != nil {
		content := *e.Content
		content.Parts = nil
		for _, part := range e.Content.Parts {
			if part == nil || part.Thought || part.FunctionCall != nil || part.FunctionResponse != nil {
				continue
			}
			if e.Content.Role == "user" {
				// Preserve only user-authored text/attachment fields, never model or
				// tool metadata attached to an otherwise legitimate user part.
				if part.Text != "" || part.InlineData != nil || part.FileData != nil {
					content.Parts = append(content.Parts, &genai.Part{Text: part.Text, InlineData: part.InlineData, FileData: part.FileData})
				}
			} else if e.Actions.StateDelta[store.BotPublicationKey] != nil && part.Text != "" {
				content.Parts = append(content.Parts, &genai.Part{Text: part.Text})
			}
		}
		projected.Content = &content
	}
	return &projected
}
