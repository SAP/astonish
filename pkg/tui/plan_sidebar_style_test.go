package tui

import (
	"charm.land/lipgloss/v2"
	"strings"
	"testing"
)

func TestPlanSidebarVisualHierarchy(t *testing.T) {
	m := sidebarTestModel(160, 65)
	defer m.cancel()
	m.theme.NoColor = true
	s := m.tr.Items[0].PlanSnapshot
	s.Steps = s.Steps[:3]
	for i := range s.Steps {
		s.Steps[i].Status = "complete"
	}
	s.Document.Results = "verified"
	m.refreshViewport()
	got := stripANSI(m.renderPlanSidebar(52))
	for _, want := range []string{"╭", "╰", "PLAN PROGRESS", "Ctrl+G", "✓ COMPLETED", "Results recorded", "3/3 phases complete", "100%", "━━━", "CURRENT", "PHASES", "✓ Phase 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[===") {
		t.Fatal("legacy ASCII bar remains")
	}
	if lipgloss.Width(got) != 28 || lipgloss.Height(got) != 52 {
		t.Fatalf("bounds %dx%d", lipgloss.Width(got), lipgloss.Height(got))
	}
	t.Log("Tall completed sidebar:\n" + got)
}

func TestPlanSidebarStyledColorAndStates(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	m := sidebarTestModel(140, 65)
	defer m.cancel()
	m.theme = DefaultTheme()
	got := m.renderPlanSidebar(52)
	if !strings.Contains(got, "\x1b[") {
		t.Fatal("colored sidebar has no styled output")
	}
	for _, state := range []planProgressState{planProgressCompleted, planProgressFailed, planProgressActive, planProgressReviewPaused, planProgressAwaitingApproval, planProgressUnavailable} {
		label, _ := m.planPanelStatus(planProgressView{State: state})
		if label == "" {
			t.Fatalf("missing state %v", state)
		}
	}
}
