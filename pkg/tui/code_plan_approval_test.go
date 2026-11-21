package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestPlanApprovalViewportCursorRepaints(t *testing.T) {
	for _, source := range []string{"legacy", "snapshot", "restored"} {
		t.Run(source, func(t *testing.T) {
			m := newTestComposerModel(100)
			defer m.cancel()
			ev := events.NewPlan("Short plan", events.PlanPending)
			if source != "legacy" {
				ev = events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &agent.PlanSnapshot{PlanID: "cursor", Revision: 1, Sequence: 1, Goal: "Cursor fixture", Steps: []agent.PlanStepInfo{{Name: "one", Description: "One phase"}}}}
			}
			if source == "restored" {
				m.tr.LoadHistory([]events.HistoryMsg{{Kind: "delegation", Delegation: &ev}})
			} else {
				m.tr.Apply(ev)
			}
			m.refreshViewport()
			before := stripANSI(m.vp.View())
			if !strings.Contains(before, "▸ Enter  "+planOptApprove) {
				t.Fatalf("initial highlight: %s", before)
			}
			next, _, _ := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyDown})
			m = next.(model)
			after := stripANSI(m.vp.View())
			if m.tr.ApprovalCursor != 1 {
				t.Fatal("cursor did not move")
			}
			if after == before || !strings.Contains(after, "▸ r  "+planOptRequest) {
				t.Fatalf("cursor moved but viewport stale: %s", after)
			}
			next, _, _ = m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyUp})
			m = next.(model)
			if got := stripANSI(m.vp.View()); !strings.Contains(got, "▸ Enter  "+planOptApprove) {
				t.Fatalf("Up highlight: %s", got)
			}
		})
	}
}

func TestCodePlanApprovalSingleLineOptionsAndRemoteOptions(t *testing.T) {
	m := newTestComposerModel(100)
	defer m.cancel()
	m.tr.Apply(events.NewPlan("Short plan", events.PlanPending))
	m.info.Mode = "code"
	m.tr.ApprovalCursor = 1
	m.refreshViewport()
	want := []string{
		"✦ Plan Ready · choose how to proceed",
		"  1/y  Approve and run autonomously until done",
		"▸ 2    Approve and pause for review after each phase",
		"  r    Request changes",
		"  n    Decline",
	}
	lines := m.planApprovalLines(*m.approvalItem(), 90)
	for i, line := range want {
		if got := stripANSI(lines[i+3]); got != line {
			t.Fatalf("option row %d = %q, want %q", i, got, line)
		}
	}
	if len(lines) != 9 {
		t.Fatalf("expected header, four single-line options and key hint; got %d lines: %v", len(lines), lines)
	}
	if !strings.Contains(stripANSI(m.renderHints()), "Enter select") {
		t.Fatal("Code hints must describe selection, not unconditional implementation")
	}
	m.info.Mode = "platform"
	m.refreshViewport()
	if !reflect.DeepEqual(m.approvalOptions(), events.PlanApprovalOptions()) {
		t.Fatalf("remote options changed: %v", m.approvalOptions())
	}
	if strings.Contains(stripANSI(m.vp.View()), planOptRun) || strings.Contains(stripANSI(m.vp.View()), planOptReview) {
		t.Fatal("Code controls leaked into remote card")
	}
	if !strings.Contains(stripANSI(m.renderHints()), "enter implement") {
		t.Fatal("remote hints changed")
	}
}

type codePlanApprovalCaptureBackend struct {
	planApprovalCaptureBackend
	message string
}

func (b *codePlanApprovalCaptureBackend) RunTurn(ctx context.Context, message string, opts backend.TurnOptions) (<-chan events.Event, error) {
	b.message = message
	return b.planApprovalCaptureBackend.RunTurn(ctx, message, opts)
}

func TestCodePlanApprovalModes(t *testing.T) {
	for _, tc := range []struct {
		name, mode, key string
		down            int
		want            agent.CodePlanExecutionMode
		status          events.PlanStatus
	}{
		{name: "default", mode: "code", want: agent.CodePlanRunToCompletion},
		{name: "review", mode: "code", down: 1, want: agent.CodePlanReviewEachPhase},
		{name: "review accelerator", mode: "code", key: "2", want: agent.CodePlanReviewEachPhase},
		{name: "run accelerator", mode: "code", key: "y", want: agent.CodePlanRunToCompletion},
		{name: "changes", mode: "code", key: "3", status: events.PlanChangesRequested},
		{name: "decline", mode: "code", key: "4", status: events.PlanDeclined},
		{name: "escape", mode: "code", key: "esc", status: events.PlanDeclined},
		{name: "platform", mode: "platform"},
		{name: "platform changes", mode: "platform", key: "2", status: events.PlanChangesRequested},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &codePlanApprovalCaptureBackend{}
			m := newModel(context.Background(), Config{Backend: capture, Width: 100, Height: 30})
			defer m.cancel()
			m.info.Mode = tc.mode
			m.ready = true
			m.layout()
			m.graphPlanMode = tc.mode == "code"
			m.tr.Apply(events.NewPlan("Approval fixture", events.PlanPending))
			m.refreshViewport()
			for i := 0; i < tc.down; i++ {
				next, _, _ := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyDown})
				m = next.(model)
			}
			if m.turnOptions().CodePlanExecutionMode != "" {
				t.Fatal("cadence leaked into planning turn")
			}
			key := tea.KeyPressMsg{Code: tea.KeyEnter}
			if tc.key == "esc" {
				key = tea.KeyPressMsg{Code: tea.KeyEsc}
			} else if tc.key != "" {
				key = tea.KeyPressMsg{Code: rune(tc.key[0]), Text: tc.key}
			}
			next, cmd, handled := m.handleApprovalKey(key)
			m = next.(model)
			if !handled {
				t.Fatal("key not handled")
			}
			if tc.status != "" {
				if cmd != nil || m.tr.Items[0].PlanStatus != tc.status {
					t.Fatalf("nonapproval: cmd=%v status=%s", cmd != nil, m.tr.Items[0].PlanStatus)
				}
				return
			}
			if cmd == nil {
				t.Fatal("approval did not launch")
			}
			started := cmd().(planApprovalStartedMsg)
			if started.err != nil {
				t.Fatal(started.err)
			}
			defer started.cancel()
			wantMessage := "I approve this plan. Please start implementing it now, phase by phase."
			switch tc.want {
			case agent.CodePlanRunToCompletion:
				wantMessage = "I approve this plan. Please run it to completion, continuing through verified phases."
			case agent.CodePlanReviewEachPhase:
				wantMessage = "I approve this plan. Please start implementing it now, pausing for review after each verified phase."
			}
			if capture.message != wantMessage {
				t.Fatalf("approval message = %q, want %q", capture.message, wantMessage)
			}
			if capture.opts.CodePlanExecutionMode != tc.want || !capture.opts.ApprovedPlanExecution {
				t.Fatalf("options: %+v", capture.opts)
			}
			if _, duplicate := m.submitPlanApproval(planOptApprove); duplicate != nil {
				t.Fatal("duplicate approval launched")
			}
			if m.turnOptions().CodePlanExecutionMode != "" {
				t.Fatal("cadence leaked into normal continuation")
			}
		})
	}
}

func TestCodePlanApprovalNavigationAndMouse(t *testing.T) {
	for _, width := range []int{60, 100} {
		capture := &codePlanApprovalCaptureBackend{}
		m := newModel(context.Background(), Config{Backend: capture, Width: width, Height: 30})
		defer m.cancel()
		m.info.Mode = "code"
		m.ready = true
		m.layout()
		m.tr.Apply(events.NewPlan("Short plan", events.PlanPending))
		m.refreshViewport()
		opts := m.approvalOptions()
		if len(opts) != 4 {
			t.Fatalf("Code options %v", opts)
		}
		for _, idx := range []int{1, 2, 3, 0} {
			next, _, _ := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyDown})
			m = next.(model)
			selected := "▸ " + m.planApprovalLabel(opts[idx], idx)
			if width == 60 && idx == 1 {
				selected = "▸ 2    Approve and pause for review after each ph…"
			}
			if got := stripANSI(m.vp.View()); !strings.Contains(got, selected) {
				t.Fatalf("width %d cursor %d missing %q: %s", width, idx, selected, got)
			}
		}
		var point selectionPoint
		found := false
		for line, text := range m.transcriptPlainLines {
			if col := strings.Index(text, "2    Approve and pause for review"); col >= 0 {
				point = selectionPoint{line: line, col: len([]rune(text[:col])) + 3}
				found = true
				break
			}
		}
		if !found {
			t.Fatal("review row absent")
		}
		mouse := tea.Mouse{Button: tea.MouseLeft, X: point.col, Y: point.line - m.vp.YOffset() + m.viewportTopY()}
		next, _ := m.handleMousePress(mouse)
		m = next.(model)
		next, cmd := m.handleMouseRelease(mouse)
		m = next.(model)
		if cmd == nil {
			t.Fatal("mouse approval did not launch")
		}
		started := cmd().(planApprovalStartedMsg)
		if started.err != nil {
			t.Fatal(started.err)
		}
		started.cancel()
		if capture.opts.CodePlanExecutionMode != agent.CodePlanReviewEachPhase {
			t.Fatalf("mouse options: %+v", capture.opts)
		}
	}
}
