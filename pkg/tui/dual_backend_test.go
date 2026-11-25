package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/SAP/astonish/pkg/tui/render"
)

type closeRecordingBackend struct {
	staticBackend
	closes int
	err    error
}

func (b *closeRecordingBackend) Close() error { b.closes++; return b.err }

func TestFinalModelClosesOpenedBackends(t *testing.T) {
	primary := &closeRecordingBackend{staticBackend: staticBackend{info: backend.Info{Mode: "code"}}}
	failure := errors.New("shutdown failed")
	alt := &closeRecordingBackend{staticBackend: staticBackend{info: backend.Info{Mode: "platform"}}, err: failure}
	initial := newModel(context.Background(), Config{Backend: primary, AltBackend: alt})
	final := initial
	final.backends[1].opened = true
	if err := final.closeBackends(); !errors.Is(err, failure) {
		t.Fatalf("cleanup error lost: %v", err)
	}
	if primary.closes != 1 || alt.closes != 1 {
		t.Fatalf("cleanup counts: %d %d", primary.closes, alt.closes)
	}
	if err := final.closeBackends(); err != nil {
		t.Fatal(err)
	}
	if primary.closes != 1 || alt.closes != 1 {
		t.Fatal("closed twice")
	}
	if initial.backends[1].opened {
		t.Fatal("initial model unexpectedly tracked alternate open")
	}
}

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

func TestCodeThemeBackendSlots(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	for _, primary := range []string{"code", "platform"} {
		alt := "platform"
		if primary == "platform" {
			alt = "code"
		}
		m := newModel(context.Background(), Config{ThemeID: CodeThemeBlueGray, Backend: staticBackend{info: backend.Info{Mode: primary}}, AltBackend: staticBackend{info: backend.Info{Mode: alt}}})
		defer m.cancel()
		for i, mode := range []string{primary, alt} {
			want := ThemeForMode(mode)
			if m.backends[i].theme.CodeThemeID != want.CodeThemeID || !reflect.DeepEqual(m.backends[i].theme.Background.GetBackground(), want.Background.GetBackground()) {
				t.Fatalf("slot %d (%s) changed palette", i, mode)
			}
			if mode == "platform" && m.backends[i].theme.RenderStyles().TableLayout != render.TableLayoutLegacy {
				t.Fatal("Code selection enabled framed Platform tables")
			}
		}
		result, _ := m.switchBackend()
		next := result.(model)
		want := ThemeForMode(alt)
		if next.theme.CodeThemeID != want.CodeThemeID || !reflect.DeepEqual(next.theme.Background.GetBackground(), want.Background.GetBackground()) {
			t.Fatal("backend switch lost the slot palette")
		}
	}
	if err := Run(context.Background(), Config{ThemeID: "invalid"}); err == nil || !strings.Contains(err.Error(), "available: blue-gray") {
		t.Fatalf("Run must validate before backend open: %v", err)
	}
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

func TestCtrlTabSwitchClearsPreviousBackendFrame(t *testing.T) {
	m := newDualModeModel(t)
	m.tr.Apply(events.NewSystem("platform content that must not remain behind the next welcome screen"))

	_, cmd := m.switchBackend()
	if cmd == nil {
		t.Fatal("switchBackend() must request a full-screen clear")
	}
	got := cmd()
	want := tea.ClearScreen()
	if reflect.TypeOf(got) != reflect.TypeOf(want) {
		t.Fatalf("switchBackend() command = %T, want %T", got, want)
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

func TestShiftTabTogglesNormalPlanInPlatformMode(t *testing.T) {
	m := newDualModeModel(t)

	// Switch to platform mode.
	result, _ := m.switchBackend()
	nm := result.(model)

	if nm.info.Mode != "platform" {
		t.Fatal("expected platform mode")
	}

	// Initial state: Normal.
	if nm.planMode || nm.graphPlanMode {
		t.Fatal("expected Normal mode initially")
	}

	// First toggle → Plan.
	nm.togglePlanMode()
	if !nm.planMode {
		t.Fatal("expected planMode=true after first toggle")
	}
	if nm.graphPlanMode {
		t.Fatal("graphPlanMode should never activate in platform mode")
	}

	// Second toggle → back to Normal (no Graph Plan in between).
	nm.togglePlanMode()
	if nm.planMode {
		t.Fatal("expected planMode=false after second toggle")
	}
	if nm.graphPlanMode {
		t.Fatal("graphPlanMode should never activate in platform mode")
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

	// Platform + Plan mode.
	nm.planMode = true
	label = nm.composerModeLabel()
	if label != "Platform · Plan" {
		t.Fatalf("expected 'Platform · Plan', got %q", label)
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
