package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestInitialPlanSnapshotEnter(t *testing.T) {
	for _, source := range []string{"live", "replayed", "restored"} {
		for _, tc := range []struct {
			name   string
			down   int
			status events.PlanStatus
		}{
			{"approve", 0, events.PlanApproved},
			{"request changes", 1, events.PlanChangesRequested},
			{"decline", 2, events.PlanDeclined},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				capture := &planApprovalCaptureBackend{}
				m := newModel(context.Background(), Config{Backend: capture, Width: 100, Height: 30})
				m.ready = true
				m.layout()
				m.graphPlanMode = true
				s := agent.PlanSnapshot{PlanID: "p", Revision: 1, Sequence: 1, Goal: "initial", Steps: []agent.PlanStepInfo{{Name: "one"}}}
				ev := events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s}
				if source == "restored" {
					m.tr.LoadHistory([]events.HistoryMsg{{Kind: "delegation", Delegation: &ev}})
				} else {
					m.tr.Apply(ev)
					if source == "replayed" {
						m.tr.Apply(ev)
					}
				}
				plain := stripANSI(m.renderPlanDocument(m.tr.Items[0], 90))
				for _, label := range []string{"Approve & implement", "Request changes", "Decline"} {
					if !strings.Contains(plain, label) {
						t.Errorf("initial plan card missing %q", label)
					}
				}
				for i := 0; i < tc.down; i++ {
					next, _, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyDown})
					if !handled {
						t.Fatal("Down not handled")
					}
					m = next.(model)
				}
				next, cmd, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				if !handled {
					t.Fatal("Enter not handled")
				}
				m = next.(model)
				if tc.status == events.PlanApproved {
					if cmd == nil {
						t.Fatalf("Enter did not launch implementation; plan status = %s", m.tr.Items[0].PlanStatus)
					}
					started, ok := cmd().(planApprovalStartedMsg)
					if !ok || started.err != nil {
						t.Fatalf("approval launch: %+v", started)
					}
					defer started.cancel()
					next, _ = m.Update(started)
					m = next.(model)
					if !capture.opts.ApprovedPlanExecution || capture.opts.PlanRevisionDecision != nil {
						t.Fatalf("wrong execution options: %+v", capture.opts)
					}
				} else if cmd != nil || capture.opts.ApprovedPlanExecution {
					t.Fatal("non-approval launched implementation")
				}
				if m.tr.Items[0].PlanStatus != tc.status || m.tr.Awaiting {
					t.Fatalf("Enter status = %s, awaiting = %t; want %s", m.tr.Items[0].PlanStatus, m.tr.Awaiting, tc.status)
				}
				if tc.status == events.PlanChangesRequested {
					if !m.graphPlanMode || m.ta.Placeholder != planChangesHint {
						t.Fatal("request changes did not preserve planning composer")
					}
				} else if m.graphPlanMode || m.planMode {
					t.Fatal("approval/decline did not return to Normal mode")
				}
			})
		}
	}
}

func TestRenderPlanCardOwnsPendingApprovalControls(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 90, Height: 30})
	m.ready = true
	m.layout()
	item := events.Item{
		Kind:         events.ItemPlan,
		Content:      samplePlanContent,
		ApprovalKind: "plan",
		PlanStatus:   events.PlanPending,
		Options:      []string{planOptApprove, planOptRequest, planOptDecline},
	}

	plain := stripANSI(m.renderPlanDocument(item, 80))
	for _, want := range []string{"Plan Ready", planOptApprove, planOptRequest, planOptDecline} {
		if !strings.Contains(plain, want) {
			t.Fatalf("atomic plan card missing %q:\n%s", want, plain)
		}
	}
}

func TestRenderPlanCardReplacesControlsWithSettledStatus(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 90, Height: 30})
	m.ready = true
	m.layout()
	item := events.Item{Kind: events.ItemPlan, Content: samplePlanContent, ApprovalKind: "plan", PlanStatus: events.PlanApproved}

	plain := stripANSI(m.renderPlanDocument(item, 80))
	if !strings.Contains(plain, "Approved") {
		t.Fatalf("settled plan missing Approved status:\n%s", plain)
	}
	if strings.Contains(plain, planOptRequest) || strings.Contains(plain, planOptDecline) {
		t.Fatalf("settled plan still shows approval controls:\n%s", plain)
	}
}

// Historical pending proposals are decode-only. They must never hijack the
// composer, redraw a review, or apply their unaccepted candidate definition.
func TestPlanUpdatesStayQuiet(t *testing.T) {
	for _, source := range []string{"live", "replayed", "restored"} {
		t.Run(source, func(t *testing.T) {
			m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
			m.ready = true
			m.layout()
			s := agent.PlanSnapshot{PlanID: "quiet", Revision: 2, Sequence: 5, Goal: "Accepted Hello", Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleExecuting}, Steps: []agent.PlanStepInfo{{Name: "greeting", Description: "Write greeting", Details: strings.Repeat("accepted details\n", 70), Status: "pending"}}}
			s.PendingRevision = &agent.PlanRevisionProposal{ID: "old", BaseRevision: 2, Reason: "legacy review", Candidate: agent.PlanDefinition{Goal: "UNACCEPTED CANDIDATE"}}
			ev := events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s}
			if source == "restored" {
				m.tr.LoadHistory([]events.HistoryMsg{{Kind: "delegation", Delegation: &ev}})
			} else {
				m.tr.Apply(ev)
			}
			m.tr.Items[0].Expanded = true
			m.ta.SetValue("draft stays here")
			m.refreshViewport()
			m.vp.SetYOffset(5)
			offset := m.vp.YOffset()
			focused := m.ta.Focused()
			if source == "replayed" {
				next, _ := m.Update(eventMsg(ev))
				m = next.(model)
			}
			if m.tr.Awaiting || m.tr.ApprovalIdx != -1 || len(m.tr.Items[0].Options) != 0 {
				t.Fatal("legacy snapshot opened approval")
			}
			old := s.Clone()
			s.PendingRevision = nil
			s.Revision++
			s.Sequence++
			s.Goal = "Accepted HelloWorld"
			next, _ := m.Update(eventMsg(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s}))
			m = next.(model)
			next, _ = m.Update(eventMsg(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &old}))
			m = next.(model)
			if len(m.tr.Items) != 1 || m.tr.Items[0].PlanSnapshot.Goal != s.Goal || !m.tr.Items[0].Expanded {
				t.Fatal("plan was duplicated, rolled back or collapsed")
			}
			if m.vp.YOffset() != offset || m.ta.Value() != "draft stays here" || m.ta.Focused() != focused {
				t.Fatal("snapshot stole scroll, draft or focus")
			}
			plain := stripANSI(m.renderPlanDocument(m.tr.Items[0], 90) + m.View().Content + m.renderHints())
			for _, forbidden := range []string{"Approve revision", "Reject revision", "Request changes", "Revision approval", "BEFORE:", "AFTER:", "UNACCEPTED CANDIDATE", "legacy review"} {
				if strings.Contains(plain, forbidden) {
					t.Fatalf("unexpected review surface %q", forbidden)
				}
			}
			for _, key := range []rune{'y', 'n', 'r', '1', '2', '3'} {
				_, _, handled := m.handleApprovalKey(tea.KeyPressMsg{Code: key})
				if handled {
					t.Fatalf("revision key %q hijacked composer", key)
				}
			}
		})
	}
}
