package events

import (
	"github.com/SAP/astonish/pkg/agent"
	"testing"
)

func TestPlanningRevisionReopensCurrentCardOnlyOnce(t *testing.T) {
	tr := NewTranscript()
	s := agent.PlanSnapshot{PlanID: "p", Revision: 1, Sequence: 1, Goal: "initial", Steps: []agent.PlanStepInfo{{Name: "one"}}}
	tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &s})
	tr.ResolvePlan(PlanChangesRequested)
	tr.Apply(NewUser("revise"))
	s.Revision++
	s.Sequence++
	s.Goal = "changed"
	ev := Event{Kind: KindPlanSnapshot, PlanSnapshot: &s}
	tr.Apply(ev)
	if len(tr.Items) != 2 || tr.ApprovalIdx != 1 || !tr.Awaiting || tr.Items[1].PlanSnapshot.Goal != "changed" {
		t.Fatalf("missing revised card: %+v", tr)
	}
	tr.ResolvePlan(PlanDeclined)
	tr.Apply(ev)
	if tr.Awaiting || len(tr.Items) != 2 {
		t.Fatal("replay reopened approval")
	}
	s.Sequence++
	tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &s})
	if tr.Awaiting {
		t.Fatal("same-revision progress reopened approval")
	}
}
