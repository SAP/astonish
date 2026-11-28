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

func TestBotEmployeeNeutralReceiptsAndArtifactWatch(t *testing.T) {
	b := &testBotBackend{selected: &store.Bot{ID: uuid.New()}}
	m := newModel(context.Background(), Config{Backend: b})
	for _, continuation := range []store.BotContinuationState{store.BotContinuationQueued, store.BotContinuationBlockedByPause} {
		item, ok := botEmployeeApprovalReceipt(continuation)
		if !ok || item.Kind != events.ItemSystem {
			t.Fatal("local approval impersonated Employee")
		}
	}
	p := &store.BotFilePublication{BotID: b.selected.ID, Summary: ""}
	ev := events.NewSystem("Published files: proof.md")
	ev.BotFilePublication = p
	updated, _ := m.Update(botWatchMsg{generation: m.botGeneration, event: ev})
	m = updated.(model)
	if len(m.tr.Items) != 1 || m.tr.Items[0].Kind != events.ItemSystem || m.tr.Items[0].BotFilePublication != p {
		t.Fatal("artifact-only watch lost receipt or manufactured speech")
	}
	// Failure vocabulary must not hide real Employee-authored blockers.
	original := events.Item{Kind: events.ItemAgent, Content: "I couldn't complete this. Which account should I use?"}
	if botEmployeeItem(original, true).Kind != events.ItemAgent || botEmployeeItem(original, true).Content != original.Content {
		t.Fatal("genuine Employee blocker filtered")
	}
}

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
					if projected.Kind != events.ItemSystem || projected.Content != botOutcomeUnknown {
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
