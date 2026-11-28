package agent

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func reviewSerialFixture() PlanSnapshot {
	s := codeExecutionFixture(CodePlanReviewEachPhase)
	for i := range s.Steps {
		s.Steps[i].ParallelGroup = "same-wave"
	}
	return s
}

func TestCodePlanReviewSerialBatch(t *testing.T) {
	r := NewPlanRuntimeFromSnapshot(reviewSerialFixture(), PlanRuntimeOptions{CodeExecution: true})
	before := r.Snapshot()
	err := r.AdmitCodeDelegates([]SubAgentTask{{TaskID: "a", PlanStep: "one"}, {TaskID: "b", PlanStep: "two"}})
	if PlanRuntimeErrorCode(err) != CodePlanReviewRequired {
		t.Fatalf("same-wave batch: %v", err)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) || len(r.plan.taskRegistry) != 0 {
		t.Fatal("rejected batch mutated runtime")
	}
	if err := r.AdmitCodeDelegates([]SubAgentTask{{TaskID: "a", PlanStep: "one"}, {TaskID: "b", PlanStep: "one"}}); err != nil {
		t.Fatal(err)
	}
}

func TestCodePlanReviewSerialVerify(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	r := NewPlanRuntimeFromSnapshot(reviewSerialFixture(), PlanRuntimeOptions{CodeExecution: true, Verify: func(string) (int, string, error) {
		entered <- struct{}{}
		<-release
		return 0, "verified", nil
	}})
	if got := r.UpdateStep("one", "running"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	assertBlocked := func(status string) {
		t.Helper()
		result := make(chan PlanStepApplyResult, 1)
		go func() { result <- r.UpdateStep("two", status) }()
		select {
		case got := <-result:
			if got.Code != CodePlanReviewRequired || !strings.Contains(got.Message, "one") {
				t.Fatal(got)
			}
		case <-entered:
			t.Fatal("second phase verified before first phase review")
		case <-time.After(5 * time.Second):
			t.Fatal("second phase was not rejected")
		}
	}
	assertBlocked("running")
	assertBlocked("complete")
	done := make(chan PlanStepApplyResult, 1)
	go func() { done <- r.UpdateStep("one", "complete") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first verify did not start")
	}
	assertBlocked("running")
	assertBlocked("complete")
	if err := r.AdmitCodeDelegates([]SubAgentTask{{TaskID: "during-verify", PlanStep: "two"}}); PlanRuntimeErrorCode(err) != CodePlanReviewRequired {
		t.Fatal(err)
	}
	unblock()
	select {
	case got := <-done:
		if got.Code != PlanStepOK || !r.CodeReviewPending() {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first verify did not finish")
	}
}

func TestCodePlanReviewSerialCorrections(t *testing.T) {
	calls := 0
	r := NewPlanRuntimeFromSnapshot(reviewSerialFixture(), PlanRuntimeOptions{CodeExecution: true, Verify: func(string) (int, string, error) { calls++; return 0, "corrected evidence", nil }})
	if got := r.UpdateStep("one", "complete"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	if err := r.AdmitCodeDelegates([]SubAgentTask{{TaskID: "premature", PlanStep: "one"}}); PlanRuntimeErrorCode(err) != PlanRuntimeCompleted {
		t.Fatalf("completed phase needs reset: %v", err)
	}
	if got := r.UpdateStep("one", "running"); got.Code != PlanStepOK {
		t.Fatalf("correction blocked: %+v", got)
	}
	if !r.CodeReviewPending() || r.Snapshot().Steps[0].Evidence != "" {
		t.Fatal("correction lost checkpoint or retained evidence")
	}
	tasks := []SubAgentTask{{TaskID: "fix", PlanStep: "one"}}
	if err := r.AdmitCodeDelegates(tasks); err != nil {
		t.Fatal(err)
	}
	if err := r.AdmitCodeDelegates([]SubAgentTask{{TaskID: "later", PlanStep: "two"}}); PlanRuntimeErrorCode(err) != CodePlanReviewRequired {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "complete", "failed"} {
		if got := r.UpdateStep("two", status); got.Code != CodePlanReviewRequired {
			t.Fatal(got)
		}
	}
	if got := r.UpdateStep("one", "complete"); got.Code != PlanRuntimeBusy {
		t.Fatal(got)
	}
	r.FinishCodeDelegates(tasks)
	if got := r.UpdateStep("one", "complete"); got.Code != PlanStepOK || calls != 2 {
		t.Fatal(got, calls)
	}
	if !r.CodeReviewPending() || r.Snapshot().Document.CodeReviewPending != "one" {
		t.Fatal("reverify lost review checkpoint")
	}
	if got := r.UpdateStep("two", "complete"); got.Code != CodePlanReviewRequired {
		t.Fatal(got)
	}
	if err := r.ContinueCodeReview(); err != nil {
		t.Fatal(err)
	}
	if got := r.UpdateStep("two", "complete"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	if got := r.Complete("verified", ""); got.Code != PlanCompletionOK {
		t.Fatal(got)
	}
}

func TestCodePlanDelegateRetryClearsEvidence(t *testing.T) {
	r := NewPlanRuntimeFromSnapshot(codeExecutionFixture(CodePlanRunToCompletion), PlanRuntimeOptions{CodeExecution: true, Verify: func(string) (int, string, error) { return 1, "broken", nil }})
	if got := r.UpdateStep("one", "complete"); got.Code != PlanStepVerifyFailed {
		t.Fatal(got)
	}
	if err := r.AdmitCodeDelegates([]SubAgentTask{{TaskID: "retry", PlanStep: "one"}}); err != nil {
		t.Fatal(err)
	}
	if s := r.Snapshot().Steps[0]; s.Status != "running" || s.Evidence != "" {
		t.Fatalf("stale failed evidence: %+v", s)
	}
}

func TestCodePlanReviewSerialDoesNotAffectOtherModes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  CodePlanExecutionMode
		local bool
	}{
		{"run-to-completion", CodePlanRunToCompletion, true},
		{"remote", CodePlanReviewEachPhase, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := reviewSerialFixture()
			s.Document.CodeExecutionMode = tc.mode
			r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CodeExecution: tc.local, Verify: func(string) (int, string, error) { return 0, "verified", nil }})
			if got := r.UpdateStep("two", "complete"); got.Code != PlanStepOK {
				t.Fatal(got)
			}
			r = NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CodeExecution: tc.local})
			if err := r.AdmitCodeDelegates([]SubAgentTask{{TaskID: "a", PlanStep: "one"}, {TaskID: "b", PlanStep: "two"}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCodePlanReviewSerialCompleteRequiresContinuation(t *testing.T) {
	s := reviewSerialFixture()
	for i := range s.Steps {
		s.Steps[i].Status = "complete"
		s.Steps[i].Evidence = "exit 0"
	}
	s.Document.CodeReviewPending = "one"
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CodeExecution: true})
	if got := r.Complete("verified", ""); got.Code != CodePlanReviewRequired {
		t.Fatal(got)
	}
	if r.Snapshot().Document.Results != "" {
		t.Fatal("pending review published Results")
	}
}
