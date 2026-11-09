package tui

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotHomeComposerAllConversationalStates(t *testing.T) {
	for _, status := range []store.BotStatus{store.BotDraft, store.BotPaused, store.BotActive} {
		t.Run(string(status), func(t *testing.T) {
			b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New(), Status: status}}}
			m := newModel(context.Background(), Config{Backend: b})
			m.ta.SetValue("home message")
			next, _ := m.submit()
			if b.submitted != "home message" || next.(model).ta.Value() != "" {
				t.Fatal("home conversation blocked")
			}
		})
	}
}
