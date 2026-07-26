package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

type networkGrantTestBackend struct {
	staticBackend
	approved bool
	broader  bool
	denied   bool
	runMsg   string
}

func (b *networkGrantTestBackend) RunTurn(_ context.Context, message string, _ backend.TurnOptions) (<-chan events.Event, error) {
	b.runMsg = message
	ch := make(chan events.Event)
	close(ch)
	return ch, nil
}

func (b *networkGrantTestBackend) ApproveNetworkGrant(_ context.Context, sessionID string, denial events.NetworkDenial, broader bool, sandboxName string) error {
	b.approved = sessionID == "sess-1" && denial.Host == "api.example.com" && sandboxName == "sandbox-1"
	b.broader = broader
	return nil
}

func (b *networkGrantTestBackend) DenyNetworkGrant(_ context.Context, sessionID string, denial events.NetworkDenial, sandboxName string) error {
	b.denied = sessionID == "sess-1" && denial.Host == "api.example.com" && sandboxName == "sandbox-1"
	return nil
}

func TestRenderNetworkDenialOverlay(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewNetworkDenial("sess-1", "sandbox-1", []events.NetworkDenial{{
		Host:           "api.example.com",
		Port:           443,
		Binary:         "/usr/bin/curl",
		BroaderPattern: "*.example.com",
	}}))

	out := stripANSI(m.renderApprovalOverlay())
	for _, want := range []string{"Network access blocked", "api.example.com:443", "/usr/bin/curl", "b=allow *.example.com", "n/esc=deny"} {
		if !strings.Contains(out, want) {
			t.Fatalf("overlay missing %q:\n%s", want, out)
		}
	}
}

func TestNetworkDenialApproveClearsPromptBeforeGrantAndRetries(t *testing.T) {
	b := &networkGrantTestBackend{staticBackend: staticBackend{info: backend.Info{SessionID: "sess-1"}}}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.SessionID = "sess-1"
	m.tr.Apply(events.NewNetworkDenial("sess-1", "sandbox-1", []events.NetworkDenial{{
		Host:           "api.example.com",
		Port:           443,
		BroaderPattern: "*.example.com",
	}}))

	next, cmd, handled := m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled {
		t.Fatal("expected enter to approve network grant")
	}
	if cmd == nil {
		t.Fatal("expected async approval command")
	}
	m2 := next.(model)
	if m2.tr.Awaiting {
		t.Fatal("network prompt should be cleared immediately after approval key")
	}
	if !m2.tr.Streaming || m2.tr.Status != "Approving network access…" {
		t.Fatalf("expected approval progress state, streaming=%v status=%q", m2.tr.Streaming, m2.tr.Status)
	}
	if b.approved || b.runMsg != "" {
		t.Fatalf("grant should not run before returned command, approved=%v runMsg=%q", b.approved, b.runMsg)
	}

	msg := cmd()
	approvedMsg, ok := msg.(networkGrantApprovedMsg)
	if !ok {
		t.Fatalf("expected networkGrantApprovedMsg, got %T", msg)
	}
	if approvedMsg.err != nil {
		t.Fatalf("approval command returned error: %v", approvedMsg.err)
	}
	if !b.approved || b.broader {
		t.Fatalf("approval flags approved=%v broader=%v", b.approved, b.broader)
	}
	if !strings.Contains(b.runMsg, "I just approved network access to api.example.com") {
		t.Fatalf("retry message = %q", b.runMsg)
	}

	updated, followCmd := m2.Update(msg)
	m3 := updated.(model)
	if !m3.tr.Streaming || m3.tr.Status != "Thinking…" {
		t.Fatalf("expected retry stream state, streaming=%v status=%q", m3.tr.Streaming, m3.tr.Status)
	}
	if followCmd == nil {
		t.Fatal("expected command waiting on retry event stream")
	}
}

func TestNetworkDenialBroaderApprove(t *testing.T) {
	b := &networkGrantTestBackend{staticBackend: staticBackend{info: backend.Info{SessionID: "sess-1"}}}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.SessionID = "sess-1"
	m.tr.Apply(events.NewNetworkDenial("sess-1", "sandbox-1", []events.NetworkDenial{{Host: "api.example.com", Port: 443, BroaderPattern: "*.example.com"}}))

	next, cmd, handled := m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if !handled {
		t.Fatal("expected b to approve broader grant")
	}
	if cmd == nil {
		t.Fatal("expected async approval command")
	}
	m2 := next.(model)
	if m2.tr.Awaiting {
		t.Fatal("network prompt should be cleared immediately after broader approval key")
	}
	if b.approved || b.runMsg != "" {
		t.Fatalf("grant should not run before returned command, approved=%v runMsg=%q", b.approved, b.runMsg)
	}

	msg := cmd()
	approvedMsg, ok := msg.(networkGrantApprovedMsg)
	if !ok {
		t.Fatalf("expected networkGrantApprovedMsg, got %T", msg)
	}
	if approvedMsg.err != nil {
		t.Fatalf("approval command returned error: %v", approvedMsg.err)
	}
	if !b.approved || !b.broader {
		t.Fatalf("approval flags approved=%v broader=%v", b.approved, b.broader)
	}
	if !strings.Contains(b.runMsg, "*.example.com") {
		t.Fatalf("retry message should mention broader pattern, got %q", b.runMsg)
	}
}

func TestNetworkDenialDenyClearsPromptBeforeGrantBackend(t *testing.T) {
	b := &networkGrantTestBackend{staticBackend: staticBackend{info: backend.Info{SessionID: "sess-1"}}}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.SessionID = "sess-1"
	m.tr.Apply(events.NewNetworkDenial("sess-1", "sandbox-1", []events.NetworkDenial{{Host: "api.example.com", Port: 443}}))

	next, cmd, handled := m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if !handled {
		t.Fatal("expected n to deny network grant")
	}
	if cmd == nil {
		t.Fatal("expected async deny command")
	}
	m2 := next.(model)
	if m2.tr.Awaiting || m2.tr.Streaming {
		t.Fatalf("prompt should be cleared and not streaming immediately: awaiting=%v streaming=%v", m2.tr.Awaiting, m2.tr.Streaming)
	}
	if b.denied {
		t.Fatal("deny should not run before returned command")
	}

	msg := cmd()
	deniedMsg, ok := msg.(networkGrantDeniedMsg)
	if !ok {
		t.Fatalf("expected networkGrantDeniedMsg, got %T", msg)
	}
	if deniedMsg.err != nil {
		t.Fatalf("deny command returned error: %v", deniedMsg.err)
	}
	if !b.denied {
		t.Fatal("expected deny backend call")
	}
}
