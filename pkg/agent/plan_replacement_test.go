package agent

import (
	"reflect"
	"testing"
)

func TestPlanReplacementAnyLifecycle(t *testing.T) {
	for _, status := range []string{"", PlanLifecycleApproved, PlanLifecycleCompleted} {
		t.Run(status, func(t *testing.T) {
			old := runtimeFixture()
			old.Document.Lifecycle = status
			old.Document.Results = "old evidence"
			old.Document.GraphAcceptance = "old acceptance"
			r := NewPlanRuntimeFromSnapshot(old, PlanRuntimeOptions{})
			previous := r.PlanState()
			before := snapshotForReplacementTest(previous)
			if err := r.AllowInitialPlanReplacement(); err != nil {
				t.Fatal(err)
			}
			doc := PlanDocumentInfo{Context: "New design", WhatNotToDo: "Keep unrelated work", Verification: "New acceptance", GraphAcceptance: "New acceptance"}
			if err := r.Announce("New plan", doc, old.Steps); err != nil {
				t.Fatal(err)
			}
			got := r.Snapshot()
			if got.PlanID == old.PlanID || got.Revision != 1 || got.Document.Lifecycle != "" || got.Document.Results != "" || got.Document.GraphAcceptance != "New acceptance" {
				t.Fatalf("replacement inherited old state: %+v", got)
			}
			for _, step := range got.Steps {
				if step.Status != "pending" || step.Evidence != "" {
					t.Fatal("inherited evidence")
				}
			}
			if r.PlanState() == previous || !reflect.DeepEqual(snapshotForReplacementTest(previous), before) {
				t.Fatal("old plan mutated")
			}
		})
	}
}

func TestPlanReplacementDiscardsInflightVerification(t *testing.T) {
	s := runtimeFixture()
	for i := range s.Steps {
		s.Steps[i].Status = "pending"
		s.Steps[i].Evidence = ""
	}
	started, release := make(chan struct{}), make(chan struct{})
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{Verify: func(string) (int, string, error) { close(started); <-release; return 0, "old verify", nil }})
	done := make(chan PlanStepApplyResult, 1)
	go func() { done <- r.UpdateStep(s.Steps[0].Name, "complete") }()
	<-started
	if err := r.AllowInitialPlanReplacement(); err != nil {
		t.Fatal(err)
	}
	if err := r.Announce("Fresh", s.Document, s.Steps); err != nil {
		t.Fatal(err)
	}
	before := r.Snapshot()
	close(release)
	if result := <-done; result.Code != PlanRuntimeStale {
		t.Fatalf("old verification accepted: %+v", result)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) {
		t.Fatal("late verification changed replacement")
	}
}

func snapshotForReplacementTest(p *PlanState) PlanSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return snapshotPlanLocked(p)
}
