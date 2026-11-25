package agent

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func delegateReleaseRuntime(options PlanRuntimeOptions) *PlanRuntime {
	s := runtimeFixture()
	for i := range s.Steps {
		s.Steps[i].Status = "pending"
		s.Steps[i].Evidence = ""
	}
	return NewPlanRuntimeFromSnapshot(s, options)
}

func delegateReleaseRevision(r *PlanRuntime) PlanRevisionRequest {
	req := revisionRequest(r.Snapshot())
	req.Steps[0].Details += " revised implementation"
	return req
}

func TestPlanRuntimeDelegateFinishAfterTerminalPhase(t *testing.T) {
	for _, terminal := range []string{"failed", "verify-failed", "complete"} {
		t.Run(terminal, func(t *testing.T) {
			verifyCalls := 0
			r := delegateReleaseRuntime(PlanRuntimeOptions{Verify: func(string) (int, string, error) {
				verifyCalls++
				if terminal == "verify-failed" {
					return 1, "failed verification", nil
				}
				return 0, "verified", nil
			}})
			for _, task := range []string{"returned", "active"} {
				if _, err := r.RegisterDelegate("one", task); err != nil {
					t.Fatal(err)
				}
			}
			status := terminal
			if status == "verify-failed" {
				status = "complete"
			}
			result := r.UpdateStep("one", status)
			if result.Code != PlanStepOK && result.Code != PlanStepVerifyFailed {
				t.Fatal(result)
			}
			before := r.Snapshot()
			callsBefore := verifyCalls
			if _, err := r.Revise(delegateReleaseRevision(r)); PlanRuntimeErrorCode(err) != PlanRuntimeBusy {
				t.Fatalf("active delegate must block revision: %v", err)
			}
			if name, err := r.FinishDelegate("ONE", "RETURNED"); err != nil || name != "" {
				t.Fatalf("terminal finish must not request verification: %q %v", name, err)
			}
			if !r.plan.completedTasks["one"]["returned"] {
				t.Fatal("returned delegate still registered as active after terminal phase")
			}
			if _, err := r.Revise(delegateReleaseRevision(r)); PlanRuntimeErrorCode(err) != PlanRuntimeBusy {
				t.Fatalf("remaining active delegate must block revision: %v", err)
			}
			if name, err := r.FinishDelegate("one", "active"); err != nil || name != "" {
				t.Fatalf("terminal finish must not request verification: %q %v", name, err)
			}
			after := r.Snapshot()
			if !reflect.DeepEqual(before.Steps, after.Steps) || before.Document.Results != after.Document.Results || verifyCalls != callsBefore {
				t.Fatal("delegate finish changed phase status/evidence/results or ran verification")
			}
			if after.Sequence != before.Sequence+2 {
				t.Fatal("delegate finishes were not persisted")
			}
			if name, err := r.FinishDelegate("one", "active"); err != nil || name != "" || !reflect.DeepEqual(after, r.Snapshot()) {
				t.Fatalf("duplicate callback was not a no-op: %q %v", name, err)
			}
			if result, err := r.Revise(delegateReleaseRevision(r)); err != nil || result.Kind != PlanRevisionResultApplied {
				t.Fatalf("revision blocked after all delegates returned: %+v %v", result, err)
			}
			if got := r.Snapshot().Steps[0]; got.Status != "pending" || got.Evidence != "" {
				t.Fatalf("revision promoted evidence: %+v", got)
			}
		})
	}
}

func TestPlanRuntimeDelegateTerminalFinishPersistenceFailure(t *testing.T) {
	fail := false
	var r *PlanRuntime
	r = delegateReleaseRuntime(PlanRuntimeOptions{CommitSnapshot: func(PlanSnapshot) error {
		if r != nil && r.plan.completedTasks["one"]["worker"] {
			t.Fatal("task completion published before persistence")
		}
		if fail {
			return errors.New("disk unavailable")
		}
		return nil
	}})
	if _, err := r.RegisterDelegate("one", "worker"); err != nil {
		t.Fatal(err)
	}
	if result := r.UpdateStep("one", "failed"); result.Code != PlanStepOK {
		t.Fatal(result)
	}
	before := r.Snapshot()
	fail = true
	if _, err := r.FinishDelegate("one", "worker"); PlanRuntimeErrorCode(err) != PlanRuntimePersistFailed {
		t.Fatalf("finish did not report failed persistence: %v", err)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) || r.plan.completedTasks["one"]["worker"] {
		t.Fatal("failed persistence released delegate")
	}
	if _, err := r.Revise(delegateReleaseRevision(r)); PlanRuntimeErrorCode(err) != PlanRuntimeBusy {
		t.Fatalf("failed finish persistence bypassed active protection: %v", err)
	}
	fail = false
	if name, err := r.FinishDelegate("one", "worker"); err != nil || name != "" {
		t.Fatalf("finish retry failed: %q %v", name, err)
	}
}

func TestPlanRuntimeDelegateTerminalFinishWrongTaskAndReplacement(t *testing.T) {
	r := delegateReleaseRuntime(PlanRuntimeOptions{})
	if _, err := r.RegisterDelegate("one", "old-worker"); err != nil {
		t.Fatal(err)
	}
	if result := r.UpdateStep("one", "failed"); result.Code != PlanStepOK {
		t.Fatal(result)
	}
	if err := r.AllowInitialPlanReplacement(); err != nil {
		t.Fatal(err)
	}
	if err := r.Announce("replacement", r.Snapshot().Document, r.Snapshot().Steps); err != nil {
		t.Fatal(err)
	}
	if err := r.SetApproved(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterDelegate("one", "current-worker"); err != nil {
		t.Fatal(err)
	}
	if result := r.UpdateStep("one", "failed"); result.Code != PlanStepOK {
		t.Fatal(result)
	}
	before := r.Snapshot()
	for _, callback := range [][2]string{{"one", "old-worker"}, {"one", "unknown"}, {"missing-phase", "current-worker"}} {
		if name, err := r.FinishDelegate(callback[0], callback[1]); err != nil || name != "" {
			t.Fatalf("late callback: %q %v", name, err)
		}
	}
	if !reflect.DeepEqual(before, r.Snapshot()) || r.plan.completedTasks["one"]["current-worker"] {
		t.Fatal("unregistered callback changed current plan")
	}
	if _, err := r.Revise(delegateReleaseRevision(r)); PlanRuntimeErrorCode(err) != PlanRuntimeBusy {
		t.Fatalf("late callback released current delegate: %v", err)
	}
}

func TestPlanRuntimeDelegateTerminalFinishConcurrentRevision(t *testing.T) {
	r := delegateReleaseRuntime(PlanRuntimeOptions{})
	for _, task := range []string{"returned", "active"} {
		if _, err := r.RegisterDelegate("one", task); err != nil {
			t.Fatal(err)
		}
	}
	if result := r.UpdateStep("one", "failed"); result.Code != PlanStepOK {
		t.Fatal(result)
	}
	req := delegateReleaseRevision(r)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if name, err := r.FinishDelegate("one", "returned"); err != nil || name != "" {
				t.Errorf("finish: %q %v", name, err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := r.Revise(req); PlanRuntimeErrorCode(err) != PlanRuntimeBusy {
				t.Errorf("active delegate protection: %v", err)
			}
		}()
	}
	wg.Wait()
	if _, err := r.FinishDelegate("one", "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Revise(req); err != nil {
		t.Fatal(err)
	}
}
