package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestBotStopRunExactRoute(t *testing.T) {
	id, rid := uuid.New(), uuid.New()
	b := &testBotBackend{selected: &store.Bot{ID: id, Scope: store.BotScopeTeam}}
	m := newModel(context.Background(), Config{Backend: b})
	next, _ := m.botCommand("/bots stop-run")
	m = next.(model)
	if !m.bots.open || !m.bots.runs {
		t.Fatal("stop-run picker missing")
	}
	m.width = 100
	if !strings.Contains(m.renderBotsOverlay(), "Loading") {
		t.Fatal("loading state")
	}
	m.bots.loading = false
	if !strings.Contains(m.renderBotsOverlay(), "No runs") {
		t.Fatal("empty state")
	}
	if _, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("empty mutation")
	}
	m.bots.err = "forbidden"
	if !strings.Contains(m.renderBotsOverlay(), "forbidden") {
		t.Fatal("error state")
	}
	m.bots.err = ""
	closed, closeCmd := m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if closeCmd != nil || closed.(model).bots.open {
		t.Fatal("Escape must close without action")
	}
	m.bots.runItems = []store.BotRun{{ID: rid, BotID: id, Status: store.RunRunning}}
	next, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil {
		t.Fatal("selection must not mutate or watch")
	}
	// Exact route is visible, and closing it never mutates the backend.
	if !strings.Contains(m.botStopRunRoute(m.bots.runItems[0]), "--confirm team:"+id.String()+":"+rid.String()) {
		t.Fatal("not exact scoped confirmation")
	}
	next, cmd = m.botCommand("/bots detach")
	if cmd != nil || next.(model).botSelected() {
		t.Fatal("detach boundary")
	}
}
