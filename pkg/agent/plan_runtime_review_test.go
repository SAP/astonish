package agent

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestPlanRuntimeUnapprovedExecution(t *testing.T) {
	s := runtimeFixture()
	s.Document.Lifecycle = ""
	for i := range s.Steps {
		s.Steps[i].Status = "pending"
		s.Steps[i].Evidence = ""
	}
	calls := 0
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{Verify: func(string) (int, string, error) { calls++; return 0, "", nil }})
	before := r.Snapshot()
	if name, err := r.AdvanceOnToolStart(); err != nil || name != "" {
		t.Fatalf("unapproved auto-start: %q %v", name, err)
	}
	if _, err := r.RegisterDelegate("one", "worker"); PlanRuntimeErrorCode(err) != PlanRuntimeUnapproved {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "complete"} {
		result := r.UpdateStep("one", status)
		if result.Code == PlanStepOK {
			t.Errorf("unapproved %s accepted", status)
		}
	}
	if calls != 0 || !reflect.DeepEqual(before, r.Snapshot()) {
		t.Fatalf("unapproved execution: calls=%d", calls)
	}
}

func TestPlanRuntimeInitialReplacement(t *testing.T) {
	s := runtimeFixture()
	s.Document.Lifecycle = ""
	for i := range s.Steps {
		s.Steps[i].Status = "pending"
		s.Steps[i].Evidence = ""
	}
	fail := false
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CommitSnapshot: func(PlanSnapshot) error {
		if fail {
			return errors.New("disk")
		}
		return nil
	}})
	doc := s.Document
	doc.Context = "updated design"
	if err := r.Announce("updated", doc, s.Steps); err == nil {
		t.Fatal("unauthorized replacement")
	}
	if err := r.AllowInitialPlanReplacement(); err != nil {
		t.Fatal(err)
	}
	before := r.Snapshot()
	fail = true
	if err := r.Announce("updated", doc, s.Steps); PlanRuntimeErrorCode(err) != PlanRuntimePersistFailed {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) {
		t.Fatal("failed save published")
	}
	fail = false
	if err := r.Announce("updated", doc, s.Steps); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().Goal != "updated" || r.Snapshot().Document.Lifecycle != "" {
		t.Fatal("replacement lost")
	}
	if err := r.Announce("again", doc, s.Steps); err == nil {
		t.Fatal("authorization reused")
	}
	if err := r.AllowInitialPlanReplacement(); err != nil {
		t.Fatal(err)
	}
	if err := r.SetApproved(); err != nil {
		t.Fatal(err)
	}
	if err := r.Announce("approved replacement", doc, s.Steps); err == nil {
		t.Fatal("approved replaced")
	}
	if err := r.AllowInitialPlanReplacement(); err == nil {
		t.Fatal("approved authorized")
	}
}

func TestPlanRuntimeInitialReplacementConcurrentOneShot(t *testing.T) {
	s := runtimeFixture()
	s.Document.Lifecycle = ""
	for i := range s.Steps {
		s.Steps[i].Status = "pending"
		s.Steps[i].Evidence = ""
	}
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{})
	if err := r.AllowInitialPlanReplacement(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- r.Announce("replacement", s.Document, s.Steps) }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d replacements", accepted)
	}
}

func TestPlanRuntimeInitialReplacementRejectsExecution(t *testing.T) {
	for _, mutate := range []func(*PlanSnapshot){
		func(s *PlanSnapshot) { s.Steps[0].Status = "running" },
		func(s *PlanSnapshot) { s.Steps[0].Evidence = "exit 0" },
	} {
		s := runtimeFixture()
		s.Document.Lifecycle = ""
		for i := range s.Steps {
			s.Steps[i].Status = "pending"
			s.Steps[i].Evidence = ""
		}
		mutate(&s)
		if err := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{}).AllowInitialPlanReplacement(); err == nil {
			t.Fatal("unsafe replacement authorized")
		}
	}
}

func TestPlanRuntimeDelegatePersistence(t *testing.T) {
	s := runtimeFixture()
	for i := range s.Steps {
		s.Steps[i].Status = "pending"
		s.Steps[i].Evidence = ""
	}
	fail := true
	saves := 0
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CommitSnapshot: func(PlanSnapshot) error {
		saves++
		if fail {
			return errors.New("disk")
		}
		return nil
	}})
	before := r.Snapshot()
	if _, err := r.RegisterDelegate("one", "worker"); PlanRuntimeErrorCode(err) != PlanRuntimePersistFailed {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) || len(r.plan.taskRegistry) != 0 {
		t.Fatal("failed delegate start published")
	}
	fail = false
	if name, err := r.RegisterDelegate("one", "worker"); err != nil || name != "one" {
		t.Fatalf("%q %v", name, err)
	}
	if name, err := r.RegisterDelegate("one", "second"); err != nil || name != "" {
		t.Fatalf("%q %v", name, err)
	}
	before = r.Snapshot()
	fail = true
	if _, err := r.FinishDelegate("one", "worker"); PlanRuntimeErrorCode(err) != PlanRuntimePersistFailed {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) || r.plan.completedTasks["one"]["worker"] {
		t.Fatal("failed finish published")
	}
	fail = false
	if name, err := r.FinishDelegate("one", "worker"); err != nil || name != "" {
		t.Fatalf("%q %v", name, err)
	}
	if name, err := r.FinishDelegate("one", "second"); err != nil || name != "one" {
		t.Fatalf("%q %v", name, err)
	}
	if r.Snapshot().Steps[0].Status != "running" {
		t.Fatal("delegate finish completed phase")
	}
	if saves != 6 {
		t.Fatalf("saves=%d", saves)
	}
}
