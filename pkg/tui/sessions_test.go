package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/SAP/astonish/pkg/tui/backend"
)

type sessionDeleteBackend struct {
	staticBackend
	deletedID string
	newCalled bool
}

func (b *sessionDeleteBackend) DeleteSession(_ context.Context, id string) error {
	b.deletedID = id
	return nil
}

func (b *sessionDeleteBackend) NewSession() {
	b.newCalled = true
}

func TestSessionsPickerDeleteRequiresConfirmation(t *testing.T) {
	b := &sessionDeleteBackend{}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.sessions = sessionsState{
		open: true,
		items: []backend.SessionSummary{
			{ID: "sess-1", Title: "First"},
			{ID: "sess-2", Title: "Second"},
		},
		cursor: 1,
	}

	next, cmd := m.handleSessionsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = next.(model)
	if cmd != nil {
		t.Fatal("delete prompt should not call backend before confirmation")
	}
	if !m.sessions.confirmDelete {
		t.Fatal("expected delete confirmation state")
	}
	if b.deletedID != "" {
		t.Fatalf("deleted before confirmation: %q", b.deletedID)
	}

	next, cmd = m.handleSessionsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = next.(model)
	if cmd == nil {
		t.Fatal("confirming delete should return a command")
	}
	if !m.sessions.loading || m.sessions.confirmDelete {
		t.Fatalf("expected loading without confirmation, loading=%v confirm=%v", m.sessions.loading, m.sessions.confirmDelete)
	}
	msg := cmd()
	if b.deletedID != "sess-2" {
		t.Fatalf("deletedID=%q want sess-2", b.deletedID)
	}
	updated, follow := m.Update(msg)
	if follow != nil {
		t.Fatal("delete result should not schedule follow-up command")
	}
	m = updated.(model)
	if m.sessions.loading {
		t.Fatal("expected sessions loading to clear")
	}
	if len(m.sessions.items) != 1 || m.sessions.items[0].ID != "sess-1" {
		t.Fatalf("unexpected sessions after delete: %#v", m.sessions.items)
	}
	if m.sessions.cursor != 0 {
		t.Fatalf("cursor=%d want 0", m.sessions.cursor)
	}
}

func TestRenderSessionsOverlayPaintsEveryRowBlack(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
	m.theme.NoColor = false
	m.sessions = sessionsState{
		open: true,
		items: []backend.SessionSummary{
			{ID: "sess-1", Title: "First", MessageCount: 2},
			{ID: "sess-2", Title: "Second", MessageCount: 3},
		},
	}

	out := m.renderSessionsOverlay()
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected overlay with multiple lines, got %d: %q", len(lines), stripANSI(out))
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, ansiTrueBlackBG) {
			t.Fatalf("line %d is not explicitly painted black: %q", i, line)
		}
	}
}

func TestSessionsPickerDeleteActiveSessionStartsNewSession(t *testing.T) {
	b := &sessionDeleteBackend{staticBackend: staticBackend{info: backend.Info{SessionID: "sess-1"}}}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.tr.SessionID = "sess-1"
	m.sessions = sessionsState{
		open:  true,
		items: []backend.SessionSummary{{ID: "sess-1", Title: "Active"}},
	}

	updated, _ := m.Update(sessionDeletedMsg{id: "sess-1"})
	m = updated.(model)
	if !b.newCalled {
		t.Fatal("deleting the active session should reset the backend to a new session")
	}
	if len(m.sessions.items) != 0 {
		t.Fatalf("expected deleted session removed, got %#v", m.sessions.items)
	}
}
