package events

import (
	"slices"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
)

func TestTranscriptPlanIsAtomicAndResolvesInPlace(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewPlan("# Execution Plan\n\n**Goal:** Atomic plan", PlanPending))
	if len(tr.Items) != 1 || tr.Items[0].Kind != ItemPlan || tr.ApprovalIdx != 0 || !tr.Awaiting {
		t.Fatalf("pending: %+v", tr)
	}
	tr.ResolvePlan(PlanApproved)
	if tr.Awaiting || tr.ApprovalIdx != -1 || len(tr.Items) != 1 || tr.Items[0].PlanStatus != PlanApproved {
		t.Fatalf("resolved: %+v", tr)
	}
}
func TestTranscriptLoadHistoryRestoresPendingPlan(t *testing.T) {
	tr := NewTranscript()
	tr.LoadHistory([]HistoryMsg{{Kind: "plan", Text: "# Execution Plan\n\n**Goal:** Resume me", PlanStatus: PlanPending, Options: []string{"Approve & implement", "Request changes", "Decline"}}})
	if len(tr.Items) != 1 || tr.Items[0].Kind != ItemPlan || !tr.Awaiting || tr.ApprovalIdx != 0 {
		t.Fatalf("history: %+v", tr)
	}
}
func TestTranscriptLoadHistoryKeepsSettledPlanWithoutAwaiting(t *testing.T) {
	for _, status := range []PlanStatus{PlanApproved, PlanChangesRequested, PlanDeclined} {
		t.Run(string(status), func(t *testing.T) {
			tr := NewTranscript()
			tr.LoadHistory([]HistoryMsg{{Kind: "plan", Text: "plan", PlanStatus: status}})
			if tr.Awaiting || tr.ApprovalIdx != -1 || len(tr.Items) != 1 || tr.Items[0].PlanStatus != status {
				t.Fatalf("history: %+v", tr)
			}
		})
	}
}
func TestInitialPlanApprovalOptions(t *testing.T) {
	want := []string{"Approve & implement", "Request changes", "Decline"}
	s := agent.PlanSnapshot{PlanID: "p", Revision: 1, Sequence: 1, Goal: "initial", Steps: []agent.PlanStepInfo{{Name: "one"}}}
	ev := Event{Kind: KindPlanSnapshot, PlanSnapshot: &s}
	for name, load := range map[string]func(*Transcript){
		"live snapshot":     func(tr *Transcript) { tr.Apply(ev) },
		"replayed snapshot": func(tr *Transcript) { tr.Apply(ev); tr.Apply(ev) },
		"restored snapshot": func(tr *Transcript) { tr.LoadHistory([]HistoryMsg{{Kind: "delegation", Delegation: &ev}}) },
		"live plan":         func(tr *Transcript) { tr.Apply(NewPlan("initial", PlanPending)) },
		"legacy approval without options": func(tr *Transcript) {
			tr.Apply(NewPlan("initial", PlanPending))
			tr.Apply(Event{Kind: KindApproval, ApprovalKind: "plan"})
		},
		"restored plan without options": func(tr *Transcript) {
			tr.LoadHistory([]HistoryMsg{{Kind: "plan", Text: "initial", PlanStatus: PlanPending}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			tr := NewTranscript()
			load(tr)
			if len(tr.Items) != 1 || !tr.Awaiting || tr.ApprovalIdx != 0 || tr.ApprovalCursor != 0 {
				t.Fatalf("initial approval state: %+v", tr)
			}
			if !slices.Equal(tr.Items[0].Options, want) {
				t.Fatalf("initial plan options = %q, want %q", tr.Items[0].Options, want)
			}
		})
	}
}

func TestPlanSnapshotInitialApprovalRemainsAtomic(t *testing.T) {
	tr := NewTranscript()
	s := agent.PlanSnapshot{PlanID: "p", Revision: 1, Sequence: 1, Goal: "initial", Steps: []agent.PlanStepInfo{{Name: "one"}}}
	tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &s})
	if !tr.Awaiting || tr.ApprovalIdx != 0 || len(tr.Items) != 1 || tr.Items[0].PlanStatus != PlanPending || tr.Items[0].ApprovalKind != "plan" {
		t.Fatalf("initial snapshot lost approval: %+v", tr)
	}
	tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &s})
	if len(tr.Items) != 1 {
		t.Fatal("replayed snapshot duplicated approval")
	}
	tr.ResolvePlan(PlanDeclined)
	s.Sequence++
	tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &s})
	if tr.Awaiting {
		t.Fatal("snapshot reopened declined initial approval")
	}
}

func TestPlanRevisionSnapshotUpsertAndRestore(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewPlan("initial", PlanPending))
	tr.Items[0].Expanded, tr.Items[0].RoutingModel = true, "strong-model"
	s := agent.PlanSnapshot{PlanID: "p", Revision: 2, Sequence: 4, Goal: "accepted", Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleExecuting}, Steps: []agent.PlanStepInfo{{Name: "one", Status: "complete", Evidence: "exit 0"}}}
	tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &s})
	if tr.Awaiting {
		t.Fatal("durable accepted snapshot left initial approval open")
	}
	s.PendingRevision = &agent.PlanRevisionProposal{ID: "proposal", BaseRevision: 2, Reason: "scope", Candidate: s.Definition()}
	s.PendingRevision.Candidate.Goal = "candidate"
	s.Sequence++
	tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &s})
	tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &s})
	old := s.Clone()
	old.Sequence--
	old.Goal = "stale"
	tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &old})
	if len(tr.Items) != 1 || tr.Awaiting || tr.Items[0].PlanSnapshot.Goal != "accepted" || !tr.Items[0].Expanded || tr.Items[0].RoutingModel != "strong-model" || tr.Items[0].PlanSnapshot.Steps[0].Evidence != "exit 0" {
		t.Fatalf("state: %+v", tr)
	}
	ev := Event{Kind: KindPlanSnapshot, PlanSnapshot: &s}
	tr.LoadHistory([]HistoryMsg{{Kind: "delegation", Delegation: &ev}, {Kind: "system", Text: "compacted"}})
	if tr.Awaiting || len(tr.Items[0].Options) != 0 {
		t.Fatal("legacy pending revision blocked history")
	}
	s.PendingRevision = nil
	s.Sequence++
	tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &s})
	if tr.Awaiting {
		t.Fatal("authoritative resolution did not clear controls")
	}
}
