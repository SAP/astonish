package events

import "testing"

func TestDelegationMultiBatchSummaryAndRestore(t *testing.T) {
	tr := NewTranscript()
	a := NewDelegationStart([]DelegationTask{{BatchID: "a", TaskID: "t", Name: "same"}})
	b := NewDelegationStart([]DelegationTask{{BatchID: "b", TaskID: "t", Name: "same"}})
	tr.Apply(a)
	tr.Apply(b)
	tr.Apply(Event{Kind: KindDelegation, DelegationType: "task_complete", DelegationBatchID: "b", DelegationTaskID: "t", DelegationAttempt: 1})
	if !tr.DelegationActive || len(tr.Delegation) != 2 {
		t.Fatalf("earlier active batch lost: %+v", tr.Delegation)
	}
	tr.Apply(Event{Kind: KindDelegation, DelegationType: "done", DelegationBatchID: "b"})
	if !tr.DelegationActive {
		t.Fatal("done on b hid active a")
	}
	tr.Apply(Event{Kind: KindDelegation, DelegationType: "task_failed", DelegationBatchID: "a", DelegationTaskID: "t", DelegationAttempt: 1})
	tr.Apply(Event{Kind: KindDelegation, DelegationType: "task_start", DelegationBatchID: "a", DelegationTaskID: "t", DelegationAttempt: 2})
	if len(tr.Delegation) != 2 || tr.Delegation[0].Attempt != 2 || len(tr.Items[0].DelegationTasks) != 2 {
		t.Fatalf("retry counts: %+v", tr.Delegation)
	}
	tr.LoadHistory([]HistoryMsg{{Delegation: &a}, {Delegation: &b}})
	if tr.DelegationActive || tr.Delegation[0].Status != "interrupted" {
		t.Fatalf("restored workers incorrectly live: %+v", tr.Delegation)
	}
}
