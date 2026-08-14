package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/events"
)

func TestTurnOptionsPlanMode(t *testing.T) {
	m := model{}
	if got := m.turnOptions(); got.SystemContext != "" {
		t.Fatalf("plan mode off SystemContext = %q, want empty", got.SystemContext)
	}
	if got := m.turnOptions(); got.PlanMode {
		t.Fatal("plan mode off should not set PlanMode flag")
	}

	m.planMode = true
	got := m.turnOptions()
	if got.SystemContext == "" {
		t.Fatal("plan mode on should send system context")
	}
	if !got.PlanMode {
		t.Fatal("plan mode on should set the PlanMode flag for the runtime gate")
	}
}

func TestTogglePlanModeDoesNotWriteTranscriptMessages(t *testing.T) {
	m := model{tr: events.NewTranscript()}

	// Normal → Plan (graphPlanMode=true)
	m.togglePlanMode()
	if !m.graphPlanMode {
		t.Fatal("expected graphPlanMode enabled after first toggle")
	}
	if len(m.tr.Items) != 0 {
		t.Fatalf("mode toggle should not write transcript messages, got %#v", m.tr.Items)
	}

	// Plan → Ask
	m.togglePlanMode()
	if !m.askMode {
		t.Fatal("expected askMode enabled after second toggle")
	}
	if len(m.tr.Items) != 0 {
		t.Fatalf("mode toggle should stay silent, got %#v", m.tr.Items)
	}

	// Ask → Normal
	m.togglePlanMode()
	if m.graphPlanMode || m.planMode || m.askMode {
		t.Fatal("expected all modes off after third toggle")
	}
	if len(m.tr.Items) != 0 {
		t.Fatalf("mode toggle should stay silent, got %#v", m.tr.Items)
	}
}

func TestRenderComposerShowsModeInBottomBorder(t *testing.T) {
	m := newTestComposerModel(80)
	out := stripANSI(m.renderComposer())
	if !strings.Contains(out, " Normal ") {
		t.Fatalf("normal composer should show Normal mode label:\n%s", out)
	}
	if strings.Contains(out, " Plan ") {
		t.Fatalf("normal composer should not show Plan mode label:\n%s", out)
	}

	m.planMode = true
	out = stripANSI(m.renderComposer())
	if !strings.Contains(out, " Plan ") {
		t.Fatalf("plan composer should show Plan mode label:\n%s", out)
	}
}

func newTestComposerModel(width int) model {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: width, Height: 24})
	m.ready = true
	m.layout()
	return m
}

func TestTogglePlanModeCycle(t *testing.T) {
	m := model{tr: events.NewTranscript()}

	// Normal → Plan
	m.togglePlanMode()
	if !m.graphPlanMode || m.planMode || m.askMode {
		t.Fatalf("first toggle should enter Plan mode, got graphPlan=%v plan=%v ask=%v", m.graphPlanMode, m.planMode, m.askMode)
	}

	// Plan → Ask
	m.togglePlanMode()
	if m.graphPlanMode || m.planMode || !m.askMode {
		t.Fatalf("second toggle should enter Ask mode, got graphPlan=%v plan=%v ask=%v", m.graphPlanMode, m.planMode, m.askMode)
	}

	// Ask → Normal
	m.togglePlanMode()
	if m.graphPlanMode || m.planMode || m.askMode {
		t.Fatalf("third toggle should return to Normal, got graphPlan=%v plan=%v ask=%v", m.graphPlanMode, m.planMode, m.askMode)
	}

	if len(m.tr.Items) != 0 {
		t.Fatalf("mode cycling should not write transcript messages, got %#v", m.tr.Items)
	}
}

func TestTurnOptionsGraphPlanMode(t *testing.T) {
	m := model{graphPlanMode: true}
	got := m.turnOptions()
	if !got.GraphPlanMode {
		t.Fatal("graph plan mode should set the GraphPlanMode flag")
	}
	if got.PlanMode {
		t.Fatal("graph plan mode must not set the PlanMode flag (mutually exclusive)")
	}
	if got.SystemContext == "" {
		t.Fatal("graph plan mode should send the graph-plan system context")
	}
	if !strings.Contains(got.SystemContext, "GRAPH-OPTIMIZED PLAN MODE") {
		t.Fatalf("graph plan system context should be the graph-plan prompt, got %q", got.SystemContext)
	}
}

func TestRenderComposerShowsPlanLabel(t *testing.T) {
	m := newTestComposerModel(80)
	m.graphPlanMode = true
	out := stripANSI(m.renderComposer())
	if !strings.Contains(out, " Plan ") {
		t.Fatalf("plan composer should show Plan mode label:\n%s", out)
	}
}

func TestPlanApprovalOverlayRendered(t *testing.T) {
	m := newTestComposerModel(80)
	m.planMode = true
	// Simulate a plan approval item in the transcript.
	m.tr.Apply(events.Event{
		Kind:         events.KindApproval,
		ToolName:     "announce_plan",
		Options:      []string{"Approve & implement", "Request changes", "Decline"},
		ApprovalKind: "plan",
	})
	if !m.tr.Awaiting {
		t.Fatal("transcript should be in Awaiting state after plan approval event")
	}
	overlay := stripANSI(m.renderApprovalOverlay())
	if !strings.Contains(overlay, "Plan Ready") {
		t.Fatalf("plan approval overlay should contain 'Plan Ready', got:\n%s", overlay)
	}
	if !strings.Contains(overlay, "Approve & implement") {
		t.Fatalf("plan approval overlay should contain 'Approve & implement', got:\n%s", overlay)
	}
	if !strings.Contains(overlay, "Request changes") {
		t.Fatalf("plan approval overlay should contain 'Request changes', got:\n%s", overlay)
	}
	if !strings.Contains(overlay, "Decline") {
		t.Fatalf("plan approval overlay should contain 'Decline', got:\n%s", overlay)
	}
}

func TestPlanApprovalApprove(t *testing.T) {
	m := newTestComposerModel(80)
	m.graphPlanMode = true
	m.planMode = false
	m.tr.Apply(events.Event{
		Kind:         events.KindApproval,
		ToolName:     "announce_plan",
		Options:      []string{"Approve & implement", "Request changes", "Decline"},
		ApprovalKind: "plan",
	})
	// submitPlanApproval with "Approve & implement" calls RunTurn on the
	// backend. staticBackend.RunTurn returns (nil, nil) which triggers an
	// early error path. We just verify mode switching works.
	next, _ := m.submitPlanApproval("Approve & implement")
	nm := next.(model)
	if nm.planMode {
		t.Fatal("planMode should be false after approve")
	}
	if nm.graphPlanMode {
		t.Fatal("graphPlanMode should be false after approve")
	}
}

func TestPlanApprovalDecline(t *testing.T) {
	m := newTestComposerModel(80)
	m.graphPlanMode = true
	m.planMode = false
	m.tr.Apply(events.Event{
		Kind:         events.KindApproval,
		ToolName:     "announce_plan",
		Options:      []string{"Approve & implement", "Request changes", "Decline"},
		ApprovalKind: "plan",
	})
	next, _ := m.submitPlanApproval("Decline")
	nm := next.(model)
	if nm.planMode {
		t.Fatal("planMode should be false after decline")
	}
	if nm.graphPlanMode {
		t.Fatal("graphPlanMode should be false after decline")
	}
	// Should have a system message about declining.
	found := false
	for _, it := range nm.tr.Items {
		if it.Kind == events.ItemSystem && strings.Contains(it.Content, "declined") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a system message about plan being declined")
	}
}

func TestPlanApprovalRequestChanges(t *testing.T) {
	m := newTestComposerModel(80)
	m.graphPlanMode = true
	m.planMode = false
	m.tr.Apply(events.Event{
		Kind:         events.KindApproval,
		ToolName:     "announce_plan",
		Options:      []string{"Approve & implement", "Request changes", "Decline"},
		ApprovalKind: "plan",
	})
	next, _ := m.submitPlanApproval("Request changes")
	nm := next.(model)
	// Plan mode should remain active so the user can describe changes.
	if !nm.graphPlanMode {
		t.Fatal("graphPlanMode should stay true after request changes")
	}
	if nm.ta.Placeholder != planChangesHint {
		t.Fatalf("placeholder = %q, want %q", nm.ta.Placeholder, planChangesHint)
	}
}

func TestPlanApprovalKeysMapCorrectly(t *testing.T) {
	newPlanModel := func() model {
		m := newTestComposerModel(80)
		m.graphPlanMode = true
		m.tr.Apply(events.Event{
			Kind:         events.KindApproval,
			ToolName:     "announce_plan",
			Options:      []string{"Approve & implement", "Request changes", "Decline"},
			ApprovalKind: "plan",
		})
		return m
	}

	t.Run("y approves", func(t *testing.T) {
		m := newPlanModel()
		next, _, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
		if !handled {
			t.Fatal("expected y to be handled")
		}
		nm := next.(model)
		if nm.graphPlanMode {
			t.Fatal("y should approve and leave plan mode")
		}
	})
	t.Run("r requests changes", func(t *testing.T) {
		m := newPlanModel()
		next, _, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
		if !handled {
			t.Fatal("expected r to be handled")
		}
		nm := next.(model)
		if !nm.graphPlanMode {
			t.Fatal("r should stay in plan mode")
		}
		if nm.ta.Placeholder != planChangesHint {
			t.Fatalf("placeholder = %q, want %q", nm.ta.Placeholder, planChangesHint)
		}
	})
	t.Run("n declines", func(t *testing.T) {
		m := newPlanModel()
		next, _, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
		if !handled {
			t.Fatal("expected n to be handled")
		}
		nm := next.(model)
		if nm.graphPlanMode {
			t.Fatal("n should decline and leave plan mode")
		}
		found := false
		for _, it := range nm.tr.Items {
			if it.Kind == events.ItemSystem && strings.Contains(it.Content, "declined") {
				found = true
			}
		}
		if !found {
			t.Fatal("expected decline system message")
		}
	})
	t.Run("esc declines", func(t *testing.T) {
		m := newPlanModel()
		next, _, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyEsc})
		if !handled {
			t.Fatal("expected esc to be handled")
		}
		nm := next.(model)
		if nm.graphPlanMode {
			t.Fatal("esc should decline and leave plan mode")
		}
	})
}

func TestTurnOptionsAskMode(t *testing.T) {
	m := model{askMode: true}
	got := m.turnOptions()
	if !got.AskMode {
		t.Fatal("ask mode should set the AskMode flag")
	}
	if got.PlanMode {
		t.Fatal("ask mode must not set the PlanMode flag (mutually exclusive)")
	}
	if got.GraphPlanMode {
		t.Fatal("ask mode must not set the GraphPlanMode flag (mutually exclusive)")
	}
	if got.SystemContext == "" {
		t.Fatal("ask mode should send the ask-mode system context")
	}
	if !strings.Contains(got.SystemContext, "ASK MODE") {
		t.Fatalf("ask mode system context should be the ask-mode prompt, got %q", got.SystemContext)
	}
}

func TestRenderComposerShowsAskLabel(t *testing.T) {
	m := newTestComposerModel(80)
	m.askMode = true
	out := stripANSI(m.renderComposer())
	if !strings.Contains(out, " Ask ") {
		t.Fatalf("ask composer should show Ask mode label:\n%s", out)
	}
}

func TestAskModeNotAvailableInPlatform(t *testing.T) {
	m := model{tr: events.NewTranscript()}
	m.info.Mode = "platform"

	// Normal → Plan
	m.togglePlanMode()
	if !m.planMode || m.graphPlanMode || m.askMode {
		t.Fatalf("platform first toggle should enter Plan, got plan=%v graph=%v ask=%v", m.planMode, m.graphPlanMode, m.askMode)
	}

	// Plan → Normal (no Graph Plan or Ask in platform mode)
	m.togglePlanMode()
	if m.planMode || m.graphPlanMode || m.askMode {
		t.Fatalf("platform second toggle should return to Normal, got plan=%v graph=%v ask=%v", m.planMode, m.graphPlanMode, m.askMode)
	}
}

func TestPlanApprovalFooterRendered(t *testing.T) {
	m := newTestComposerModel(100)
	m.planMode = true
	m.tr.Apply(events.Event{
		Kind:         events.KindApproval,
		ToolName:     "announce_plan",
		Options:      []string{"Approve & implement", "Request changes", "Decline"},
		ApprovalKind: "plan",
	})
	if !m.tr.Awaiting {
		t.Fatal("transcript should be in Awaiting state")
	}
	footer := stripANSI(m.renderPlanApprovalFooter())
	if !strings.Contains(footer, "Plan Ready") {
		t.Fatalf("plan approval footer should contain 'Plan Ready', got:\n%s", footer)
	}
	if !strings.Contains(footer, "Approve & implement") {
		t.Fatalf("plan approval footer should contain 'Approve & implement', got:\n%s", footer)
	}
	if !strings.Contains(footer, "Request changes") {
		t.Fatalf("plan approval footer should contain 'Request changes', got:\n%s", footer)
	}
	if !strings.Contains(footer, "Decline") {
		t.Fatalf("plan approval footer should contain 'Decline', got:\n%s", footer)
	}
}

func TestPlanApprovalNotRenderedInTranscript(t *testing.T) {
	m := newTestComposerModel(80)
	m.theme = plainTheme()
	// Add a plan item and a plan approval item.
	m.tr.Items = append(m.tr.Items, events.Item{
		Kind:    events.ItemPlan,
		Content: "# Execution Plan\n\n**Goal:** Test\n\n## Phases\n\n- [ ] **step-one** — Do something\n",
	})
	m.tr.Items = append(m.tr.Items, events.Item{
		Kind:         events.ItemApproval,
		ApprovalKind: "plan",
		ToolName:     "announce_plan",
		Content:      "Approve announce_plan?",
		Options:      []string{"Approve & implement", "Request changes", "Decline"},
	})
	out, _, _ := m.renderTranscript()
	plain := stripANSI(out)
	// The plan content should be rendered.
	if !strings.Contains(plain, "Test") {
		t.Fatalf("plan content should be rendered in transcript:\n%s", plain)
	}
	// The plan approval item should NOT be rendered.
	if strings.Contains(plain, "Approve announce_plan") {
		t.Fatalf("plan approval should NOT be rendered in transcript, got:\n%s", plain)
	}
}

