package events

import "testing"

func TestTranscriptPlanIsAtomicAndResolvesInPlace(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewPlan("# Execution Plan\n\n**Goal:** Atomic plan", PlanPending))

	if len(tr.Items) != 1 {
		t.Fatalf("items = %d, want one atomic plan item", len(tr.Items))
	}
	if tr.Items[0].Kind != ItemPlan || tr.ApprovalIdx != 0 || !tr.Awaiting {
		t.Fatalf("pending plan state = %+v, awaiting=%v idx=%d", tr.Items[0], tr.Awaiting, tr.ApprovalIdx)
	}

	tr.ResolvePlan(PlanApproved)
	if tr.Awaiting || tr.ApprovalIdx != -1 {
		t.Fatalf("resolved plan still awaiting: awaiting=%v idx=%d", tr.Awaiting, tr.ApprovalIdx)
	}
	if len(tr.Items) != 1 || tr.Items[0].PlanStatus != PlanApproved {
		t.Fatalf("resolved plan was not retained in place: %+v", tr.Items)
	}
}

func TestTranscriptLoadHistoryRestoresPendingPlan(t *testing.T) {
	tr := NewTranscript()
	tr.LoadHistory([]HistoryMsg{{
		Kind:       "plan",
		Text:       "# Execution Plan\n\n**Goal:** Resume me",
		PlanStatus: PlanPending,
		Options:    []string{"Approve & implement", "Request changes", "Decline"},
	}})

	if len(tr.Items) != 1 || tr.Items[0].Kind != ItemPlan {
		t.Fatalf("history items = %+v, want one plan", tr.Items)
	}
	if !tr.Awaiting || tr.ApprovalIdx != 0 {
		t.Fatalf("pending history not restored: awaiting=%v idx=%d", tr.Awaiting, tr.ApprovalIdx)
	}
}

func TestTranscriptLoadHistoryKeepsSettledPlanWithoutAwaiting(t *testing.T) {
	for _, status := range []PlanStatus{PlanApproved, PlanChangesRequested, PlanDeclined} {
		t.Run(string(status), func(t *testing.T) {
			tr := NewTranscript()
			tr.LoadHistory([]HistoryMsg{{Kind: "plan", Text: "plan", PlanStatus: status}})
			if tr.Awaiting || tr.ApprovalIdx != -1 {
				t.Fatalf("settled plan restored as pending: awaiting=%v idx=%d", tr.Awaiting, tr.ApprovalIdx)
			}
			if len(tr.Items) != 1 || tr.Items[0].PlanStatus != status {
				t.Fatalf("settled status not retained: %+v", tr.Items)
			}
		})
	}
}
