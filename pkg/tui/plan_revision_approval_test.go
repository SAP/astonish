package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestRevisedPlanApprovalPresentation(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
	m.info.Mode = "code"
	m.ready = true
	m.graphPlanMode = true
	m.layout()
	s := agent.PlanSnapshot{PlanID: "revise", Revision: 1, Sequence: 1, Goal: "Old plan", Steps: []agent.PlanStepInfo{{Name: "one", Description: "old", Status: "pending"}}}
	m.tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	m.tr.ResolvePlan(events.PlanChangesRequested)
	m.tr.Apply(events.NewUser("change scrolling"))
	m.tr.Apply(events.NewText(strings.Repeat("discussion\n", 80)))
	m.tr.Apply(events.NewDone())
	m.ta.SetValue("draft preserved")
	m.refreshViewport()
	m.vp.SetYOffset(0)
	m.userScrolledUp = true
	s.Revision++
	s.Sequence++
	s.Goal = "Revised scrolling plan"
	s.Steps[0].Description = "Reveal every new phase"
	next, _ := m.Update(eventMsg(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s}))
	m = next.(model)
	if !m.tr.Awaiting || m.tr.ApprovalIdx != len(m.tr.Items)-1 {
		t.Fatalf("revised approval not attached to current card: %+v", m.tr)
	}
	plain := stripANSI(m.View().Content)
	for _, want := range []string{"Revised scrolling plan", "Plan Ready", planOptRun, planOptReview, "Request changes", "Decline"} {
		if !strings.Contains(plain, want) {
			t.Errorf("revised presentation missing %q:\n%s", want, plain)
		}
	}
	if m.ta.Value() != "draft preserved" {
		t.Fatal("draft lost")
	}
	oldOffset := m.vp.YOffset()
	next, _ = m.Update(eventMsg(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s}))
	m = next.(model)
	if m.vp.YOffset() != oldOffset {
		t.Fatal("replay moved viewport")
	}
	next, _, handled := m.handlePlanApprovalKey(tea.KeyPressMsg{Code: 'r'})
	m = next.(model)
	if !handled || m.tr.Awaiting || !m.graphPlanMode || m.ta.Placeholder != planChangesHint {
		t.Fatal("request changes did not restore planning composer")
	}
}
