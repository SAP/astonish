package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

func TestBotCommandsRemainUsableDuringPartialTranscript(t *testing.T) {
	b := &testBotBackend{selected: &store.Bot{ID: uuid.New()}}
	m := newModel(context.Background(), Config{Backend: b})
	m.tr.Apply(events.NewText("partial"))
	m.ta.SetValue("yes")
	next, cmd := m.submit()
	m = next.(model)
	if cmd != nil || len(m.history) != 0 {
		t.Fatal("observer submitted a chat turn")
	}
	m.ta.SetValue("/bots detach")
	next, _ = m.submit()
	if next.(model).botSelected() {
		t.Fatal("partial transcript blocked detach")
	}
}

func TestBotDecisionExpiryRecheckedAtConfirmation(t *testing.T) {
	b := &testBotBackend{selected: &store.Bot{ID: uuid.New()}}
	m := newModel(context.Background(), Config{Backend: b})
	a := store.BotApproval{ID: uuid.New(), BotID: b.selected.ID, Status: store.ApprovalPending, ActionDigest: "exact", ExpiresAt: time.Now().Add(time.Hour)}
	m.bots = botsState{open: true, approvals: true, decisions: []store.BotApproval{a}}
	next, _ := m.handleBotsKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(model)
	m.bots.decisions[0].ExpiresAt = time.Now().Add(-time.Second)
	next, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || !strings.Contains(next.(model).bots.err, "expired") {
		t.Fatal("expired confirmation submitted")
	}
}

func TestBotSessionSwitchCancelsViewer(t *testing.T) {
	b := &testBotBackend{selected: &store.Bot{ID: uuid.New()}}
	m := newModel(context.Background(), Config{Backend: b})
	ctx, cancel := context.WithCancel(context.Background())
	m.botCancel = cancel
	m.openSessionsPicker()
	if ctx.Err() == nil || b.selected != nil {
		t.Fatal("sessions retained Bot viewer")
	}
}
