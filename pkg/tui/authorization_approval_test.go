package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

// subAgentAuthBackend simulates a backend where the current approval overlay
// was raised by a blocked sub-agent: RespondSubAgentAuth reports the choice was
// consumed (returns true), so submitApproval must NOT call RunTurn.
type subAgentAuthBackend struct {
	authTestBackend
	respondedWith string
	responded     bool
}

func (b *subAgentAuthBackend) RespondSubAgentAuth(choice string) bool {
	b.responded = true
	b.respondedWith = choice
	return true
}

// TestSubmitApproval_SubAgentPathDoesNotCallRunTurn is the TUI-side regression
// guard for the freeze: when the approval belongs to a sub-agent, the decision
// is delivered via RespondSubAgentAuth and RunTurn must NOT be invoked (the
// sub-agent resumes the still-running parent turn on its own).
func TestSubmitApproval_SubAgentPathDoesNotCallRunTurn(t *testing.T) {
	b := &subAgentAuthBackend{}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	// Simulate an active event channel (the parent turn is still running while
	// the sub-agent is blocked awaiting authorization).
	live := make(chan events.Event)
	m.eventCh = live
	m.tr.Apply(events.NewAuthorizationApproval(
		"shell_command",
		map[string]any{"command": "ls"},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))

	next, cmd := m.submitApproval("Allow")
	m2 := next.(model)

	if !b.responded {
		t.Fatal("expected RespondSubAgentAuth to be called")
	}
	if b.respondedWith != "Allow" {
		t.Fatalf("respondedWith=%q want %q", b.respondedWith, "Allow")
	}
	if b.called {
		t.Fatal("RunTurn must NOT be called on the sub-agent approval path (freeze regression)")
	}
	if m2.tr.Awaiting {
		t.Fatal("approval overlay should be cleared after sub-agent response")
	}
	if cmd == nil {
		t.Fatal("expected a command to resume listening on the existing event channel")
	}
}

// TestSubmitApproval_MainThreadPathCallsRunTurn verifies the complementary
// case: when no sub-agent is blocked (RespondSubAgentAuth returns false),
// submitApproval drives the decision through RunTurn(choice) as before.
func TestSubmitApproval_MainThreadPathCallsRunTurn(t *testing.T) {
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

	_, cmd := m.submitApproval("Allow")
	if cmd == nil {
		t.Fatal("expected a command from submitApproval")
	}
	cmd()
	if !b.called {
		t.Fatal("RunTurn must be called on the main-thread approval path")
	}
	if b.runMsg != "Allow" {
		t.Fatalf("runMsg=%q want %q", b.runMsg, "Allow")
	}
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
		"move",         // cursor hint
		"select",       // enter hint
		"command",      // arg key shown
		"rm -rf build", // arg value shown
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

// TestRenderToolAuthorizationOverlay_LongCommand verifies that a long or
// multiline shell command is displayed across multiple lines rather than being
// hard-cut at 60 bytes on a single line.
func TestRenderToolAuthorizationOverlay_LongCommand(t *testing.T) {
	longCmd := "git log --oneline --graph --decorate --all --color=always | head -50 && echo done"
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 40})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewAuthorizationApproval(
		"shell_command",
		map[string]any{"command": longCmd},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))

	out := stripANSI(m.renderApprovalOverlay())

	// The full command must not be silently swallowed — at minimum the opening
	// portion should be visible and the end should appear somewhere.
	if !strings.Contains(out, "git log") {
		t.Errorf("overlay missing start of long command:\n%s", out)
	}
	// At width=100 the card is wide enough to show the whole command; it must
	// NOT be cut at 60 chars (old behaviour would lose "| head -50 && echo done").
	if !strings.Contains(out, "head -50") {
		t.Errorf("overlay cut long command too early (old 60-char truncation?): %q\n%s", longCmd, out)
	}
}

// TestRenderToolAuthorizationOverlay_MultilineCommand verifies that a command
// containing real newlines (e.g. a shell script) is displayed across multiple
// visual lines instead of being collapsed to a single run-on line.
func TestRenderToolAuthorizationOverlay_MultilineCommand(t *testing.T) {
	script := "set -e\ncd /app\nnpm ci\nnpm run build\nnpm test"
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 50})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewAuthorizationApproval(
		"shell_command",
		map[string]any{"command": script},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))

	out := stripANSI(m.renderApprovalOverlay())

	// Each line of the script should be visible.
	for _, line := range []string{"set -e", "cd /app", "npm ci", "npm run build", "npm test"} {
		if !strings.Contains(out, line) {
			t.Errorf("overlay missing script line %q:\n%s", line, out)
		}
	}
	// Must be rendered across multiple lines — the newlines must NOT be squashed.
	if strings.Contains(out, "set -e cd /app") || strings.Contains(out, "set -encd /app") {
		t.Errorf("overlay collapsed multiline command to a single line:\n%s", out)
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
	next, cmd, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: '2', Text: "2"})
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
	next, _, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if !handled {
		t.Fatal("expected down handled")
	}
	m = next.(model)
	if m.tr.ApprovalCursor != 1 {
		t.Fatalf("cursor after down=%d want 1", m.tr.ApprovalCursor)
	}

	// Enter → submit the highlighted option ("Always Allow").
	next, cmd, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyEnter})
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

	next, cmd, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyEnter})
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

	next, cmd, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyEsc})
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

func TestApprovalOverlay_StableArgOrder(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewAuthorizationApproval(
		"shell_command",
		map[string]any{"command": "echo hi", "working_dir": "/tmp", "timeout": 30},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))

	// Render multiple times and verify the output is identical each time.
	first := m.renderApprovalOverlay()
	for i := 0; i < 20; i++ {
		got := m.renderApprovalOverlay()
		if got != first {
			t.Fatalf("render %d produced different output (parameter order instability):\nfirst:\n%s\n\ngot:\n%s", i, first, got)
		}
	}

	// Verify sorted order: command < timeout < working_dir (alphabetical).
	out := stripANSI(first)
	cmdIdx := strings.Index(out, "command:")
	timeoutIdx := strings.Index(out, "timeout:")
	wdIdx := strings.Index(out, "working_dir:")
	if cmdIdx < 0 || timeoutIdx < 0 || wdIdx < 0 {
		t.Fatalf("expected all three arg keys in output:\n%s", out)
	}
	if !(cmdIdx < timeoutIdx && timeoutIdx < wdIdx) {
		t.Errorf("args not in alphabetical order: command@%d, timeout@%d, working_dir@%d", cmdIdx, timeoutIdx, wdIdx)
	}
}
