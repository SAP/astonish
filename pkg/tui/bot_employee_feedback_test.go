package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

func TestBotEmployeeErrorMatrix(t *testing.T) {
	for _, kind := range []events.Kind{events.KindError, events.KindErrorInfo} {
		for _, selected := range []bool{false, true} {
			t.Run(string(kind)+map[bool]string{true: "/bot", false: "/chat"}[selected], func(t *testing.T) {
				b := &testBotBackend{}
				if selected {
					b.selected = &store.Bot{ID: uuid.New()}
				}
				m := newModel(context.Background(), Config{Backend: b})
				m.width = 100
				m.height = 30
				m.applyEvent(events.Event{Kind: kind, Text: "secret HTTP /api/private", ErrorTitle: "secret", ErrorReason: "stack trace"})
				item := m.tr.Items[len(m.tr.Items)-1]
				projected := botEmployeeItem(item, selected)
				if selected {
					if projected.Kind != events.ItemAgent || projected.Content != botOutcomeUnknown {
						t.Fatalf("unsafe projection: %+v", projected)
					}
					rendered, _, _ := m.renderTranscript()
					if strings.Contains(rendered, "secret") {
						t.Fatal("raw error rendered")
					}
				} else if projected.Content != item.Content || projected.Kind != item.Kind {
					t.Fatal("ordinary chat changed")
				}
				if m.tr.Items[len(m.tr.Items)-1].Kind != events.ItemError {
					t.Fatal("source error state mutated")
				}
			})
		}
	}
	if strings.Contains(botEmployeeFailure(errors.New("secret")), "secret") {
		t.Fatal("local failure leaks diagnostics")
	}
}
