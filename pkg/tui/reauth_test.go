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

func TestModelPicker_ModelsLoad403ArmsReauth(t *testing.T) {
	m, _ := newReauthTestModel(t)
	// Simulate an open picker that has drilled into the xai_oauth provider.
	m.modelPicker.open = true
	m.modelPicker.step = "model"
	m.modelPicker.selectedProvider = "grok-subscription"
	m.modelPicker.currentProvider = "grok-subscription"

	// The models list failed with a 403 wrapped as ErrReauthRequired.
	loadErr := fmt.Errorf("%w: models request returned 403 Forbidden: bad-credentials", xai_oauth.ErrReauthRequired)
	next, _ := m.Update(modelModelsLoadedMsg{provider: "grok-subscription", err: loadErr})
	m = next.(model)

	if m.modelPicker.open {
		t.Fatal("expected model picker to close when re-auth is required")
	}
	if m.reauthProvider != "grok-subscription" {
		t.Fatalf("reauthProvider = %q, want grok-subscription", m.reauthProvider)
	}
	if !transcriptContains(m, "Press Enter to re-authenticate") {
		t.Fatalf("expected re-auth prompt in transcript, items=%+v", m.tr.Items)
	}

	// Enter then launches the device-code flow, consistent with the turn path.
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if m.reauthProvider != "" || !m.reauthLaunched {
		t.Fatalf("after Enter: reauthProvider=%q reauthLaunched=%v", m.reauthProvider, m.reauthLaunched)
	}
	if cmd == nil {
		t.Fatal("expected a device-code command after Enter")
	}
	if _, ok := cmd().(xaiOAuthStartedMsg); !ok {
		t.Fatal("expected xaiOAuthStartedMsg from Enter command")
	}
}

func TestModelPicker_ModelsLoadPlainErrorRendersInPicker(t *testing.T) {
	m, _ := newReauthTestModel(t)
	m.modelPicker.open = true
	m.modelPicker.step = "model"
	m.modelPicker.selectedProvider = "grok-subscription"

	next, _ := m.Update(modelModelsLoadedMsg{provider: "grok-subscription", err: fmt.Errorf("network down")})
	m = next.(model)

	if !m.modelPicker.open {
		t.Fatal("expected picker to stay open for a non-reauth error")
	}
	if m.reauthProvider != "" {
		t.Fatalf("reauthProvider = %q, want empty for non-reauth error", m.reauthProvider)
	}
	if !strings.Contains(m.modelPicker.err, "Failed to load models") {
		t.Fatalf("picker err = %q, want 'Failed to load models'", m.modelPicker.err)
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

// TestReauth_SuccessReloadsProvider verifies that after the device-code flow
// completes (providerMutatedMsg add success while reauthLaunched), the backend's
// active provider is rebuilt so the freshly written tokens take effect, instead
// of the running agent keeping its dead in-memory transport and re-prompting.
func TestReauth_SuccessReloadsProvider(t *testing.T) {
	m, b := newReauthTestModel(t)
	// Simulate that Enter already launched the re-auth device-code flow.
	m.reauthLaunched = true

	next, cmd := m.Update(providerMutatedMsg{action: "add", name: "grok-subscription"})
	m = next.(model)

	if m.reauthLaunched {
		t.Fatal("expected reauthLaunched cleared after successful re-auth")
	}
	if cmd == nil {
		t.Fatal("expected a provider-reload command after successful re-auth")
	}
	// The reload command runs the backend rebuild and reports the outcome.
	reloadMsg := cmd()
	if _, ok := reloadMsg.(providerReloadedMsg); !ok {
		t.Fatalf("reload command produced %T, want providerReloadedMsg", reloadMsg)
	}
	if b.reloaded != 1 {
		t.Fatalf("ReloadActiveProvider called %d times, want 1", b.reloaded)
	}

	// Feeding the reload result back renders the final confirmation.
	next, _ = m.Update(reloadMsg)
	m = next.(model)
	if !transcriptContains(m, "xAI OAuth re-authenticated.") {
		t.Fatalf("expected re-authenticated confirmation, items=%+v", m.tr.Items)
	}
}

// TestReauth_ReloadFailureSurfacesError verifies that when the post-re-auth
// provider rebuild fails, the user is told rather than silently looping.
func TestReauth_ReloadFailureSurfacesError(t *testing.T) {
	m, b := newReauthTestModel(t)
	b.reloadErr = fmt.Errorf("rebuild boom")
	m.reauthLaunched = true

	next, cmd := m.Update(providerMutatedMsg{action: "add", name: "grok-subscription"})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected a provider-reload command")
	}
	reloadMsg := cmd()
	next, _ = m.Update(reloadMsg)
	m = next.(model)

	var sawError bool
	for _, it := range m.tr.Items {
		if it.Kind == events.ItemError && strings.Contains(it.Content, "reloading the provider failed") {
			sawError = true
		}
	}
	if !sawError {
		t.Fatalf("expected reload-failure error render, items=%+v", m.tr.Items)
	}
}
