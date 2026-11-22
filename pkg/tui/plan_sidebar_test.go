package tui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestPlanProgressConcisePhasePresentation(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprintf("restored=%v", restored), func(t *testing.T) {
			m := sidebarTestModel(160, 65)
			defer m.cancel()
			m.theme.NoColor = true
			s := m.tr.Items[0].PlanSnapshot.Clone()
			s.Steps = []agent.PlanStepInfo{
				{Name: "internal-complete", Description: "Prepare environment", Status: "complete", ParallelGroup: "hidden-wave"},
				{Name: "internal-running", Description: "Build capability", Status: "running", ParallelGroup: "hidden-wave"},
				{Name: "internal-pending", Description: "Verify capability", Status: "pending"},
				{Name: "internal-failed", Description: "Repair capability", Status: "failed", Summary: "Meaningful failure summary", Evidence: "exit 1: useful evidence"},
				{Name: "internal-unknown", Description: "Inspect capability", Status: "unexpected"},
				{Name: "Fallback title", Status: "pending"},
				{Name: "internal-duplicate", Description: "Verify capability", Status: "pending"},
			}
			m.tr = events.NewTranscript()
			ev := events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s}
			if restored {
				m.tr.LoadHistory([]events.HistoryMsg{{Delegation: &ev}})
			} else {
				m.tr.Apply(ev)
				m.tr.Streaming = true
			}
			m.refreshViewport()
			for _, text := range []string{m.renderPlanPhaseCards(80), m.renderPlanProgressBody(80, false), m.renderPlanProgressBody(80, true)} {
				for _, want := range []string{"✓ Prepare environment", "● Build capability", "○ Verify capability", "✗ Repair capability", "? Inspect capability", "○ Fallback title"} {
					if !strings.Contains(text, want) {
						t.Errorf("missing %q:\n%s", want, text)
					}
				}
				for _, absent := range []string{"internal-", "hidden-wave", "Group:", "pending", "running", "complete", "failed", "unexpected", " · live"} {
					if strings.Contains(text, absent) {
						t.Errorf("redundant %q:\n%s", absent, text)
					}
				}
				if strings.Contains(text, "recorded, not live") != restored || strings.Count(text, "Verify capability") != 2 {
					t.Errorf("restore/duplicate title contract changed:\n%s", text)
				}
			}
			full := m.renderPlanProgressBody(80, true)
			if !strings.Contains(full, "Meaningful failure summary") || !strings.Contains(full, "Evidence: exit 1: useful evidence") {
				t.Fatal("full list lost verification evidence/summary")
			}
			for _, text := range []string{m.renderCompactPlanProgress(), m.renderPlanProgressHeader(160)} {
				if !strings.Contains(text, "Current: ● Build capability") || strings.Contains(text, "internal-") || strings.Contains(text, "recorded, not live") != restored {
					t.Errorf("current presentation differs:\n%s", text)
				}
			}
			_, spans := m.layoutPlanPhaseCards(80)
			if spans[1].name != "internal-running" || m.planPanel.target != "internal-running" {
				t.Fatal("human titles changed identity anchors")
			}
		})
	}
}

func sidebarTestModel(width, height int) model {
	m := newModel(context.Background(), Config{Backend: staticBackend{info: backend.Info{Mode: "code"}}, Width: width, Height: height})
	m.ready = true
	s := agent.PlanSnapshot{PlanID: "sidebar", Revision: 1, Sequence: 1, Goal: "Keep progress visible", Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleExecuting}}
	for i := 0; i < 12; i++ {
		state := "pending"
		if i == 0 {
			state = "complete"
		}
		if i == 1 || i == 2 {
			state = "running"
		}
		s.Steps = append(s.Steps, agent.PlanStepInfo{Name: fmt.Sprintf("phase-%d", i+1), Description: fmt.Sprintf("Phase %d description", i+1), Status: state, ParallelGroup: "wave-1", Evidence: "exit 0: verified evidence"})
	}
	m.tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	m.tr.Streaming = true
	m.tr.Items = append(m.tr.Items, events.Item{Kind: events.ItemAgent, Content: strings.Repeat("conversation history\n\n", 50)})
	m.layout()
	m.refreshViewport()
	return m
}

func TestPlanSidebarDualBackendRestoreParity(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := newDualModeModel(t)
			defer m.cancel()
			m.width = width
			s := progressSnapshot("complete", "failed", "running")
			s.Goal = "Code-only progress fixture"
			ev := events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s}
			m.tr.LoadHistory([]events.HistoryMsg{{Delegation: &ev}})
			m.refreshViewport()
			want := m.planProgress()
			wantLayout := m.planLayout()
			m.planPanel.open = true
			m.layoutPlanPanel()
			m.planPanel.detail.SetYOffset(2)

			next, _ := m.switchBackend()
			m = next.(model)
			// Even a remote transcript containing a plan must never get Code chrome.
			m.tr.Apply(ev)
			m.refreshViewport()
			if m.info.Mode != "platform" || m.planLayout() != planLayoutHidden || m.planProgress().Present || m.planPanel.open || m.vp.Width() != width {
				t.Fatal("actual backend switch leaked Code progress into remote chat")
			}
			if strings.Contains(stripANSI(m.View().Content), "Plan progress · ctrl+g") {
				t.Fatal("remote view contains Code sidebar")
			}
			next, _ = m.switchBackend()
			m = next.(model)
			// Backend switching intentionally opens the welcome screen. The plan
			// returns when the user restores the Code session, not from stale UI state.
			if m.info.Mode != "code" || m.planLayout() != planLayoutHidden || m.planPanel.open {
				t.Fatal("Code welcome screen inherited a stale panel")
			}
			m.tr.LoadHistory([]events.HistoryMsg{{Delegation: &ev}})
			m.refreshViewport()
			if got := m.planProgress(); !reflect.DeepEqual(got, want) || m.planLayout() != wantLayout {
				t.Fatalf("Code session restore changed progress: got %+v want %+v", got, want)
			}
			if !strings.Contains(stripANSI(m.View().Content), "1/3 phases complete") {
				t.Fatal("restored progress is not visible")
			}
		})
	}
}

func TestPlanSidebarRevisionReplacesVisibleProgress(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := sidebarTestModel(width, 24)
			defer m.cancel()
			s := progressSnapshot("complete", "complete", "complete")
			s.Steps[1].Evidence = "obsolete evidence"
			s.Document.Results = "Old completed result"
			m.tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
			m.refreshViewport()
			if !strings.Contains(stripANSI(m.View().Content), "100%") {
				t.Fatal("initial completion not visible")
			}
			s = s.Clone()
			s.Sequence++
			s.Revision++
			s.Document.Results = ""
			s.Steps[1].Status, s.Steps[2].Status = "pending", "pending"
			s.Steps[1].Description, s.Steps[1].Evidence = "Revised verification", ""
			m.tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
			m.refreshViewport()
			view := stripANSI(m.View().Content)
			if !strings.Contains(view, "1/3 phases complete") || !strings.Contains(view, "33%") {
				t.Fatalf("revised percent did not decrease:\n%s", view)
			}
			next, _ := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
			m = next.(model)
			body := m.renderPlanProgressBody(width, true)
			if !strings.Contains(body, "Revised verification") || strings.Contains(body, "obsolete evidence") {
				t.Fatalf("full list retained old definition/evidence:\n%s", body)
			}
		})
	}
}

func TestPlanSidebarTerminalLifecyclePersists(t *testing.T) {
	for _, status := range []events.PlanStatus{events.PlanDeclined, events.PlanChangesRequested} {
		for _, width := range []int{80, 120} {
			t.Run(fmt.Sprintf("%v/%d", status, width), func(t *testing.T) {
				m := sidebarTestModel(width, 24)
				defer m.cancel()
				m.tr.Streaming = false
				m.tr.Items[0].PlanStatus = status
				m.refreshViewport()
				want := "Declined"
				if status == events.PlanChangesRequested {
					want = "Changes requested"
				}
				m.vp.GotoBottom()
				if m.planLayout() == planLayoutHidden || !strings.Contains(stripANSI(m.View().Content), want) {
					t.Fatalf("%s plan disappeared after scrolling:\n%s", want, stripANSI(m.View().Content))
				}
				next, _ := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
				m = next.(model)
				if !m.planPanel.open || !strings.Contains(stripANSI(m.View().Content), want) {
					t.Fatal("terminal plan cannot be inspected")
				}
			})
		}
	}
}

func TestPlanSidebarLongUnicodeShortTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 14}, {101, 14}, {80, 14}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := sidebarTestModel(size[0], size[1])
			defer m.cancel()
			m.theme.NoColor = true
			s := m.tr.Items[0].PlanSnapshot
			s.Goal = strings.Repeat("日本語 🧪 e\u0301 ", 40)
			for i := range s.Steps {
				s.Steps[i].Description = strings.Repeat("長い説明🧪", 40)
				s.Steps[i].Summary = strings.Repeat("詳細 ", 40)
			}
			m.refreshViewport()
			for _, open := range []bool{false, true} {
				m.planPanel.open = open
				m.layoutPlanPanel()
				view := stripANSI(m.View().Content)
				if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
					t.Fatalf("open=%v Unicode exceeds %dx%d: %dx%d", open, size[0], size[1], lipgloss.Width(view), lipgloss.Height(view))
				}
				if !open && !strings.Contains(view, "1/12 phases complete") {
					t.Fatal("long title displaced pinned count on short terminal")
				}
			}
		})
	}
}

func TestPlanSidebarResponsiveGeometry(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {101, 30}, {100, 30}, {80, 24}, {120, 14}, {80, 14}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := sidebarTestModel(size[0], size[1])
			defer m.cancel()
			expected := planLayoutCompact
			tw := size[0]
			if size[0] >= 101 {
				expected = planLayoutSidebar
				tw -= 29
			}
			if m.planLayout() != expected || m.vp.Width() != tw {
				t.Fatalf("layout=%v viewport=%d", m.planLayout(), m.vp.Width())
			}
			view := stripANSI(m.View().Content)
			if lipgloss.Height(view) > size[1] {
				t.Fatalf("height %d exceeds %d:\n%s", lipgloss.Height(view), size[1], view)
			}
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > size[0] {
					t.Fatalf("overwide %d: %q", lipgloss.Width(line), line)
				}
			}
			if !strings.Contains(view, "1/12 phases complete") {
				t.Fatalf("progress not pinned:\n%s", view)
			}
			if expected == planLayoutCompact {
				if !strings.Contains(view, "Current: ● Phase 2 description, ● Phase 3 description") {
					t.Fatalf("compact current phases missing:\n%s", view)
				}
			} else {
				if strings.Contains(m.planPanelHeader(24, size[1]), "Current:") {
					t.Fatal("wide current row remains")
				}
				if size[1] >= 30 {
					for _, phase := range []string{"● Phase 2", "● Phase 3"} {
						found := false
						for i := 0; i < 30; i++ {
							panel := stripANSI(m.renderPlanSidebar(m.vp.Height() + m.stickyHeaderLines))
							if strings.Contains(panel, phase) && !strings.Contains(panel, "Progress: unavailable") {
								found = true
								break
							}
							m.planPanel.sidebar.ScrollDown(1)
						}
						if !found {
							t.Fatalf("running card %s not inspectable", phase)
						}
					}
				}
			}
			m.vp.GotoTop()
			if !strings.Contains(stripANSI(m.View().Content), "1/12 phases complete") {
				t.Fatal("panel scrolled away")
			}
		})
	}
}

func TestPlanSidebarFullListPreservesComposerAndTranscript(t *testing.T) {
	m := sidebarTestModel(80, 24)
	defer m.cancel()
	m.ta.SetValue("draft untouched")
	m.history = []string{"older prompt", "newer prompt"}
	m.historyIdx = 1
	m.userScrolledUp = true
	m.vp.SetYOffset(3)
	offset, focus := m.vp.YOffset(), m.ta.Focused()
	history := append([]string(nil), m.history...)
	historyIdx := m.historyIdx
	assertUnchanged := func() {
		t.Helper()
		if m.ta.Value() != "draft untouched" || m.ta.Focused() != focus || m.vp.YOffset() != offset || !m.userScrolledUp || m.historyIdx != historyIdx || !reflect.DeepEqual(m.history, history) {
			t.Fatal("full-list navigation changed composer, focus, prompt history, or transcript scroll")
		}
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = next.(model)
	if !m.planPanel.open {
		t.Fatal("Ctrl+G did not open full list")
	}
	for i := 0; i < 8; i++ {
		next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m = next.(model)
	}
	if m.planPanel.detail.YOffset() == 0 {
		t.Fatal("full list did not scroll")
	}
	if !strings.Contains(stripANSI(m.View().Content), "verified evidence") {
		t.Fatal("evidence not visible")
	}
	assertUnchanged()
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyUp}, {Code: tea.KeyDown}, {Code: tea.KeyPgUp}, {Code: tea.KeyHome}, {Code: tea.KeyEnd}, {Code: 'x', Text: "x"}} {
		next, _ = m.Update(key)
		m = next.(model)
		assertUnchanged()
	}
	next, _ = m.Update(tea.MouseWheelMsg{X: 20, Y: 10, Button: tea.MouseWheelUp})
	m = next.(model)
	assertUnchanged()
	next, _ = m.Update(tea.PasteMsg{Content: "do not paste"})
	m = next.(model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(model)
	if m.planPanel.open || m.ta.Value() != "draft untouched" || m.ta.Focused() != focus || m.vp.YOffset() != offset {
		t.Fatal("full list changed conversation state")
	}
}

func TestPlanSidebarMouseIsolation(t *testing.T) {
	m := sidebarTestModel(120, 30)
	defer m.cancel()
	offset := m.vp.YOffset()
	next, _ := m.handleMouse(tea.MouseWheelMsg{X: 110, Y: 15, Button: tea.MouseWheelDown})
	m = next.(model)
	if m.vp.YOffset() != offset || m.planPanel.sidebar.YOffset() == 0 {
		t.Fatal("panel wheel moved transcript or did not scroll panel")
	}
	if _, ok := m.selectionPointForMouse(tea.Mouse{X: m.transcriptWidth(), Y: m.viewportTopY()}); ok {
		t.Fatal("divider can select transcript")
	}
	next, _ = m.handleMouse(tea.MouseClickMsg{X: 110, Y: 2, Button: tea.MouseLeft})
	m = next.(model)
	if !m.planPanel.open {
		t.Fatal("header click did not open list")
	}
}

func TestPlanSidebarSessionModesAndMalformed(t *testing.T) {
	m := sidebarTestModel(120, 30)
	defer m.cancel()
	for _, flags := range [][2]bool{{false, false}, {true, false}, {false, true}} {
		m.graphPlanMode, m.askMode = flags[0], flags[1]
		if m.planLayout() != planLayoutSidebar {
			t.Fatal("Code mode hid plan")
		}
	}
	m.planPanel.open = true
	m.tr.Items = append(m.tr.Items, events.Item{Kind: events.ItemPlan, Content: "malformed"})
	m.refreshViewport()
	if m.planPanel.open || !strings.Contains(m.renderPlanProgressBody(28, false), "unavailable") {
		t.Fatal("new malformed plan retained old plan state")
	}
	m.info.Mode = "platform"
	m.refreshViewport()
	if m.planLayout() != planLayoutHidden || m.vp.Width() != 120 {
		t.Fatal("remote chat kept panel")
	}
	m.info.Mode = "code"
	m.tr = events.NewTranscript()
	m.refreshViewport()
	if m.planLayout() != planLayoutHidden || m.planPanel.open {
		t.Fatal("new session kept plan")
	}
}

func TestPlanSidebarNoColorUnicodeAndLifecycle(t *testing.T) {
	m := sidebarTestModel(101, 30)
	defer m.cancel()
	m.theme.NoColor = true
	s := m.tr.Items[0].PlanSnapshot
	s.Goal = strings.Repeat("日本語計画 ", 20)
	for i := range s.Steps {
		s.Steps[i].Status = "complete"
	}
	m.tr.Streaming = false
	m.refreshViewport()
	text := m.renderPlanSidebar(28)
	if strings.Contains(text, "\x1b") || lipgloss.Width(text) > 28 {
		t.Fatal("invalid plain Unicode panel")
	}
	if !strings.Contains(text, "100%") || !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(text, "│", " ")), " "), "Awaiting completion record") {
		t.Fatal("phase completion confused with Results")
	}
	s.Document.Results = "proven capability"
	m.refreshViewport()
	if !strings.Contains(m.renderPlanSidebar(28), "Results recorded") {
		t.Fatal("completion record not shown")
	}
}

func TestPlanSidebarApprovalActionsAfterInspection(t *testing.T) {
	for _, width := range []int{80, 120} {
		for _, tc := range []struct {
			key    string
			status events.PlanStatus
		}{
			{key: "r", status: events.PlanChangesRequested},
			{key: "n", status: events.PlanDeclined},
		} {
			t.Run(fmt.Sprintf("%d/%s", width, tc.key), func(t *testing.T) {
				m := sidebarTestModel(width, 30)
				defer m.cancel()
				s := progressSnapshot("pending", "pending", "pending")
				s.PlanID = "approval-actions"
				s.Document.Lifecycle = ""
				m.tr = events.NewTranscript()
				m.tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
				m.refreshViewport()
				next, _ := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
				m = next.(model)
				if !m.planPanel.open {
					t.Fatal("pending plan cannot be inspected")
				}
				next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
				m = next.(model)
				if m.tr.ApprovalCursor != 0 || m.tr.Items[0].PlanStatus != events.PlanPending {
					t.Fatal("inspecting phases changed approval selection")
				}
				next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				m = next.(model)
				if m.planPanel.open || m.tr.Items[0].PlanStatus != events.PlanPending {
					t.Fatal("closing inspection declined pending approval")
				}
				next, _ = m.Update(tea.KeyPressMsg{Code: rune(tc.key[0]), Text: tc.key})
				m = next.(model)
				if m.tr.Items[0].PlanStatus != tc.status {
					t.Fatalf("approval accelerator %q intercepted: got %v", tc.key, m.tr.Items[0].PlanStatus)
				}
				if m.planLayout() == planLayoutHidden {
					t.Fatal("decision removed persistent progress")
				}
			})
		}
	}
}

func TestPlanSidebarApprovalAndModalPrecedence(t *testing.T) {
	for _, w := range []int{120, 101, 100, 80} {
		m := sidebarTestModel(w, 30)
		s := agent.PlanSnapshot{PlanID: "approval", Revision: 1, Sequence: 1, Goal: "Approve this plan", Steps: []agent.PlanStepInfo{{Name: "one", Description: "One phase"}}}
		m.tr = events.NewTranscript()
		m.tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
		m.refreshViewport()
		text := stripANSI(m.View().Content)
		for _, label := range []string{"Approve", "Decline", "Request changes"} {
			if !strings.Contains(text, label) {
				t.Fatalf("width %d missing %s:\n%s", w, label, text)
			}
		}
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = next.(model)
		if m.tr.ApprovalCursor != 1 {
			t.Fatal("panel captured approval arrow")
		}
		m.sessions.open = true
		if _, _, handled := m.handlePlanProgressKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}); handled {
			t.Fatal("plan stole modal key")
		}
		m.cancel()
	}
}

func TestPlanSidebarResizeAndCancel(t *testing.T) {
	m := sidebarTestModel(120, 30)
	defer m.cancel()
	m.ta.SetValue("draft survives resize")
	m.vp.SetYOffset(4)
	m.userScrolledUp = true
	m.planPanel.open = true
	m.layoutPlanPanel()
	m.planPanel.detail.SetYOffset(5)
	for _, width := range []int{80, 101, 100, 120} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m = next.(model)
		if !m.planPanel.open || m.planPanel.detail.YOffset() != 5 || m.ta.Value() != "draft survives resize" || m.vp.YOffset() != 4 {
			t.Fatalf("resize to %d moved user state", width)
		}
	}
	cancelled := false
	m.turnCancel = func() { cancelled = true }
	next, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = next.(model)
	if !cancelled || m.quitting {
		t.Fatal("full list intercepted turn cancellation")
	}
}

func sidebarWheel(m model, button tea.MouseButton, count int) model {
	y := 3 + lipgloss.Height(m.planPanelHeader(24, m.vp.Height()+m.stickyHeaderLines))
	for range count {
		next, _ := m.Update(tea.MouseWheelMsg{X: m.transcriptWidth() + 5, Y: y, Button: button})
		m = next.(model)
	}
	return m
}

func sidebarApply(m model, s agent.PlanSnapshot) model {
	next, _ := m.Update(eventMsg(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s}))
	return next.(model)
}

func sidebarLateSnapshot(m model, active int) agent.PlanSnapshot {
	s := m.tr.Items[0].PlanSnapshot.Clone()
	s.Sequence++
	for i := range s.Steps {
		s.Steps[i].Status = "pending"
		if i == active {
			s.Steps[i].Status = "running"
		}
	}
	return s
}

func assertSidebarTargetVisible(t *testing.T, m model, name string) {
	t.Helper()
	for _, span := range m.planPanel.spans {
		if span.name != name {
			continue
		}
		offset, height := m.planPanel.sidebar.YOffset(), m.planPanel.sidebar.Height()
		if span.end-span.start > height {
			if offset != span.start {
				t.Fatalf("oversized %s starts at %d, offset %d", name, span.start, offset)
			}
		} else if span.start < offset || span.end > offset+height {
			t.Fatalf("%s span [%d,%d) outside [%d,%d)", name, span.start, span.end, offset, offset+height)
		}
		if m.planPanel.revealPending {
			t.Fatal("visible reveal not consumed")
		}
		return
	}
	t.Fatalf("missing target %s", name)
}

func TestPlanSidebarInitialActiveReveal(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprint(restored), func(t *testing.T) {
			m := sidebarTestModel(120, 30)
			defer m.cancel()
			s := sidebarLateSnapshot(m, 9)
			m.tr = events.NewTranscript()
			ev := events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s}
			if restored {
				m.tr.LoadHistory([]events.HistoryMsg{{Delegation: &ev}})
			} else {
				m.tr.Apply(ev)
				m.tr.Streaming = true
			}
			m.refreshViewport()
			assertSidebarTargetVisible(t, m, "phase-10")
			if restored && m.planProgress().Phases[9].Live {
				t.Fatal("restore claimed live worker")
			}
		})
	}
	for _, state := range []string{"pending", "complete", "review", "failed", "declined", "approval", "results", "empty", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			m := sidebarTestModel(120, 30)
			defer m.cancel()
			s := sidebarLateSnapshot(m, -1)
			s.PlanID = "initial-" + state
			want := ""
			switch state {
			case "complete":
				for i := range s.Steps {
					s.Steps[i].Status = "complete"
				}
			case "review":
				s.Steps[9].Status = "complete"
				s.Document.CodeReviewPending = "phase-10"
				want = "phase-10"
			case "failed":
				s.Steps[9].Status = "failed"
				want = "phase-10"
			case "approval":
				s.Document.Lifecycle = ""
			case "results":
				s.Document.Results = "done"
				s.Steps[9].Status = "running"
			case "empty":
				s.Steps = nil
			}
			m = sidebarApply(m, s)
			if state == "declined" {
				m.tr.Items[len(m.tr.Items)-1].PlanStatus = events.PlanDeclined
				m.refreshViewport()
			}
			if state == "unavailable" {
				m.tr.Items = append(m.tr.Items, events.Item{Kind: events.ItemPlan, Content: "bad"})
				m.refreshViewport()
			}
			if m.planPanel.target != want {
				t.Fatalf("target %q want %q", m.planPanel.target, want)
			}
			if want != "" {
				assertSidebarTargetVisible(t, m, want)
			} else if m.planPanel.sidebar.YOffset() != 0 {
				t.Fatal("no target moved initial offset")
			}
		})
	}
}

func TestPlanSidebarRevealOnPhaseChange(t *testing.T) {
	m := sidebarTestModel(120, 30)
	defer m.cancel()
	m = sidebarApply(m, sidebarLateSnapshot(m, 9))
	assertSidebarTargetVisible(t, m, "phase-10")
	m.ta.SetValue("safe draft")
	m.vp.SetYOffset(4)
	m.userScrolledUp = true
	transcript, focus := m.vp.YOffset(), m.ta.Focused()
	for _, target := range []int{10, 11, 8} {
		m = sidebarWheel(m, tea.MouseWheelUp, 100) // includes edge input
		if m.planPanel.sidebar.YOffset() != 0 {
			t.Fatal("wheel did not browse to top")
		}
		s := m.tr.Items[0].PlanSnapshot.Clone()
		s.Sequence++
		m = sidebarApply(m, s)
		m.refreshViewport()
		if m.planPanel.sidebar.YOffset() != 0 {
			t.Fatal("same target pulled browsing back")
		}
		stale := sidebarLateSnapshot(m, 0)
		stale.Sequence = 1
		m = sidebarApply(m, stale)
		if m.planPanel.sidebar.YOffset() != 0 {
			t.Fatal("stale snapshot pulled browsing back")
		}
		m = sidebarApply(m, sidebarLateSnapshot(m, target))
		assertSidebarTargetVisible(t, m, fmt.Sprintf("phase-%d", target+1))
		if m.vp.YOffset() != transcript || m.ta.Value() != "safe draft" || m.ta.Focused() != focus {
			t.Fatal("transition stole transcript/draft/focus")
		}
	}
	// Status changes and concurrent workers do not change the selected identity.
	m = sidebarWheel(m, tea.MouseWheelUp, 100)
	s := m.tr.Items[0].PlanSnapshot.Clone()
	s.Sequence++
	s.Steps[9].Status = "running"
	m = sidebarApply(m, s)
	if m.planPanel.target != "phase-9" || m.planPanel.sidebar.YOffset() != 0 {
		t.Fatal("concurrent worker stole target")
	}
	s.Sequence++
	s.Steps[9].Status = "pending"
	s.Steps[8].Status = "complete"
	s.Document.CodeReviewPending = "phase-9"
	m = sidebarApply(m, s)
	if m.planPanel.sidebar.YOffset() != 0 {
		t.Fatal("same-identity review pulled browsing back")
	}
	s.Sequence++
	s.Document.CodeReviewPending = ""
	s.Steps[8].Status = "failed"
	m = sidebarApply(m, s)
	if m.planPanel.sidebar.YOffset() != 0 {
		t.Fatal("same-identity failure pulled browsing back")
	}
	s.Sequence++
	s.Steps[8].Status = "pending"
	m = sidebarApply(m, s)
	if m.planPanel.target != "" || m.planPanel.sidebar.YOffset() != 0 {
		t.Fatal("absence moved browsing")
	}
	s.Sequence++
	s.Steps[8].Status = "running"
	m = sidebarApply(m, s)
	assertSidebarTargetVisible(t, m, "phase-9")
	before := m.planPanel.sidebar.YOffset()
	next, _ := m.Update(tea.MouseWheelMsg{X: 110, Y: 3, Button: tea.MouseWheelDown})
	m = next.(model)
	if m.planPanel.sidebar.YOffset() != before {
		t.Fatal("header wheel scrolled cards")
	}
	// A selected target already fully visible must not move the viewport.
	s = sidebarLateSnapshot(m, 10)
	_, spans := m.layoutPlanPhaseCards(24)
	m.planPanel.sidebar.SetYOffset(spans[10].start)
	before = m.planPanel.sidebar.YOffset()
	m = sidebarApply(m, s)
	if m.planPanel.sidebar.YOffset() != before {
		t.Fatal("fully visible new target moved viewport")
	}
	assertSidebarTargetVisible(t, m, "phase-11")
	// Losing a target preserves a nonzero browsing offset, too.
	s = sidebarLateSnapshot(m, -1)
	m = sidebarApply(m, s)
	if m.planPanel.sidebar.YOffset() != before {
		t.Fatal("no target reset nonzero offset")
	}
}

func TestPlanSidebarScrollResetAndResize(t *testing.T) {
	m := sidebarTestModel(120, 30)
	defer m.cancel()
	m = sidebarApply(m, sidebarLateSnapshot(m, 9))
	m = sidebarWheel(m, tea.MouseWheelUp, 100)
	resize := func(width, height int) {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		m = next.(model)
	}
	key := func(code rune, mod tea.KeyMod) {
		next, _ := m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
		m = next.(model)
	}
	resize(80, 30)
	resize(120, 30)
	if m.planPanel.sidebar.YOffset() != 0 {
		t.Fatal("resize without transition pulled back")
	}
	resize(80, 30)
	m = sidebarApply(m, sidebarLateSnapshot(m, 10))
	if !m.planPanel.revealPending {
		t.Fatal("compact consumed reveal")
	}
	resize(120, 30)
	assertSidebarTargetVisible(t, m, "phase-11")
	m = sidebarWheel(m, tea.MouseWheelUp, 100)
	key('g', tea.ModCtrl)
	key(tea.KeyEscape, 0)
	if m.planPanel.sidebar.YOffset() != 0 {
		t.Fatal("Ctrl+G return without transition pulled back")
	}
	key('g', tea.ModCtrl)
	m = sidebarApply(m, sidebarLateSnapshot(m, 11))
	m = sidebarApply(m, sidebarLateSnapshot(m, 8)) // latest hidden target wins
	if !m.planPanel.revealPending || m.planPanel.sidebar.YOffset() != 0 {
		t.Fatal("covered list consumed reveal")
	}
	key(tea.KeyEscape, 0)
	assertSidebarTargetVisible(t, m, "phase-9")
	resize(120, 10)
	m = sidebarApply(m, sidebarLateSnapshot(m, 10))
	if !m.planPanel.revealPending {
		t.Fatal("invisible card viewport consumed reveal")
	}
	resize(120, 30)
	assertSidebarTargetVisible(t, m, "phase-11")
	// Same-target revision: preserve a visible card by identity through reflow.
	s := m.tr.Items[0].PlanSnapshot.Clone()
	s.Sequence++
	s.Steps[4].Description = "A wrapped phase description with a second row"
	m = sidebarApply(m, s)
	m.planPanel.sidebar.SetYOffset(m.planPanel.spans[4].start + 1)
	s = m.tr.Items[0].PlanSnapshot.Clone()
	s.Sequence++
	s.Revision++
	s.Steps[0].Description = strings.Repeat("日本語 🧪 e\u0301 ", 15)
	m = sidebarApply(m, s)
	if m.planPanel.sidebar.YOffset() != m.planPanel.spans[4].start+1 {
		t.Fatal("revision lost visible anchor")
	}
	oldOffset := m.planPanel.sidebar.YOffset()
	s.Sequence++
	s.Revision++
	s.Steps = append(s.Steps[:4:4], s.Steps[5:]...)
	m = sidebarApply(m, s)
	if m.planPanel.sidebar.YOffset() != oldOffset {
		t.Fatal("removed anchor did not retain numeric offset")
	}
	// Different plan and transcript owner both reset initial positioning.
	m = sidebarWheel(m, tea.MouseWheelUp, 100)
	s.Sequence++
	s.PlanID = "replacement"
	m = sidebarApply(m, s)
	assertSidebarTargetVisible(t, m, "phase-11")
	m = sidebarWheel(m, tea.MouseWheelUp, 100)
	m.tr = events.NewTranscript()
	m.tr.SessionID = "other-session"
	m = sidebarApply(m, s)
	assertSidebarTargetVisible(t, m, "phase-11")
}

func TestPlanSidebarTargetPriority(t *testing.T) {
	m := sidebarTestModel(120, 65)
	defer m.cancel()
	s := sidebarLateSnapshot(m, 9)
	s.Steps[10].Status = "running"
	s.Steps[6].Status = "failed"
	s.Document.CodeReviewPending = s.Steps[8].Name
	for _, want := range []string{"phase-10", "phase-11", "phase-9", "phase-7"} {
		m = sidebarApply(m, s)
		if m.planPanel.target != want {
			t.Fatalf("selected %q, want %q", m.planPanel.target, want)
		}
		assertSidebarTargetVisible(t, m, want)
		s.Sequence++
		switch want {
		case "phase-10":
			s.Steps[9].Status = "complete"
		case "phase-11":
			s.Steps[10].Status = "complete"
		case "phase-9":
			s.Document.CodeReviewPending = ""
		}
	}
}

func TestPlanSidebarCardSpanLayout(t *testing.T) {
	m := sidebarTestModel(120, 30)
	defer m.cancel()
	s := sidebarLateSnapshot(m, 9)
	s.Steps[9].Description = strings.Repeat("日本語 🧪 e\u0301 ", 40)
	m = sidebarApply(m, s)
	assertSidebarTargetVisible(t, m, "phase-10")
	for _, width := range []int{12, 24, 40} {
		content, spans := m.layoutPlanPhaseCards(width)
		rows := strings.Split(content, "\n")
		for i, span := range spans {
			if span.index != i || span.name != s.Steps[i].Name || span.end <= span.start {
				t.Fatalf("invalid span %+v", span)
			}
			if i == 0 && span.start != 0 || i > 0 && span.start != spans[i-1].end+1 {
				t.Fatal("gap not accounted for")
			}
			if i < len(spans)-1 && rows[span.end] != "" {
				t.Fatal("card span includes gap")
			}
			for _, row := range rows[span.start:span.end] {
				if lipgloss.Width(row) > width {
					t.Fatalf("wrapped row wider than %d: %q", width, row)
				}
			}
		}
		if spans[len(spans)-1].end != len(rows) || content != m.renderPlanPhaseCards(width) {
			t.Fatal("span/render metrics diverged")
		}
	}
}
