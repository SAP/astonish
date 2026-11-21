package tui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

type permissionTestBackend struct {
	codePlanApprovalCaptureBackend
	snapshot backend.CodePermissionsSnapshot
	fail     bool
	revoked  backend.CodePermissionScope
	enabled  int
}

func (b *permissionTestBackend) Info() backend.Info {
	return backend.Info{Mode: "code", SessionID: b.snapshot.SessionID}
}
func (b *permissionTestBackend) CodePermissions(context.Context) (backend.CodePermissionsSnapshot, error) {
	if b.fail {
		return backend.CodePermissionsSnapshot{}, fmt.Errorf("unavailable")
	}
	return b.snapshot, nil
}
func (b *permissionTestBackend) RevokeCodePermission(_ context.Context, s backend.CodePermissionScope) error {
	b.revoked = s
	if s == backend.CodePermissionPlanFullAccess {
		b.snapshot.PlanFullAccess = false
	} else {
		b.snapshot.SessionAllFolders = false
	}
	return nil
}
func (b *permissionTestBackend) ReenableCodePlanFullAccess(_ context.Context, session, plan string, revision int64) error {
	b.enabled++
	b.snapshot.PlanFullAccess = true
	b.snapshot.PlanID = plan
	return nil
}

func TestCodePermissionsRefreshDuringSaveIsNotLost(t *testing.T) {
	b := &permissionTestBackend{snapshot: backend.CodePermissionsSnapshot{SessionID: "session"}}
	m := newModel(context.Background(), Config{Backend: b, Width: 90, Height: 40})
	defer m.cancel()
	next, load := m.openCodePermissions()
	m = next.(model)
	// A lifecycle event can arrive while the overlay read or revoke is pending.
	next, _ = m.Update(eventMsg(events.NewSystem("permission changed")))
	m = next.(model)
	if !m.permissions.refreshPending {
		t.Fatal("lifecycle refresh lost while loading")
	}
	msg := load().(codePermissionsMsg)
	next, refresh := m.Update(msg)
	m = next.(model)
	if refresh == nil || m.permissions.refreshPending {
		t.Fatal("pending refresh was not drained")
	}
	b.snapshot.SessionAllFolders = true
	next, _ = m.Update(refresh())
	m = next.(model)
	if !m.permissions.snapshot.SessionAllFolders {
		t.Fatal("new authority was not refreshed")
	}
	msg.request = m.permissionRequest
	msg.snapshot.SessionID = "other"
	next, _ = m.Update(msg)
	m = next.(model)
	if m.permissions.known || m.codePermissionFooter() != "" || m.permissions.err == "" {
		t.Fatal("wrong-session projection retained authority")
	}
}

func TestCodeApprovalStaleAsyncResultPreservesNewPlanDraft(t *testing.T) {
	b := &permissionTestBackend{snapshot: backend.CodePermissionsSnapshot{SessionID: "session"}}
	m := newModel(context.Background(), Config{Backend: b, Width: 90, Height: 40})
	defer m.cancel()
	s := agent.PlanSnapshot{PlanID: "one", Revision: 1, Sequence: 1, Steps: []agent.PlanStepInfo{{Name: "one"}}}
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	m.refreshViewport()
	next, launch := m.submitPlanApproval(planOptRun)
	m = next.(model)
	started := launch().(planApprovalStartedMsg)
	s.Revision++
	s.Sequence++
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	m.refreshViewport()
	m.ta.SetValue("new plan draft")
	next, _ = m.Update(started)
	m = next.(model)
	if !m.tr.Awaiting || m.ta.Value() != "new plan draft" || m.eventCh != nil || m.planApprovalPending {
		t.Fatal("stale launch result consumed new approval")
	}
}

func TestCodePermissionsCtrlPAndModalInput(t *testing.T) {
	b := &permissionTestBackend{snapshot: backend.CodePermissionsSnapshot{SessionID: "session"}}
	m := newModel(context.Background(), Config{Backend: b, Width: 40, Height: 18})
	defer m.cancel()
	m.ta.SetValue("draft")
	m.tr.Streaming = true
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	m = next.(model)
	if !m.permissions.open || cmd == nil {
		t.Fatal("Ctrl+P unavailable during work")
	}
	next, _ = m.Update(tea.PasteMsg{Content: "ignored"})
	m = next.(model)
	if m.ta.Value() != "draft" {
		t.Fatal("modal paste reached composer")
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(model)
	m.tr.Apply(events.NewPlan("plan", events.PlanPending))
	next, _ = m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	m = next.(model)
	if m.permissions.open || m.tr.ApprovalCursor != 3 {
		t.Fatal("Ctrl+P replaced approval navigation")
	}
}

func TestCodePermissionsReenableRejectsChangedIdentityAndCompletedPlan(t *testing.T) {
	b := &permissionTestBackend{snapshot: backend.CodePermissionsSnapshot{SessionID: "session"}}
	m := newModel(context.Background(), Config{Backend: b, Width: 90, Height: 40})
	defer m.cancel()
	s := agent.PlanSnapshot{PlanID: "active", Revision: 1, Sequence: 1, Steps: []agent.PlanStepInfo{{Name: "one"}}}
	s.Document.Lifecycle = agent.PlanLifecycleApproved
	s.Document.CodeExecutionMode = agent.CodePlanRunToCompletion
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	next, cmd := m.openCodePermissions()
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	s.Revision++
	s.Sequence++
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil || b.enabled != 0 || m.permissions.confirm || m.permissions.err == "" {
		t.Fatal("confirmation was not bound to displayed identity")
	}
	s.Sequence++
	s.Steps[0].Status = "complete"
	s.Document.Results = "Verified"
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	labels, _ := m.permissionRows()
	if len(labels) != 0 {
		t.Fatalf("completed plan offered enable: %v", labels)
	}
}

func TestCodePermissionsOverlayLifecycle(t *testing.T) {
	b := &permissionTestBackend{snapshot: backend.CodePermissionsSnapshot{SessionID: "session", SessionAllFolders: true}}
	m := newModel(context.Background(), Config{Backend: b, Width: 90, Height: 40})
	defer m.cancel()
	m.ta.SetValue("my draft")
	next, cmd := m.openCodePermissions()
	m = next.(model)
	if !m.permissions.open || !m.permissions.loading {
		t.Fatal("not loading")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if !m.permissions.known || m.permissions.loading {
		t.Fatalf("load failed %+v", m.permissions)
	}
	if !strings.Contains(m.renderFooterMeta(), "Folders · session") {
		t.Fatal("missing live footer")
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if !m.permissions.loading {
		t.Fatal("revoke not pending")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if b.revoked != backend.CodePermissionSessionAllFolders || strings.Contains(m.renderFooterMeta(), "Folders · session") {
		t.Fatal("revoke not reflected")
	}
	if !strings.Contains(m.renderCodePermissions(), "Current approvals") {
		t.Fatal("empty projection")
	}
	b.fail = true
	next, cmd = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if m.permissions.err == "" || m.permissions.known {
		t.Fatal("error claimed known authority")
	}
	b.fail = false
	next, cmd = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(model)
	if m.permissions.open || m.ta.Value() != "my draft" {
		t.Fatal("closing changed draft")
	}
	m.ta.SetValue("/perm")
	m.syncSlashCompletion()
	if len(m.slash.matches) != 1 || m.slash.matches[0].Name != "permissions" {
		t.Fatal("completion missing")
	}
	m.info.Mode = "platform"
	m.syncSlashCompletion()
	if len(m.slash.matches) != 0 || m.codePermissionsCap() != nil {
		t.Fatal("remote exposed permissions")
	}
}

func TestCodePermissionsStaleRefreshAndConfirmedReenable(t *testing.T) {
	b := &permissionTestBackend{snapshot: backend.CodePermissionsSnapshot{SessionID: "session"}}
	m := newModel(context.Background(), Config{Backend: b, Width: 70, Height: 35})
	defer m.cancel()
	s := agent.PlanSnapshot{PlanID: "active", Revision: 2, Sequence: 2, Steps: []agent.PlanStepInfo{{Name: "one"}}}
	s.Document.Lifecycle = agent.PlanLifecycleApproved
	s.Document.CodeExecutionMode = agent.CodePlanReviewEachPhase
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	next, cmd := m.openCodePermissions()
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	old := m.refreshCodePermissions()
	newer := m.refreshCodePermissions()
	next, _ = m.Update(newer())
	m = next.(model)
	stale := old().(codePermissionsMsg)
	stale.snapshot.SessionAllFolders = true
	next, _ = m.Update(stale)
	m = next.(model)
	if m.permissions.snapshot.SessionAllFolders {
		t.Fatal("stale refresh won")
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil || b.enabled != 0 || !m.permissions.confirm || !strings.Contains(m.renderCodePermissions(), "Not sandboxed.") {
		t.Fatal("missing explicit confirmation")
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if b.enabled != 1 || !strings.Contains(m.renderFooterMeta(), "Full access · this plan") {
		t.Fatal("reenable not reflected")
	}
	m.tr = events.NewTranscript()
	m.syncCodePermissions()
	if m.permissions.known || m.permissions.open || m.codePermissionFooter() != "" {
		t.Fatal("session switch leaked grant")
	}
}

func TestCodePlanPermissionRepaintsCachedCard(t *testing.T) {
	m := newTestComposerModel(110)
	defer m.cancel()
	m.info.Mode = "code"
	m.tr.Apply(events.NewPlan("Short plan", events.PlanPending))
	m.refreshViewport()
	if !strings.Contains(stripANSI(m.vp.View()), "Current approvals") {
		t.Fatal("default missing")
	}
	next, _, _ := m.handlePlanApprovalKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
	m = next.(model)
	view := stripANSI(m.vp.View())
	if !strings.Contains(view, "Full access for this plan") || !strings.Contains(view, "Not") || !strings.Contains(view, "sandboxed.") {
		t.Fatalf("stale permission card: %s", stripANSI(m.vp.View()))
	}
}

func TestCodePermissionSelectionIdentityAndAtomicOptions(t *testing.T) {
	for _, mode := range []agent.CodePlanExecutionMode{agent.CodePlanRunToCompletion, agent.CodePlanReviewEachPhase} {
		b := &codePlanApprovalCaptureBackend{}
		m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 40})
		defer m.cancel()
		m.info.Mode = "code"
		s := agent.PlanSnapshot{PlanID: "permission-plan", Revision: 3, Sequence: 4, Steps: []agent.PlanStepInfo{{Name: "one"}}}
		m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
		m.ta.SetValue("draft remains")
		m.refreshViewport()
		next, _, handled := m.handlePlanApprovalKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
		m = next.(model)
		if !handled || m.planPermission != agent.CodePlanFullAccess {
			t.Fatal("p did not select full access")
		}
		m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
		m.refreshViewport()
		if m.planPermission != agent.CodePlanFullAccess {
			t.Fatal("duplicate reset selection")
		}
		if !strings.Contains(strings.Join(m.planPermissionLines(160), "\n"), "Not sandboxed.") {
			t.Fatal("warning missing")
		}
		choice := planOptRun
		if mode == agent.CodePlanReviewEachPhase {
			choice = planOptReview
		}
		next, cmd := m.submitPlanApproval(choice)
		m = next.(model)
		started := cmd().(planApprovalStartedMsg)
		if started.err != nil {
			t.Fatal(started.err)
		}
		defer started.cancel()
		if b.opts.CodePlanPermissionMode != agent.CodePlanFullAccess || b.opts.CodePlanApprovalID != s.PlanID || b.opts.CodePlanApprovalRevision != s.Revision {
			t.Fatalf("unbound approval: %+v", b.opts)
		}
		if b.opts.CodePlanExecutionMode != mode {
			t.Fatal("permission changed cadence")
		}
		if m.ta.Value() != "draft remains" {
			t.Fatal("draft lost before start")
		}
	}
}

func TestCodePermissionSelectionResetsAndFailureKeepsDraft(t *testing.T) {
	m := newTestComposerModel(100)
	defer m.cancel()
	m.info.Mode = "code"
	s := agent.PlanSnapshot{PlanID: "one", Revision: 1, Sequence: 1, Steps: []agent.PlanStepInfo{{Name: "one"}}}
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	m.refreshViewport()
	next, _, _ := m.handlePlanApprovalKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
	m = next.(model)
	m.planApprovalPending = true
	m.ta.SetValue("keep me")
	next, _ = m.Update(planApprovalStartedMsg{err: fmt.Errorf("disk full")})
	m = next.(model)
	if m.planPermission != agent.CodePlanFullAccess || !m.tr.Awaiting || m.ta.Value() != "keep me" || m.planApprovalPending {
		t.Fatal("failure destroyed retry state")
	}
	s.Revision++
	s.Sequence++
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	m.refreshViewport()
	if m.planPermission != agent.CodePlanCurrentApprovals {
		t.Fatal("revision retained escalation")
	}
}

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
	if len(lines) != 10 {
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
