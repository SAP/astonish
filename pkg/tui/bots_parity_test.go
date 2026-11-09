package tui

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"strings"
	"testing"
)

type parityBotBackend struct {
	testBotBackend
	fail      bool
	submitted string
}

func (b *parityBotBackend) RunTurn(_ context.Context, text string, _ backend.TurnOptions) (<-chan events.Event, error) {
	b.submitted = text
	if b.fail {
		return nil, errors.New("admission rejected")
	}
	ch := make(chan events.Event)
	close(ch)
	return ch, nil
}
func (b *parityBotBackend) BotSnapshot(context.Context) (*store.BotSnapshot, error) {
	return &store.BotSnapshot{Bot: *b.selected, PendingApprovals: []store.BotApproval{{ID: uuid.New()}}}, nil
}
func (b *parityBotBackend) BotGoals(context.Context) ([]store.BotGoal, error) { return nil, nil }
func (b *parityBotBackend) BotStudioURL() string                              { return "https://platform/chat?bot=test" }
func TestBotDecisionReplyDoesNotReopenClosedOverlay(t *testing.T) {
	b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	next, cmd := m.Update(botDecisionMsg{generation: m.botGeneration})
	if next.(model).bots.open || cmd != nil {
		t.Fatal("completed decision reopened dismissed overlay")
	}
}

func TestBotPickerIgnoresPreviousOverlayResponse(t *testing.T) {
	b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	next, old := m.botCommand("/bots")
	m = next.(model)
	next, _ = m.botCommand("/bots goals")
	m = next.(model)
	next, _ = m.Update(old())
	m = next.(model)
	if !m.bots.loading || m.bots.overview != botOverviewGoals {
		t.Fatal("old picker response contaminated new overlay")
	}
}

func TestBotComposerSingleChronologyAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New(), Status: store.BotPaused}}, fail: fail}
		m := newModel(context.Background(), Config{Backend: b})
		m.ta.SetValue("hello")
		next, _ := m.submit()
		m = next.(model)
		if b.submitted != "hello" {
			t.Fatal("paused conversation not submitted")
		}
		if fail && m.ta.Value() != "hello" {
			t.Fatal("failed input lost")
		}
		if !fail && m.ta.Value() != "" {
			t.Fatal("accepted input retained")
		}
		for _, item := range m.tr.Items {
			if item.Kind == events.ItemUser {
				t.Fatal("optimistic user echo duplicated durable chronology")
			}
		}
	}
}
func TestBotOverviewStatesAndStaleRequest(t *testing.T) {
	b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New(), Name: "Monitor", Status: store.BotPaused}}}
	m := newModel(context.Background(), Config{Backend: b})
	m.width = 100
	next, cmd := m.botCommand("/bots goals")
	m = next.(model)
	if !strings.Contains(m.renderBotsOverlay(), "Loading") {
		t.Fatal("missing loading")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(model)
	for _, s := range []string{"No goals", "Pending decisions: 1", "Next run: unavailable", "paused"} {
		if !strings.Contains(m.renderBotsOverlay(), s) {
			t.Fatal(s)
		}
	}
	m.bots.requestID = uuid.New()
	m.bots.loading = true
	next, _ = m.Update(msg)
	if !next.(model).bots.loading {
		t.Fatal("stale overview accepted")
	}
	m.bots.err = "denied"
	m.bots.loading = false
	if !strings.Contains(m.renderBotsOverlay(), "denied") {
		t.Fatal("error hidden")
	}
}
