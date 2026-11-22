package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/tui/events"
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
	for _, want := range []string{"╭", "╰", "PLAN PROGRESS", "Ctrl+G", "✓ COMPLETED", "Results recorded", "3/3 phases complete", "100%", "━━━", "PHASES", "✓ Phase 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, absent := range []string{"[===", "CURRENT", "Current:"} {
		if strings.Contains(got, absent) {
			t.Fatalf("redundant/legacy %q remains:\n%s", absent, got)
		}
	}
	if lipgloss.Width(got) != 28 || lipgloss.Height(got) != 52 {
		t.Fatalf("bounds %dx%d", lipgloss.Width(got), lipgloss.Height(got))
	}
	t.Log("Tall completed sidebar:\n" + got)
}

func TestPlanSidebarHeaderReservesPhaseRows(t *testing.T) {
	m := sidebarTestModel(120, 65)
	defer m.cancel()
	m.theme.NoColor = true
	for _, tc := range []struct{ height, headerRows, oldHeaderRows int }{{18, 9, 10}, {35, 9, 10}, {52, 18, 23}} {
		t.Run(fmt.Sprint(tc.height), func(t *testing.T) {
			header := m.planPanelHeader(24, tc.height)
			for _, absent := range []string{"CURRENT", "Current:", "phase-2", "phase-3"} {
				if strings.Contains(header, absent) {
					t.Fatalf("duplicate current work %q in header: %s", absent, header)
				}
			}
			if got := lipgloss.Height(header); got != tc.headerRows || tc.height-2-got <= tc.height-2-tc.oldHeaderRows {
				t.Fatalf("header rows=%d, want %d and more phase rows than before", got, tc.headerRows)
			}
			if !strings.HasSuffix(header, "PHASES") {
				t.Fatal("counts do not lead to PHASES")
			}
		})
	}
	short := m.renderPlanSidebar(8)
	if strings.Contains(short, "phase-2") || !strings.Contains(short, "1/12 phases complete") || !strings.Contains(short, "Executing") {
		t.Fatalf("short fallback must retain counts/lifecycle only: %s", short)
	}
	for _, text := range []string{m.renderCompactPlanProgress(), m.renderPlanProgressHeader(80)} {
		if !strings.Contains(text, "Current: phase-2, phase-3") {
			t.Fatalf("compact/detail current names lost: %s", text)
		}
	}
}

func TestPlanSidebarStyledColorAndStates(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	m := sidebarTestModel(140, 65)
	defer m.cancel()
	m.theme = DefaultTheme()
	got := m.renderPlanSidebar(52)
	if header := m.planPanelHeader(24, 52); !strings.Contains(header, m.theme.PlanHeader.Render("PLAN PROGRESS")) {
		t.Fatal("sidebar heading bypasses orange PlanHeader role")
	}
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

func TestPlanSidebarDividerMatchesChrome(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		for _, height := range []int{14, 30, 65} {
			for _, approval := range []string{"normal", "plan", "tool"} {
				t.Run(fmt.Sprintf("plain=%v/%d/%s", noColor, height, approval), func(t *testing.T) {
					t.Setenv("NO_COLOR", "")
					if noColor {
						t.Setenv("NO_COLOR", "1")
					}
					m := sidebarTestModel(120, height)
					defer m.cancel()
					m.theme = DefaultTheme()
					if approval == "plan" {
						m.tr.Items[0].PlanStatus = events.PlanPending
						m.tr.Awaiting, m.tr.ApprovalIdx = true, 0
					} else if approval == "tool" {
						m.tr.Items = append(m.tr.Items, events.Item{Kind: events.ItemApproval, ApprovalKind: "tool", Options: []string{"Allow", "Always Allow", "Deny"}})
						m.tr.Awaiting, m.tr.ApprovalIdx = true, len(m.tr.Items)-1
					}
					m.refreshViewport()
					want, _ := sidebarColorCells(m.theme.Border.Width(m.width).Render(strings.Repeat("─", m.width)), true)
					if (want[0][0] == "default") != noColor {
						t.Fatalf("unexpected reference foreground %s", want[0][0])
					}
					bodyH := m.vp.Height() + m.stickyHeaderLines
					for _, text := range []string{m.conversationBody(bodyH), m.View().Content} {
						fg, plain := sidebarColorCells(text, true)
						vertical, horizontal := 0, 0
						for y, line := range plain {
							runes := []rune(line)
							if len(runes) <= m.transcriptWidth() {
								continue
							}
							if runes[m.transcriptWidth()] == '│' {
								vertical++
								if fg[y][m.transcriptWidth()] != want[0][0] {
									t.Fatalf("row %d divider=%s chrome=%s", y, fg[y][m.transcriptWidth()], want[0][0])
								}
							}
							if strings.HasPrefix(line, strings.Repeat("─", m.width)) {
								horizontal++
								if fg[y][m.transcriptWidth()] != want[0][0] {
									t.Fatal("chrome reference differs")
								}
							}
						}
						if vertical != bodyH {
							t.Fatalf("divider rows=%d want=%d", vertical, bodyH)
						}
						if text != m.conversationBody(bodyH) && (horizontal < 1 || approval != "plan" && horizontal < 2) {
							t.Fatal("missing header/footer separators")
						}
					}
				})
			}
		}
	}
}
