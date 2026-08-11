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

func TestRenderSessionsOverlayShowsTitleAndTime(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 120, Height: 30})
	m.theme.NoColor = true // disable ANSI so we can inspect plain text
	m.sessions = sessionsState{
		open: true,
		items: []backend.SessionSummary{
			{ID: "abcd1234-full-id", Title: "Refactor auth module", MessageCount: 5, UpdatedAt: "2025-01-15 10:30"},
			{ID: "efgh5678-full-id", Title: "Fix login bug", MessageCount: 12, UpdatedAt: "2025-01-14 09:15"},
		},
	}

	out := m.renderSessionsOverlay()
	plain := stripANSI(out)

	// Verify title is rendered (not the raw message).
	if !strings.Contains(plain, "Refactor auth module") {
		t.Errorf("expected title 'Refactor auth module' in output:\n%s", plain)
	}
	// Verify timestamp is rendered.
	if !strings.Contains(plain, "2025-01-15 10:30") {
		t.Errorf("expected timestamp '2025-01-15 10:30' in output:\n%s", plain)
	}
	// Verify message count is rendered.
	if !strings.Contains(plain, "5 msgs") {
		t.Errorf("expected '5 msgs' in output:\n%s", plain)
	}
	// Verify session ID is truncated (8 chars).
	if !strings.Contains(plain, "abcd1234") {
		t.Errorf("expected truncated session ID 'abcd1234' in output:\n%s", plain)
	}
	if strings.Contains(plain, "abcd1234-full-id") {
		t.Errorf("session ID should be truncated, but found full ID in output:\n%s", plain)
	}

	// Verify column alignment: both rows' message count and date columns
	// should start at the same visual position (rune offset, not byte offset).
	lines := strings.Split(plain, "\n")
	var dataLines []string
	for _, l := range lines {
		if strings.Contains(l, "msgs") {
			dataLines = append(dataLines, l)
		}
	}
	if len(dataLines) < 2 {
		t.Fatalf("expected at least 2 data lines with 'msgs', got %d in:\n%s", len(dataLines), plain)
	}
	// The "msgs" column should end at the same rune offset in both lines.
	runeIndex := func(s, sub string) int {
		byteIdx := strings.Index(s, sub)
		if byteIdx < 0 {
			return -1
		}
		return len([]rune(s[:byteIdx]))
	}
	pos0 := runeIndex(dataLines[0], "msgs")
	pos1 := runeIndex(dataLines[1], "msgs")
	if pos0 != pos1 {
		t.Errorf("column misalignment: 'msgs' at rune position %d in row 0 vs %d in row 1:\n  %s\n  %s",
			pos0, pos1, dataLines[0], dataLines[1])
	}
	// The date column should also be aligned.
	datePos0 := runeIndex(dataLines[0], "2025-01-1")
	datePos1 := runeIndex(dataLines[1], "2025-01-1")
	if datePos0 != datePos1 {
		t.Errorf("column misalignment: date at rune position %d in row 0 vs %d in row 1:\n  %s\n  %s",
			datePos0, datePos1, dataLines[0], dataLines[1])
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

// modelAwareBackend updates Info().Provider/Model on NewSession and ResumeSession
// the way platformBackend does after model-status refresh.
type modelAwareBackend struct {
	staticBackend
	bySession map[string]backend.Info
	cascade   backend.Info
}

func (b *modelAwareBackend) NewSession() {
	b.info = b.cascade
	b.info.SessionID = ""
	b.info.IsResumed = false
}

func (b *modelAwareBackend) ResumeSession(_ context.Context, sessionID string) ([]backend.HistoryEntry, error) {
	if info, ok := b.bySession[sessionID]; ok {
		b.info = info
		b.info.SessionID = sessionID
		b.info.IsResumed = true
	}
	return []backend.HistoryEntry{{Kind: "user", Text: "hi"}}, nil
}

func TestStartNewSessionRefreshesFooterModelToCascade(t *testing.T) {
	b := &modelAwareBackend{
		staticBackend: staticBackend{info: backend.Info{
			SessionID: "sess-custom",
			Provider:  "openai",
			Model:     "gpt-4o",
		}},
		cascade: backend.Info{Provider: "cascade-provider", Model: "cascade-model"},
	}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.info = b.info

	next, _ := m.startNewSession()
	m = next.(model)
	if m.info.Provider != "cascade-provider" || m.info.Model != "cascade-model" {
		t.Fatalf("footer after /new = %s/%s, want cascade-provider/cascade-model", m.info.Provider, m.info.Model)
	}
	if m.info.SessionID != "" {
		t.Fatalf("sessionID after /new = %q", m.info.SessionID)
	}
}

func TestApplyHistoryUsesBackendInfoModelAfterResume(t *testing.T) {
	b := &modelAwareBackend{
		staticBackend: staticBackend{info: backend.Info{Provider: "cascade-provider", Model: "cascade-model"}},
		bySession: map[string]backend.Info{
			"sess-a": {Provider: "openai", Model: "gpt-4o"},
			"sess-b": {Provider: "anthropic", Model: "claude-sonnet-4"},
		},
	}

	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	// Stale footer from cascade/default session.
	m.info = backend.Info{Provider: "cascade-provider", Model: "cascade-model"}

	// ResumeSession (as the sessions picker cmd does) refreshes backend model first.
	cmd := m.resumeSessionCmd("sess-a")
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(model)
	if m.info.Provider != "openai" || m.info.Model != "gpt-4o" {
		t.Fatalf("footer after resume A = %s/%s, want openai/gpt-4o", m.info.Provider, m.info.Model)
	}
	if m.info.SessionID != "sess-a" {
		t.Fatalf("sessionID=%q", m.info.SessionID)
	}

	// Switch to another session with a different pin.
	cmd = m.resumeSessionCmd("sess-b")
	msg = cmd()
	next, _ = m.Update(msg)
	m = next.(model)
	if m.info.Provider != "anthropic" || m.info.Model != "claude-sonnet-4" {
		t.Fatalf("footer after resume B = %s/%s, want anthropic/claude-sonnet-4", m.info.Provider, m.info.Model)
	}

	// Back to the custom-model session must not keep B's model.
	cmd = m.resumeSessionCmd("sess-a")
	msg = cmd()
	next, _ = m.Update(msg)
	m = next.(model)
	if m.info.Provider != "openai" || m.info.Model != "gpt-4o" {
		t.Fatalf("footer after resume A again = %s/%s, want openai/gpt-4o", m.info.Provider, m.info.Model)
	}
}

func TestApplyHistorySetsTitleFromBackendInfo(t *testing.T) {
	b := &modelAwareBackend{
		staticBackend: staticBackend{info: backend.Info{Provider: "openai", Model: "gpt-4o"}},
		bySession: map[string]backend.Info{
			"sess-titled": {Provider: "openai", Model: "gpt-4o", Title: "Fix broken tests"},
			"sess-notitle": {Provider: "anthropic", Model: "claude-sonnet-4"},
		},
	}

	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})

	// Resume session with a title — header should show it.
	cmd := m.resumeSessionCmd("sess-titled")
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(model)
	if m.tr.Title != "Fix broken tests" {
		t.Fatalf("tr.Title after resume = %q, want %q", m.tr.Title, "Fix broken tests")
	}

	// Resume session without a title — header title should be empty.
	cmd = m.resumeSessionCmd("sess-notitle")
	msg = cmd()
	next, _ = m.Update(msg)
	m = next.(model)
	if m.tr.Title != "" {
		t.Fatalf("tr.Title after resume (no title) = %q, want empty", m.tr.Title)
	}
}
