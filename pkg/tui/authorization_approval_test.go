package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// authTestBackend captures the message RunTurn is called with so tests can
// assert the option string the approval overlay submits back to the agent.
type authTestBackend struct {
	staticBackend
	runMsg string
	called bool
}

func (b *authTestBackend) RunTurn(_ context.Context, message string, _ backend.TurnOptions) (<-chan events.Event, error) {
	b.called = true
	b.runMsg = message
	ch := make(chan events.Event)
	close(ch)
	return ch, nil
}

func TestRenderToolAuthorizationOverlay(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewAuthorizationApproval(
		"shell_command",
		map[string]any{"command": "rm -rf build"},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))

	out := stripANSI(m.renderApprovalOverlay())
	for _, want := range []string{
		"Tool authorization required",
		"shell_command",
		"Allow",
		"Always Allow",
		"Deny",
		"move",   // cursor hint
		"select", // enter hint
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay missing %q:\n%s", want, out)
		}
	}
	// The default cursor sits on the first option ("Allow"), shown with a caret.
	if !strings.Contains(out, "❯ Allow") {
		t.Errorf("overlay should highlight the default (first) option with a caret:\n%s", out)
	}
}

func TestRenderFolderAuthorizationOverlay(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewAuthorizationApproval(
		"read_file",
		map[string]any{"path": "/etc/hosts"},
		[]string{"Allow", "Always Allow", "Deny"},
		"folder", []string{"/etc/hosts"},
	))

	out := stripANSI(m.renderApprovalOverlay())
	for _, want := range []string{
		"Folder access required",
		"read_file",
		"/etc/hosts",
		"Allow",
		"Always Allow",
		"Deny",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("folder overlay missing %q:\n%s", want, out)
		}
	}
}

func TestAuthorizationKey_SubmitsOptionByNumber(t *testing.T) {
	b := &authTestBackend{}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewAuthorizationApproval(
		"write_file",
		map[string]any{"file_path": "x.go"},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))

	// Number keys remain accelerators: "2" submits the second option.
	next, cmd, handled := m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if !handled {
		t.Fatal("expected key handled")
	}
	if cmd == nil {
		t.Fatal("expected a command from submitApproval")
	}
	m2 := next.(model)
	if m2.tr.Awaiting {
		t.Fatal("approval should be cleared after submit")
	}
	// Execute the command to drive RunTurn.
	cmd()
	if !b.called {
		t.Fatal("expected RunTurn called")
	}
	if b.runMsg != "Always Allow" {
		t.Fatalf("runMsg=%q want %q", b.runMsg, "Always Allow")
	}
}

// TestAuthorizationKey_CursorEnter verifies the primary interaction: arrow keys
// move the cursor and Enter submits the highlighted option.
func TestAuthorizationKey_CursorEnter(t *testing.T) {
	b := &authTestBackend{}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewAuthorizationApproval(
		"write_file",
		map[string]any{"file_path": "x.go"},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))

	// Default cursor is on the first option ("Allow").
	if m.tr.ApprovalCursor != 0 {
		t.Fatalf("default cursor=%d want 0", m.tr.ApprovalCursor)
	}

	// Down once → "Always Allow" (index 1).
	next, _, handled := m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyDown})
	if !handled {
		t.Fatal("expected down handled")
	}
	m = next.(model)
	if m.tr.ApprovalCursor != 1 {
		t.Fatalf("cursor after down=%d want 1", m.tr.ApprovalCursor)
	}

	// Enter → submit the highlighted option ("Always Allow").
	next, cmd, handled := m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled || cmd == nil {
		t.Fatal("expected enter handled with a command")
	}
	m2 := next.(model)
	if m2.tr.Awaiting {
		t.Fatal("approval should be cleared after submit")
	}
	cmd()
	if b.runMsg != "Always Allow" {
		t.Fatalf("runMsg=%q want %q", b.runMsg, "Always Allow")
	}
}

// TestAuthorizationKey_EnterDefaultAllows verifies that a bare Enter (no
// navigation) submits the safe default (first option, "Allow").
func TestAuthorizationKey_EnterDefaultAllows(t *testing.T) {
	b := &authTestBackend{}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewAuthorizationApproval(
		"shell_command",
		map[string]any{"command": "go build ./..."},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))

	next, cmd, handled := m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled || cmd == nil {
		t.Fatal("expected enter handled with a command")
	}
	_ = next
	cmd()
	if b.runMsg != "Allow" {
		t.Fatalf("runMsg=%q want %q (Enter should accept the default)", b.runMsg, "Allow")
	}
}

func TestAuthorizationKey_DenyViaEsc(t *testing.T) {
	b := &authTestBackend{}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewAuthorizationApproval(
		"shell_command",
		map[string]any{"command": "ls"},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))

	next, cmd, handled := m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled || cmd == nil {
		t.Fatal("expected esc handled with a command")
	}
	m2 := next.(model)
	if m2.tr.Awaiting {
		t.Fatal("approval should be cleared after deny")
	}
	cmd()
	if b.runMsg != "Deny" {
		t.Fatalf("runMsg=%q want Deny", b.runMsg)
	}
}
