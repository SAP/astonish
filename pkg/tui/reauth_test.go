package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/provider/xai_oauth"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// transcriptContains reports whether any transcript item's content contains sub.
func transcriptContains(m model, sub string) bool {
	for _, it := range m.tr.Items {
		if strings.Contains(it.Content, sub) {
			return true
		}
	}
	return false
}

func newReauthTestModel(t *testing.T) (model, *xaiOAuthStub) {
	t.Helper()
	b := &xaiOAuthStub{}
	m := newModelPickerTestModel(t, b)
	m.info = backend.Info{Provider: "grok-subscription", Model: "grok-3"}
	return m, b
}

func TestTurnErr_XAIReauthPromptsThenLaunches(t *testing.T) {
	m, _ := newReauthTestModel(t)

	// A turn fails with the re-auth sentinel wrapped.
	next, _ := m.Update(turnErrMsg{err: fmt.Errorf("%w: boom", xai_oauth.ErrReauthRequired)})
	m = next.(model)

	if m.reauthProvider != "grok-subscription" {
		t.Fatalf("reauthProvider = %q, want grok-subscription", m.reauthProvider)
	}
	if !transcriptContains(m, "Press Enter to re-authenticate") {
		t.Fatalf("expected re-auth prompt in transcript, items=%+v", m.tr.Items)
	}

	// User returns and presses Enter to launch the device-code flow.
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if m.reauthProvider != "" {
		t.Fatalf("reauthProvider = %q, want cleared after Enter", m.reauthProvider)
	}
	if !m.reauthLaunched {
		t.Fatal("expected reauthLaunched = true after Enter")
	}
	if cmd == nil {
		t.Fatal("expected a device-code command after Enter, got nil")
	}
	msg := cmd()
	if _, ok := msg.(xaiOAuthStartedMsg); !ok {
		t.Fatalf("command produced %T, want xaiOAuthStartedMsg", msg)
	}
}

func TestTurnErr_XAIReauthEscDismisses(t *testing.T) {
	m, _ := newReauthTestModel(t)

	next, _ := m.Update(turnErrMsg{err: fmt.Errorf("%w: boom", xai_oauth.ErrReauthRequired)})
	m = next.(model)
	if m.reauthProvider == "" {
		t.Fatal("expected re-auth prompt pending")
	}

	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(model)
	if m.reauthProvider != "" {
		t.Fatalf("reauthProvider = %q, want cleared after Esc", m.reauthProvider)
	}
	if m.reauthLaunched {
		t.Fatal("expected reauthLaunched = false after Esc")
	}
	if cmd != nil {
		if _, ok := cmd().(xaiOAuthStartedMsg); ok {
			t.Fatal("Esc must not launch the device-code flow")
		}
	}
	if !transcriptContains(m, "dismissed") {
		t.Fatalf("expected dismissal message, items=%+v", m.tr.Items)
	}
}

func TestTurnErr_NonReauthErrorUnchanged(t *testing.T) {
	m, _ := newReauthTestModel(t)

	next, _ := m.Update(turnErrMsg{err: fmt.Errorf("some unrelated failure")})
	m = next.(model)

	if m.reauthProvider != "" {
		t.Fatalf("reauthProvider = %q, want empty for non-reauth error", m.reauthProvider)
	}
	var sawError bool
	for _, it := range m.tr.Items {
		if it.Kind == events.ItemError && strings.Contains(it.Content, "some unrelated failure") {
			sawError = true
		}
	}
	if !sawError {
		t.Fatalf("expected plain error render, items=%+v", m.tr.Items)
	}
}
