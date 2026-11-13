package entstore

import (
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// ADK serializes Event.ID as "ID", not "id". Exercise the production
// transcript writer and both scoped lookup adapters rather than hand-built JSON.
func TestBotSkillCanonicalEventLookup(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, _, worker, run, lease, now := botRunFixture(t, scope)
			event := &session.Event{ID: "canonical-owner-source", Author: "user", Timestamp: *now,
				LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Observe", "user")}}
			if err := r.appendBotRunEvent(worker, lease, run.SessionID, event); err != nil {
				t.Fatal(err)
			}
			db := r.db.(botSkillDB)
			got, err := db.skillSourceEvent(worker, run.SessionID, event.ID)
			if err != nil || got == nil || got.ID != event.ID || got.Author != "user" || !got.Timestamp.Equal(event.Timestamp) || got.Content.Parts[0].Text != "Observe" {
				t.Fatalf("canonical event not recovered: %+v %v", got, err)
			}
			for _, tc := range []struct {
				sessionID uuid.UUID
				eventID   string
			}{
				{uuid.New(), event.ID}, {run.SessionID, "missing"},
			} {
				if _, err := db.skillSourceEvent(worker, tc.sessionID, tc.eventID); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("foreign/missing source resolved: %v", err)
				}
			}
		})
	}
}
