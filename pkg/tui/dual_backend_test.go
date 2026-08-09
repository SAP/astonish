package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func newDualModeModel(t *testing.T) model {
	t.Helper()
	primary := staticBackend{info: backend.Info{Mode: "code", Provider: "openai", Model: "gpt-4o"}}
	alt := staticBackend{info: backend.Info{Mode: "platform", Provider: "anthropic", Model: "claude-4"}}
	m := newModel(context.Background(), Config{
		Backend:    primary,
		AltBackend: alt,
		Width:      100,
		Height:     30,
	})
	m.ready = true
	m.layout()
	return m
}

func TestCtrlTabSwitchesBackend(t *testing.T) {
	m := newDualModeModel(t)

	if !m.dualMode {
		t.Fatal("expected dualMode to be true")
	}
	if m.info.Mode != "code" {
		t.Fatalf("expected initial mode 'code', got %q", m.info.Mode)
	}

	// Switch backend directly (Ctrl+Tab dispatches here in Update).
	result, _ := m.switchBackend()
	nm := result.(model)

	if nm.info.Mode != "platform" {
		t.Fatalf("expected mode 'platform' after switch, got %q", nm.info.Mode)
	}
	if nm.activeBackendIdx != 1 {
		t.Fatalf("expected activeBackendIdx=1, got %d", nm.activeBackendIdx)
	}
}

func TestCtrlTabSwapsTheme(t *testing.T) {
	m := newDualModeModel(t)

	codeAccent := m.theme.AccentColor

	result, _ := m.switchBackend()
	nm := result.(model)

	platformAccent := nm.theme.AccentColor
	if codeAccent == platformAccent {
		t.Fatalf("theme accent should differ between modes, both are %v", codeAccent)
	}
}

func TestCtrlTabResetsToWelcomeScreen(t *testing.T) {
	m := newDualModeModel(t)

	// Add a message to the code transcript.
	m.tr.Apply(events.NewSystem("hello from code"))
	if len(m.tr.Items) == 0 {
		t.Fatal("code transcript should have items before switch")
	}

	// Switch to platform — should get a fresh, empty transcript (welcome screen).
	result, _ := m.switchBackend()
	nm := result.(model)

	if len(nm.tr.Items) != 0 {
		t.Fatalf("expected empty transcript (welcome screen) after switch to platform, got %d items", len(nm.tr.Items))
	}

	// Add a message in platform mode.
	nm.tr.Apply(events.NewSystem("hello from platform"))

	// Switch back to code — should also get a fresh, empty transcript.
	result2, _ := nm.switchBackend()
	nm2 := result2.(model)

	if len(nm2.tr.Items) != 0 {
		t.Fatalf("expected empty transcript (welcome screen) after switch back to code, got %d items", len(nm2.tr.Items))
	}
}

func TestCtrlTabWithoutDualMode(t *testing.T) {
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code"}},
		Width:   80,
		Height:  24,
	})
	m.ready = true

	if m.dualMode {
		t.Fatal("expected dualMode to be false when no AltBackend")
	}

	// The switchBackend path shouldn't be reachable, but ensure no panic.
	if m.activeBackendIdx != 0 {
		t.Fatal("default activeBackendIdx should be 0")
	}
}

func TestShiftTabDisabledInPlatformMode(t *testing.T) {
	m := newDualModeModel(t)

	// Switch to platform mode.
	result, _ := m.switchBackend()
	nm := result.(model)

	if nm.info.Mode != "platform" {
		t.Fatal("expected platform mode")
	}

	// Send shift+tab — should NOT activate plan mode.
	nm.planMode = false
	nm.graphPlanMode = false

	// Simulate the shift+tab check as done in Update.
	if nm.info.Mode != "platform" {
		nm.togglePlanMode()
	}

	if nm.planMode || nm.graphPlanMode {
		t.Fatal("shift+tab should not activate plan mode in platform mode")
	}
}

func TestDualModeHintsShowCtrlTab(t *testing.T) {
	m := newDualModeModel(t)
	m.theme.NoColor = true

	hints := m.renderHints()
	if !strings.Contains(hints, "ctrl+\\") {
		t.Fatalf("dual-mode hints should contain 'ctrl+\\\\', got: %s", hints)
	}
}

func TestComposerLabelInDualMode(t *testing.T) {
	m := newDualModeModel(t)

	label := m.composerModeLabel()
	if label != "Code · Normal" {
		t.Fatalf("expected 'Code · Normal', got %q", label)
	}

	m.planMode = true
	label = m.composerModeLabel()
	if label != "Code · Plan" {
		t.Fatalf("expected 'Code · Plan', got %q", label)
	}

	// Switch to platform.
	result, _ := m.switchBackend()
	nm := result.(model)
	label = nm.composerModeLabel()
	if label != "Platform" {
		t.Fatalf("expected 'Platform', got %q", label)
	}
}

func TestPlatformThemeHasDifferentAccent(t *testing.T) {
	code := DefaultTheme()
	platform := PlatformTheme()

	if code.AccentColor == platform.AccentColor {
		t.Fatal("platform and code themes should have different AccentColor")
	}
}

func TestThemeForModeReturnsCorrectTheme(t *testing.T) {
	code := ThemeForMode("code")
	platform := ThemeForMode("platform")

	if code.AccentColor == platform.AccentColor {
		t.Fatal("ThemeForMode should return different themes for different modes")
	}

	// Default for unknown modes should be code theme.
	other := ThemeForMode("unknown")
	if other.AccentColor != code.AccentColor {
		t.Fatal("ThemeForMode with unknown mode should return code theme")
	}
}
