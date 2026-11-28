package launcher

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
)

func failureDelegateRuntime(t *testing.T, code bool) (*agent.ChatAgent, *agent.PlanRuntime, *int) {
	t.Helper()
	verifies := new(int)
	doc := agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleApproved, Context: "Delegate release regression", WhatNotToDo: "Preserve verification", Verification: "Run focused delegate tests"}
	if code {
		doc.CodeExecutionMode = agent.CodePlanRunToCompletion
	}
	r := agent.NewPlanRuntime(agent.NewPlanState("Delegates", doc, []agent.PlanStepInfo{{Name: "one", Description: "Release delegates", Details: "Track joined tasks", Summary: "Delegates release safely", Outcome: "Revision no longer busy", Files: []agent.PlanFileChange{{Path: "delegate.go", Kind: "modify"}}, Status: "running", Verify: "true", VerifyKind: "unit"}}), agent.PlanRuntimeOptions{CodeExecution: code, Verify: func(string) (int, string, error) { *verifies++; return 0, "verified", nil }})
	c := &agent.ChatAgent{}
	t.Cleanup(c.BindSessionPlanRuntime("session", r))
	return c, r, verifies
}

func delegateFailureRevise(r *agent.PlanRuntime) error {
	r.UpdateStep("one", "failed")
	s := r.Snapshot()
	steps := s.Definition().Steps
	steps[0].Details += " revised"
	_, err := r.Revise(agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "delegate returned", Steps: steps})
	return err
}

func delegateFailureEvent(kind, name, batch, task string) agent.SubTaskProgressEvent {
	return agent.SubTaskProgressEvent{Type: kind, SessionID: "session", PlanStep: "one", TaskName: name, BatchID: batch, TaskID: task}
}

func TestPlanDelegateFailureRelease(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "timeout"} {
		t.Run(status, func(t *testing.T) {
			c, r, v := failureDelegateRuntime(t, false)
			forwardPlanDelegateProgress(c, delegateFailureEvent("task_start", "worker", "", ""))
			e := delegateFailureEvent("task_failed", "worker", "", "")
			e.Status = status
			forwardPlanDelegateProgress(c, e)
			if err := delegateFailureRevise(r); agent.PlanRuntimeErrorCode(err) == agent.PlanRuntimeBusy {
				t.Fatal("terminal delegate still busy")
			}
			if *v != 0 {
				t.Fatal("failure ran verification")
			}
		})
	}
}

func TestPlanDelegateJoinedRetryAndSiblingProtection(t *testing.T) {
	c, r, v := failureDelegateRuntime(t, false)
	for _, name := range []string{"worker", "sibling"} {
		forwardPlanDelegateProgress(c, delegateFailureEvent("task_start", name, "batch", name))
	}
	failed := delegateFailureEvent("task_failed", "worker", "batch", "worker")
	failed.Attempt = 1
	forwardPlanDelegateProgress(c, failed)
	retry := delegateFailureEvent("task_start", "worker", "batch", "worker")
	retry.Attempt = 2
	forwardPlanDelegateProgress(c, retry)
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_complete", "sibling", "batch", "sibling"))
	if *v != 0 {
		t.Fatal("attempt completion verified before join")
	}
	if err := delegateFailureRevise(r); agent.PlanRuntimeErrorCode(err) != agent.PlanRuntimeBusy {
		t.Fatalf("retry/sibling not busy: %v", err)
	}
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_joined", "worker", "batch", "worker"))
	if err := delegateFailureRevise(r); agent.PlanRuntimeErrorCode(err) != agent.PlanRuntimeBusy {
		t.Fatalf("joined worker released active sibling: %v", err)
	}
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_joined", "sibling", "batch", "sibling"))
	if *v != 0 || r.Snapshot().Steps[0].Status != "failed" || r.Snapshot().Steps[0].Evidence != "" {
		t.Fatal("join falsely verified/completed phase")
	}
	if err := delegateFailureRevise(r); err != nil {
		t.Fatalf("joined batch still busy: %v", err)
	}
}

func TestPlanDelegateRetryNameReregisters(t *testing.T) {
	c, r, _ := failureDelegateRuntime(t, false)
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_start", "worker", "", ""))
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_failed", "worker", "", ""))
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_start", "worker", "", ""))
	if err := delegateFailureRevise(r); agent.PlanRuntimeErrorCode(err) != agent.PlanRuntimeBusy {
		t.Fatalf("fresh retry not busy: %v", err)
	}
}

func TestPlanDelegateCodeCallbacksIgnored(t *testing.T) {
	c, r, v := failureDelegateRuntime(t, true)
	before := r.Snapshot()
	for _, kind := range []string{"task_start", "task_failed", "task_complete", "task_joined"} {
		forwardPlanDelegateProgress(c, delegateFailureEvent(kind, "worker", "batch", "id"))
	}
	if r.Snapshot().Sequence != before.Sequence || *v != 0 {
		t.Fatal("ID-owned Code callback mutated plan")
	}
}

func TestPlanDelegateJoinedPersistenceFailure(t *testing.T) {
	fail := false
	verifies := 0
	plan := agent.NewPlanState("Delegate", agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleApproved}, []agent.PlanStepInfo{{Name: "one", Status: "running", Verify: "true", VerifyKind: "unit"}})
	r := agent.NewPlanRuntime(plan, agent.PlanRuntimeOptions{CommitSnapshot: func(agent.PlanSnapshot) error {
		if fail {
			return fmt.Errorf("storage unavailable")
		}
		return nil
	}, Verify: func(string) (int, string, error) { verifies++; return 0, "", nil }})
	c := &agent.ChatAgent{}
	t.Cleanup(c.BindSessionPlanRuntime("session", r))
	var got []agent.SubTaskProgressEvent
	c.SubTaskProgressCallback = func(e agent.SubTaskProgressEvent) { got = append(got, e) }
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_start", "worker", "batch", "id"))
	before := r.Snapshot().Sequence
	fail = true
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_joined", "worker", "batch", "id"))
	if r.Snapshot().Sequence != before || verifies != 0 {
		t.Fatal("failed persistence published release or verified")
	}
	if len(got) != 2 || got[1].Reason != agent.PlanRuntimePersistFailed {
		t.Fatalf("failure not surfaced: %+v", got)
	}
	fail = false
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_joined", "worker", "batch", "id"))
	if r.Snapshot().Sequence != before+1 {
		t.Fatal("join retry not durably released")
	}
}

func TestPlanDelegateSameNamedBatchesDoNotReleaseEachOther(t *testing.T) {
	c, r, _ := failureDelegateRuntime(t, false)
	for _, batch := range []string{"a", "b"} {
		forwardPlanDelegateProgress(c, delegateFailureEvent("task_start", "worker", batch, "id"))
	}
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_joined", "worker", "a", "id"))
	if err := delegateFailureRevise(r); agent.PlanRuntimeErrorCode(err) != agent.PlanRuntimeBusy {
		t.Fatalf("other batch released: %v", err)
	}
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_joined", "worker", "a", "id"))
	if err := delegateFailureRevise(r); agent.PlanRuntimeErrorCode(err) != agent.PlanRuntimeBusy {
		t.Fatalf("duplicate join released other batch: %v", err)
	}
	forwardPlanDelegateProgress(c, delegateFailureEvent("task_joined", "worker", "b", "id"))
	if err := delegateFailureRevise(r); err != nil {
		t.Fatalf("both joins still busy: %v", err)
	}
}

func TestPlanDelegateManagerJoinsFailures(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			c, r, v := failureDelegateRuntime(t, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if deadline {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer stop()
			} else {
				cancel()
			}
			mgr := agent.NewSubAgentManager(agent.SubAgentConfig{})
			var mu sync.Mutex
			joined := 0
			mgr.SubTaskProgress = func(e agent.SubTaskProgressEvent) {
				mu.Lock()
				defer mu.Unlock()
				if e.Type == "task_joined" {
					joined++
				}
				forwardPlanDelegateProgress(c, e)
			}
			tasks := []agent.SubAgentTask{{Name: "worker", PlanStep: "one", ParentID: "session", ParentDepth: 100, BatchID: "batch", TaskID: "id"}}
			forwardPlanDelegateProgress(c, delegateFailureEvent("task_start", "worker", "batch", "id"))
			mgr.RunTasks(ctx, tasks)
			if joined != 1 || *v != 0 {
				t.Fatalf("joined=%d verifies=%d", joined, *v)
			}
			if err := delegateFailureRevise(r); err != nil {
				t.Fatalf("manager terminal remains busy: %v", err)
			}
		})
	}
}
