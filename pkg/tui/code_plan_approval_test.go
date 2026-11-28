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
	if m.permissions.known || m.codePermissionFooter() != "Folders ?  Tools ?" || m.permissions.err == "" {
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
	if m.permissions.open || m.tr.ApprovalCursor != 5 {
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
	m.permissions.cursor = 2 // Plan access is a stable row after Folders and Tools.
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
	if m.codePermissionRows()[2].actionable {
		t.Fatal("completed plan offered enable")
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
	if !strings.Contains(m.renderFooterMeta(), "Folders ✓") {
		t.Fatal("missing live footer")
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if !m.permissions.loading {
		t.Fatal("revoke not pending")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if b.revoked != backend.CodePermissionSessionAllFolders || strings.Contains(m.renderFooterMeta(), "Folders ✓") {
		t.Fatal("revoke not reflected")
	}
	if !strings.Contains(m.renderCodePermissions(), "Ask outside project") {
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
	m.permissions.cursor = 2
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil || b.enabled != 0 || !m.permissions.confirm || !strings.Contains(m.renderCodePermissions(), "Not sandboxed.") {
		t.Fatal("missing explicit confirmation")
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if b.enabled != 1 || !strings.Contains(m.renderFooterMeta(), "Folders ✓  Tools ✓") {
		t.Fatal("reenable not reflected")
	}
	m.tr = events.NewTranscript()
	m.syncCodePermissions()
	if m.permissions.known || m.permissions.open || m.codePermissionFooter() != "Folders ?  Tools ?" {
		t.Fatal("session switch leaked grant")
	}
}

func TestCodePlanApprovalSelectedDescription(t *testing.T) {
	want := []string{
		"Run all verified phases using current tool and folder approvals.",
		"Run all verified phases without tool or folder prompts for this plan. Not sandboxed.",
		"Pause after each verified phase, using current tool and folder approvals.",
		"Pause after each verified phase; no tool or folder prompts for this plan. Not sandboxed.",
		"Return to planning so you can describe the changes needed.",
		"Do not execute this plan; return to Normal mode.",
	}
	for _, width := range []int{40, 120} {
		m := newTestComposerModel(width)
		m.info.Mode = "code"
		m.tr.Apply(events.NewPlan("Short plan", events.PlanPending))
		m.refreshViewport()
		for i, description := range want {
			m.tr.ApprovalCursor = i
			for name, surface := range map[string]string{
				"card":    strings.Join(m.planApprovalLines(*m.approvalItem(), width-10), "\n"),
				"footer":  m.renderPlanApprovalFooter(),
				"overlay": m.renderPlanApprovalOverlay(m.approvalItem()),
			} {
				plain := stripANSI(surface)
				// Borders and padding separate wrapped lines but no prose may be lost.
				plain = strings.Map(func(r rune) rune {
					if strings.ContainsRune("│╭╮╰╯─", r) {
						return ' '
					}
					return r
				}, plain)
				plain = strings.Join(strings.Fields(plain), " ")
				if strings.Count(plain, description) != 1 {
					t.Fatalf("%s width=%d cursor=%d selected explanation missing/duplicated: %s", name, width, i, plain)
				}
				for j, other := range want {
					if i != j && strings.Contains(plain, other) {
						t.Fatalf("%s includes unselected description %d", name, j)
					}
				}
				if strings.Contains(plain, "p change") {
					t.Fatal("obsolete permission toggle")
				}
			}
		}
		m.planApprovalPending = true
		if got := strings.Join(m.planApprovalDescriptionLines(120), " "); !strings.Contains(got, want[5]) || !strings.Contains(got, "Saving approval…") {
			t.Fatalf("pending description: %s", got)
		}
		m.info.AutoApprove = true
		if got := strings.Join(m.planApprovalDescriptionLines(160), " "); strings.Count(got, "safe choices do not disable it") != 1 {
			t.Fatalf("override notice: %s", got)
		}
		m.cancel()
	}
}

func TestCodePlanApprovalSafeAccelerators(t *testing.T) {
	for _, key := range []string{"y", "Y", "1"} {
		b := &codePlanApprovalCaptureBackend{}
		m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 40})
		m.info.Mode = "code"
		s := agent.PlanSnapshot{PlanID: "safe", Revision: 2, Sequence: 3, Steps: []agent.PlanStepInfo{{Name: "one"}}}
		m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
		m.refreshViewport()
		m.tr.ApprovalCursor = 3
		next, cmd, handled := m.handlePlanApprovalKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
		m = next.(model)
		if !handled || cmd != nil || m.tr.ApprovalCursor != 3 || b.message != "" {
			t.Fatal("p changed or dispatched approval")
		}
		next, cmd, handled = m.handlePlanApprovalKey(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		m = next.(model)
		if !handled || cmd == nil {
			t.Fatalf("%s did not submit", key)
		}
		started := cmd().(planApprovalStartedMsg)
		if started.err != nil {
			t.Fatal(started.err)
		}
		if b.opts.CodePlanPermissionMode != agent.CodePlanCurrentApprovals || b.opts.CodePlanExecutionMode != agent.CodePlanRunToCompletion || b.opts.CodePlanApprovalID != "safe" || b.opts.CodePlanApprovalRevision != 2 {
			t.Fatalf("%s inherited highlighted authority: %+v", key, b.opts)
		}
		started.cancel()
		m.cancel()
	}
}

func TestCodePlanApprovalNarrowMouseCatalog(t *testing.T) {
	m := newTestComposerModel(30)
	defer m.cancel()
	m.info.Mode = "code"
	m.tr.Apply(events.NewPlan("Short plan", events.PlanPending))
	m.refreshViewport()
	for i, option := range m.approvalOptions() {
		prefix := strings.SplitN(m.planApprovalLabel(option, i), "  ", 2)[0]
		found := false
		for line, text := range m.transcriptPlainLines {
			if col := strings.Index(text, prefix+"  "); col >= 0 {
				got, ok := m.codePlanApprovalAtPoint(selectionPoint{line: line, col: len([]rune(text[:col])) + 1})
				if !ok || got != i {
					t.Fatalf("option %d narrow hit=%d/%t: %q", i, got, ok, text)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("narrow option %d absent", i)
		}
	}
}

func TestCodePlanApprovalExplicitAccessChoices(t *testing.T) {
	m := newTestComposerModel(110)
	defer m.cancel()
	m.info.Mode = "code"
	m.tr.Apply(events.NewPlan("Short plan", events.PlanPending))
	m.refreshViewport()
	want := []string{"Approve and run", "Approve and run with full access", "Approve and review each phase", "Approve and review with full access", "Request changes", "Decline"}
	if !reflect.DeepEqual(m.approvalOptions(), want) {
		t.Fatalf("catalog = %v", m.approvalOptions())
	}
}

func TestCodePlanPermissionRepaintsCachedCard(t *testing.T) {
	m := newTestComposerModel(110)
	defer m.cancel()
	m.info.Mode = "code"
	m.tr.Apply(events.NewPlan("Short plan", events.PlanPending))
	m.refreshViewport()
	if !strings.Contains(stripANSI(m.vp.View()), "Run all verified phases using current tool and folder approvals.") {
		t.Fatal("default missing")
	}
	next, _, _ := m.handlePlanApprovalKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(model)
	view := stripANSI(m.vp.View())
	if !strings.Contains(view, "without tool or folder prompts") || !strings.Contains(view, "Not") || !strings.Contains(view, "sandboxed.") {
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
		next, _, handled := m.handlePlanApprovalKey(tea.KeyPressMsg{Code: tea.KeyDown})
		m = next.(model)
		if !handled || m.tr.ApprovalCursor != 1 {
			t.Fatal("Down did not highlight full access")
		}
		m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
		m.refreshViewport()
		if m.tr.ApprovalCursor != 1 {
			t.Fatal("duplicate reset selection")
		}
		if !strings.Contains(strings.Join(m.planApprovalDescriptionLines(160), "\n"), "Not sandboxed.") {
			t.Fatal("warning missing")
		}
		choice := planOptRunFull
		if mode == agent.CodePlanReviewEachPhase {
			choice = planOptReviewFull
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
	next, _, _ := m.handlePlanApprovalKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(model)
	m.planApprovalPending = true
	m.ta.SetValue("keep me")
	next, _ = m.Update(planApprovalStartedMsg{err: fmt.Errorf("disk full")})
	m = next.(model)
	if m.tr.ApprovalCursor != 1 || !m.tr.Awaiting || m.ta.Value() != "keep me" || m.planApprovalPending {
		t.Fatal("failure destroyed retry state")
	}
	s.Revision++
	s.Sequence++
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	m.refreshViewport()
	if m.tr.ApprovalCursor != 0 {
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
	m.refreshViewport()
	m.tr.ApprovalCursor = 1
	want := []string{
		"✦ Plan Ready · choose how to proceed",
		"  1/y  Approve and run",
		"▸ 2    Approve and run with full access",
		"  3    Approve and review each phase",
		"  4    Approve and review with full access",
		"  5/r  Request changes",
		"  6/n  Decline",
	}
	lines := m.planApprovalLines(*m.approvalItem(), 90)
	for i, line := range want {
		if got := stripANSI(lines[i+3]); got != line {
			t.Fatalf("option row %d = %q, want %q", i, got, line)
		}
	}
	if len(lines) != 12 {
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
		permission      agent.CodePlanPermissionMode
		status          events.PlanStatus
	}{
		{name: "default", mode: "code", want: agent.CodePlanRunToCompletion},
		{name: "review", mode: "code", down: 2, want: agent.CodePlanReviewEachPhase},
		{name: "review accelerator", mode: "code", key: "3", want: agent.CodePlanReviewEachPhase},
		{name: "run full accelerator", mode: "code", key: "2", want: agent.CodePlanRunToCompletion, permission: agent.CodePlanFullAccess},
		{name: "review full accelerator", mode: "code", key: "4", want: agent.CodePlanReviewEachPhase, permission: agent.CodePlanFullAccess},
		{name: "run accelerator", mode: "code", key: "y", want: agent.CodePlanRunToCompletion},
		{name: "changes", mode: "code", key: "5", status: events.PlanChangesRequested},
		{name: "decline", mode: "code", key: "6", status: events.PlanDeclined},
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
			if tc.mode == "code" {
				wantPermission := tc.permission
				if wantPermission == "" {
					wantPermission = agent.CodePlanCurrentApprovals
				}
				if capture.opts.CodePlanPermissionMode != wantPermission {
					t.Fatalf("permission = %q, want %q", capture.opts.CodePlanPermissionMode, wantPermission)
				}
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
		if len(opts) != 6 {
			t.Fatalf("Code options %v", opts)
		}
		m.syncCodePermissions()
		m.permissions.known = true
		for _, idx := range []int{1, 2, 3, 4, 5, 0} {
			next, cmd, _ := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyDown})
			m = next.(model)
			if cmd != nil || capture.message != "" || m.codePermissionFooter() != "Folders ○  Tools ○" {
				t.Fatal("navigation dispatched execution or projected authority")
			}
			selected := "▸ " + m.planApprovalLabel(opts[idx], idx)
			if got := stripANSI(m.vp.View()); !strings.Contains(got, selected) {
				t.Fatalf("width %d cursor %d missing %q: %s", width, idx, selected, got)
			}
		}
		var point selectionPoint
		found := false
		for line, text := range m.transcriptPlainLines {
			if col := strings.Index(text, "3    Approve and review each phase"); col >= 0 {
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
