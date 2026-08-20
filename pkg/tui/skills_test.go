package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

type skillsTestBackend struct {
	staticBackend
	summaries []backend.SkillSummary
	runTurns  int
}

func (b *skillsTestBackend) ListLocalSkills(context.Context) ([]backend.SkillSummary, error) {
	return b.summaries, nil
}

func (b *skillsTestBackend) RunTurn(context.Context, string, backend.TurnOptions) (<-chan events.Event, error) {
	b.runTurns++
	return nil, nil
}

func TestFormatSkillSummaries(t *testing.T) {
	got := formatSkillSummaries([]backend.SkillSummary{
		{Name: "alpha", Description: "Alpha", Source: "project", Eligible: true},
		{Name: "beta", Description: "Beta", Source: "user", Missing: []string{"docker", "git"}},
	})
	for _, want := range []string{"alpha [project, eligible]", "beta [user, unavailable]", "Missing: docker, git"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted skills missing %q:\n%s", want, got)
		}
	}
}

func TestSkillsCompletionCapabilityGated(t *testing.T) {
	without := newModel(context.Background(), Config{Backend: staticBackend{}})
	without.ta.SetValue("/ski")
	without.syncSlashCompletion()
	if without.slash.active {
		t.Fatalf("platform completion exposed /skills: %+v", without.slash.matches)
	}

	with := newModel(context.Background(), Config{Backend: &skillsTestBackend{}})
	with.ta.SetValue("/ski")
	with.syncSlashCompletion()
	if !with.slash.active || len(with.slash.matches) != 1 || with.slash.matches[0].Name != "skills" {
		t.Fatalf("local completion missing /skills: %+v", with.slash.matches)
	}
}

// TestSkillsPickerOpenAndClose verifies that /skills opens the picker overlay
// (returns a load command), and that pressing Esc closes it.
func TestSkillsPickerOpenAndClose(t *testing.T) {
	local := &skillsTestBackend{summaries: []backend.SkillSummary{
		{Name: "alpha", Description: "Alpha", Source: "project", Eligible: true},
	}}
	m := newModel(context.Background(), Config{Backend: local})

	// /skills should open the picker and return a load command — not run a turn.
	next, cmd := m.handleSlash("/skills")
	got := next.(model)
	if local.runTurns != 0 {
		t.Fatalf("/skills ran a turn: turns=%d", local.runTurns)
	}
	if !got.skillsPicker.open {
		t.Fatalf("skillsPicker should be open after /skills")
	}
	if !got.skillsPicker.loading {
		t.Fatalf("skillsPicker should be loading after /skills")
	}
	if cmd == nil {
		t.Fatalf("expected a load command from /skills, got nil")
	}

	// Simulate receiving the loaded items.
	loaded, cmd2 := got.applySkillsLoaded(skillsLoadedMsg{items: local.summaries})
	got = loaded.(model)
	if cmd2 != nil {
		t.Fatalf("applySkillsLoaded should return nil cmd, got %v", cmd2)
	}
	if got.skillsPicker.loading {
		t.Fatalf("picker should stop loading after items received")
	}
	if len(got.skillsPicker.items) != 1 || got.skillsPicker.items[0].Name != "alpha" {
		t.Fatalf("unexpected items: %+v", got.skillsPicker.items)
	}

	// Esc should close the picker.
	closed, _ := got.handleSkillsPickerKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	gotClosed := closed.(model)
	if gotClosed.skillsPicker.open {
		t.Fatalf("skillsPicker should be closed after Esc")
	}
}

// TestSkillsPickerUnsupportedBackendShowsNotice verifies that on a backend that
// does not implement LocalSkillsBackend, /skills writes the notice into the
// transcript (not the picker).
func TestSkillsPickerUnsupportedBackendShowsNotice(t *testing.T) {
	platform := newModel(context.Background(), Config{Backend: staticBackend{}})
	next, cmd := platform.handleSlash("/skills")
	got := next.(model)
	if got.skillsPicker.open {
		t.Fatalf("picker should not open on unsupported backend")
	}
	if cmd != nil {
		t.Fatalf("unexpected command: %v", cmd)
	}
	if len(got.tr.Items) == 0 || got.tr.Items[len(got.tr.Items)-1].Content != localSkillsOnlyNotice {
		t.Fatalf("unsupported notice = %+v", got.tr.Items)
	}
}

// TestSkillsPickerCursorNavigation verifies up/down key movement.
func TestSkillsPickerCursorNavigation(t *testing.T) {
	local := &skillsTestBackend{summaries: []backend.SkillSummary{
		{Name: "a", Description: "A", Source: "user", Eligible: true},
		{Name: "b", Description: "B", Source: "user", Eligible: true},
		{Name: "c", Description: "C", Source: "user", Eligible: true},
	}}
	m := newModel(context.Background(), Config{Backend: local})
	next, _ := m.handleSlash("/skills")
	m = next.(model)
	next2, _ := m.applySkillsLoaded(skillsLoadedMsg{items: local.summaries})
	m = next2.(model)

	// Initially at 0.
	if m.skillsPicker.cursor != 0 {
		t.Fatalf("initial cursor = %d", m.skillsPicker.cursor)
	}

	// Down twice.
	m2, _ := m.handleSkillsPickerKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m3, _ := m2.(model).handleSkillsPickerKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if m3.(model).skillsPicker.cursor != 2 {
		t.Fatalf("cursor after 2 downs = %d", m3.(model).skillsPicker.cursor)
	}

	// Down past end — stays at max.
	m4, _ := m3.(model).handleSkillsPickerKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if m4.(model).skillsPicker.cursor != 2 {
		t.Fatalf("cursor clamped = %d", m4.(model).skillsPicker.cursor)
	}

	// Up once.
	m5, _ := m4.(model).handleSkillsPickerKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if m5.(model).skillsPicker.cursor != 1 {
		t.Fatalf("cursor after up = %d", m5.(model).skillsPicker.cursor)
	}
}
