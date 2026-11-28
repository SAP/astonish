package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPlanPreparedTrustedSettlementReconcilesIntent(t *testing.T) {
	s := revisionFixture()
	s.Document.Lifecycle = PlanLifecycleApproved
	var intent, settled PlanPreparedOperation
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{
		Verify: func(string) (int, string, error) { return 0, "ok", nil },
		PrepareOperation: func(op PlanPreparedOperation) error {
			if op.Stage == PlanPreparedVerifying {
				intent = op
			}
			if op.Stage == PlanPreparedSettled {
				settled = op
			}
			return nil
		},
		CommitSnapshot: func(PlanSnapshot) error { return errors.New("denied") },
	})
	r.UpdateStep("two", "complete")
	restored := NewPlanRuntimeFromSnapshot(intent.Base, PlanRuntimeOptions{})
	if _, err := restored.ResumePreparedOperation(intent); PlanRuntimeErrorCode(err) != PlanRuntimeVerificationUncertain {
		t.Fatal(err)
	}
	if result, err := restored.ResumePreparedOperation(settled); err != nil || result.Code != PlanStepOK {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestPlanPreparedLegacyVerificationAssignsIdentity(t *testing.T) {
	s := revisionFixture()
	s.Document.Lifecycle = PlanLifecycleApproved
	s.PlanID = ""
	s.Revision = 0
	s.Sequence = 0
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{Verify: func(string) (int, string, error) { return 0, "ok", nil }})
	if got := r.UpdateStep("two", "complete"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	if got := r.Snapshot(); got.PlanID == "" || got.Revision != 1 || got.Sequence != 1 {
		t.Fatal(got)
	}
}

func TestPlanPreparedAnnouncementRestore(t *testing.T) {
	s := revisionFixture()
	r := NewPlanRuntime(nil, PlanRuntimeOptions{CommitSnapshot: func(PlanSnapshot) error { return errors.New("denied") }})
	if err := r.Announce(s.Goal, s.Document, s.Steps); err == nil {
		t.Fatal("expected denial")
	}
	op := roundTripPrepared(t, r.PreparedOperations()[0])
	restored := NewPlanRuntime(nil, PlanRuntimeOptions{})
	if _, err := restored.ResumePreparedOperation(op); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*op.Candidate, restored.Snapshot()) {
		t.Fatal("candidate changed")
	}
	if _, err := restored.ResumePreparedOperation(op); err != nil {
		t.Fatal(err)
	}
}

func roundTripPrepared(t *testing.T, op PlanPreparedOperation) PlanPreparedOperation {
	t.Helper()
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	var out PlanPreparedOperation
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPlanPreparedPublishRestartFailureEvidenceAndDuplicate(t *testing.T) {
	for _, exit := range []int{0, 7} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			s := revisionFixture()
			s.Document.Lifecycle = PlanLifecycleApproved
			calls := 0
			var saved PlanPreparedOperation
			r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{
				Verify: func(string) (int, string, error) {
					calls++
					if exit != 0 {
						return exit, "failure output", errors.New("failed verifier")
					}
					return 0, "success output", nil
				},
				PrepareOperation: func(op PlanPreparedOperation) error { saved = op; return nil },
				CommitSnapshot:   func(PlanSnapshot) error { return errors.New("denied mirror") },
			})
			base := r.Snapshot()
			if got := r.UpdateStep("two", "complete"); got.Code != PlanRuntimePersistFailed {
				t.Fatal(got)
			}
			if !reflect.DeepEqual(base, r.Snapshot()) {
				t.Fatal("denial changed accepted state")
			}
			saved = roundTripPrepared(t, saved)
			if saved.Stage != PlanPreparedSettled || saved.Verification.ExitCode != exit {
				t.Fatal(saved)
			}
			mirrors, hooks := 0, 0
			restored := NewPlanRuntimeFromSnapshot(base, PlanRuntimeOptions{
				Verify:           func(string) (int, string, error) { calls++; t.Error("replayed verifier"); return 0, "", nil },
				PrepareOperation: func(PlanPreparedOperation) error { hooks++; return nil },
				CommitSnapshot:   func(PlanSnapshot) error { mirrors++; return nil },
			})
			result, err := restored.ResumePreparedOperation(saved)
			if err != nil {
				t.Fatal(err)
			}
			want := PlanStepOK
			if exit != 0 {
				want = PlanStepVerifyFailed
			}
			if result.Code != want || calls != 1 {
				t.Fatalf("result %+v calls %d", result, calls)
			}
			if exit != 0 && !strings.Contains(restored.Snapshot().Steps[1].Evidence, "failed verifier") {
				t.Fatal("lost failure evidence")
			}
			if _, err = restored.ResumePreparedOperation(saved); err != nil {
				t.Fatal(err)
			}
			if mirrors != 1 || hooks != 1 {
				t.Fatalf("duplicate published: %d/%d", mirrors, hooks)
			}
		})
	}
}

func TestPlanPreparedStaleBase(t *testing.T) {
	s := revisionFixture()
	s.Document.Lifecycle = PlanLifecycleApproved
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{Verify: func(string) (int, string, error) { return 0, "ok", nil }, CommitSnapshot: func(PlanSnapshot) error { return errors.New("deny") }})
	r.UpdateStep("two", "complete")
	op := r.PreparedOperations()[0]
	base := r.Snapshot()
	base.Sequence++
	restored := NewPlanRuntimeFromSnapshot(base, PlanRuntimeOptions{})
	if _, err := restored.ResumePreparedOperation(op); PlanRuntimeErrorCode(err) != PlanRuntimeStale {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Snapshot(), NewPlanRuntimeFromSnapshot(base, PlanRuntimeOptions{}).Snapshot()) {
		t.Fatal("stale candidate published")
	}
}

func TestPlanPreparedSuspendBeforeVerifyAndUncertainRestart(t *testing.T) {
	s := revisionFixture()
	s.Document.Lifecycle = PlanLifecycleApproved
	denied, calls := true, 0
	var intent PlanPreparedOperation
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{
		Verify: func(string) (int, string, error) { calls++; return 0, "ok", nil },
		PrepareOperation: func(op PlanPreparedOperation) error {
			if op.Stage == PlanPreparedVerifying {
				intent = op
			}
			if denied {
				return errors.New("approval required")
			}
			return nil
		},
	})
	if got := r.UpdateStep("two", "complete"); got.Code != PlanRuntimePersistFailed {
		t.Fatal(got)
	}
	ready := r.PreparedOperations()[0]
	if ready.Stage != PlanPreparedReady || calls != 0 {
		t.Fatalf("dispatched before admission: %+v %d", ready, calls)
	}
	denied = false
	if _, err := r.ResumePreparedOperation(ready); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	restored := NewPlanRuntimeFromSnapshot(intent.Base, PlanRuntimeOptions{Verify: func(string) (int, string, error) { t.Fatal("uncertain replay"); return 0, "", nil }})
	if _, err := restored.ResumePreparedOperation(roundTripPrepared(t, intent)); PlanRuntimeErrorCode(err) != PlanRuntimeVerificationUncertain {
		t.Fatal(err)
	}
	if got := restored.UpdateStep("two", "complete"); got.Code != PlanRuntimeVerificationUncertain {
		t.Fatal(got)
	}
}

func TestPlanPreparedSettledSaveDenialRetainsLocalEvidence(t *testing.T) {
	s := revisionFixture()
	s.Document.Lifecycle = PlanLifecycleApproved
	calls, deny := 0, true
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{
		Verify: func(string) (int, string, error) { calls++; return 0, "ok", nil },
		PrepareOperation: func(op PlanPreparedOperation) error {
			if deny && op.Stage == PlanPreparedSettled {
				return errors.New("storage unavailable")
			}
			return nil
		},
	})
	if got := r.UpdateStep("two", "complete"); got.Code != PlanRuntimePersistFailed {
		t.Fatal(got)
	}
	if r.PreparedOperations()[0].Verification.Output != "ok" {
		t.Fatal("lost evidence")
	}
	deny = false
	if got := r.UpdateStep("two", "complete"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestPlanPreparedConcurrentWaves(t *testing.T) {
	for _, hooked := range []bool{false, true} {
		t.Run(fmt.Sprint(hooked), func(t *testing.T) {
			s := revisionFixture()
			s.Document.Lifecycle = PlanLifecycleApproved
			for i := range s.Steps {
				s.Steps[i].Status = "running"
				s.Steps[i].Evidence = ""
				s.Steps[i].ParallelGroup = "wave"
			}
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			options := PlanRuntimeOptions{Verify: func(string) (int, string, error) { started <- struct{}{}; <-release; return 0, "ok", nil }}
			if hooked {
				options.PrepareOperation = func(PlanPreparedOperation) error { return nil }
			}
			r := NewPlanRuntimeFromSnapshot(s, options)
			var wg sync.WaitGroup
			for _, step := range s.Steps {
				wg.Add(1)
				go func(name string) {
					defer wg.Done()
					if got := r.UpdateStep(name, "complete"); got.Code != PlanStepOK {
						t.Error(got)
					}
				}(step.Name)
			}
			<-started
			<-started
			close(release)
			wg.Wait()
			for _, step := range r.Snapshot().Steps {
				if step.Status != "complete" {
					t.Fatal(step)
				}
			}
			if len(r.PreparedOperations()) != 0 {
				t.Fatal("successful operations retained")
			}
		})
	}
}

func TestPlanPreparedCallbackIsolation(t *testing.T) {
	s := revisionFixture()
	r := NewPlanRuntime(nil, PlanRuntimeOptions{PrepareOperation: func(op PlanPreparedOperation) error {
		op.Candidate.Steps[0].Files[0].Path = "corrupt"
		return errors.New("denied")
	}})
	if err := r.Announce(s.Goal, s.Document, s.Steps); err == nil {
		t.Fatal("expected denial")
	}
	op := r.PreparedOperations()[0]
	if op.Candidate.Steps[0].Files[0].Path == "corrupt" {
		t.Fatal("callback aliased record")
	}
	op.Candidate.Steps[0].Files[0].Path = "also corrupt"
	if r.PreparedOperations()[0].Candidate.Steps[0].Files[0].Path == "also corrupt" {
		t.Fatal("getter aliased record")
	}
}

func TestPlanPreparedAnnouncementStableAfterCommitFailure(t *testing.T) {
	var candidates []PlanSnapshot
	r := NewPlanRuntime(nil, PlanRuntimeOptions{CommitSnapshot: func(s PlanSnapshot) error {
		candidates = append(candidates, s)
		if len(candidates) == 1 {
			return errors.New("approval required")
		}
		return nil
	}})
	s := revisionFixture()
	if err := r.Announce(s.Goal, s.Document, s.Steps); err == nil {
		t.Fatal("expected denial")
	}
	if err := r.Announce(s.Goal, s.Document, s.Steps); err != nil {
		t.Fatal(err)
	}
	if candidates[0].PlanID != candidates[1].PlanID || candidates[0].Sequence != candidates[1].Sequence {
		t.Fatal("retry changed exact candidate identity")
	}
}

func TestPlanPreparedVerificationOnceAfterCommitFailure(t *testing.T) {
	s := revisionFixture()
	s.Document.Lifecycle = PlanLifecycleApproved
	calls, commits := 0, 0
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{Verify: func(string) (int, string, error) { calls++; return 0, "settled output", nil }, CommitSnapshot: func(PlanSnapshot) error {
		commits++
		if commits == 1 {
			return errors.New("denied")
		}
		return nil
	}})
	if got := r.UpdateStep("two", "complete"); got.Code != PlanRuntimePersistFailed {
		t.Fatal(got)
	}
	if got := r.UpdateStep("two", "complete"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	if calls != 1 {
		t.Fatalf("verification executed %d times", calls)
	}
}
