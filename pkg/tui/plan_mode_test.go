package tui

import (
	"context"
	"strings"
	"testing"

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
	m.togglePlanMode()
	if !m.planMode {
		t.Fatal("expected plan mode enabled")
	}
	if len(m.tr.Items) != 0 {
		t.Fatalf("mode toggle should not write transcript messages, got %#v", m.tr.Items)
	}

	m.togglePlanMode()
	if m.planMode {
		t.Fatal("expected plan mode disabled")
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

func TestTogglePlanModeThreeWayCycle(t *testing.T) {
	m := model{tr: events.NewTranscript()}

	// Normal → Plan
	m.togglePlanMode()
	if !m.planMode || m.graphPlanMode {
		t.Fatalf("first toggle should enter Plan mode, got plan=%v graph=%v", m.planMode, m.graphPlanMode)
	}

	// Plan → Graph Plan
	m.togglePlanMode()
	if m.planMode || !m.graphPlanMode {
		t.Fatalf("second toggle should enter Graph Plan mode, got plan=%v graph=%v", m.planMode, m.graphPlanMode)
	}

	// Graph Plan → Normal
	m.togglePlanMode()
	if m.planMode || m.graphPlanMode {
		t.Fatalf("third toggle should return to Normal, got plan=%v graph=%v", m.planMode, m.graphPlanMode)
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

func TestRenderComposerShowsGraphPlanLabel(t *testing.T) {
	m := newTestComposerModel(80)
	m.graphPlanMode = true
	out := stripANSI(m.renderComposer())
	if !strings.Contains(out, " Graph Plan ") {
		t.Fatalf("graph plan composer should show Graph Plan mode label:\n%s", out)
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
	m.planMode = true
	m.graphPlanMode = false
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
	m.planMode = true
	m.graphPlanMode = false
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
	m.planMode = true
	m.graphPlanMode = false
	m.tr.Apply(events.Event{
		Kind:         events.KindApproval,
		ToolName:     "announce_plan",
		Options:      []string{"Approve & implement", "Request changes", "Decline"},
		ApprovalKind: "plan",
	})
	next, _ := m.submitPlanApproval("Request changes")
	nm := next.(model)
	// Plan mode should remain active so the user can describe changes.
	if !nm.planMode {
		t.Fatal("planMode should stay true after request changes")
	}
}

