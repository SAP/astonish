package tools

import (
	"context"
	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/adk/tool"
	"testing"
)

type scopedBasicContext struct {
	tool.Context
	base context.Context
}

func (c scopedBasicContext) Value(k any) any { return c.base.Value(k) }

func TestScopedPlanCallbacks(t *testing.T) {
	original := planStepUpdateCallback
	defer func() { planStepUpdateCallback = original }()
	planStepUpdateCallback = func(string, string) agent.PlanStepApplyResult {
		t.Fatal("global callback reached")
		return agent.PlanStepApplyResult{}
	}
	calls := 0
	ctx := scopedBasicContext{base: WithPlanCallbacks(context.Background(), PlanCallbacks{Update: func(s, status string) agent.PlanStepApplyResult {
		calls++
		return agent.PlanStepApplyResult{Name: s, Applied: status, Code: agent.PlanStepOK}
	}})}
	out, err := updatePlan(ctx, UpdatePlanArgs{Step: "owned", Status: "running"})
	if err != nil || out.Status != "ok" || calls != 1 {
		t.Fatalf("%+v %v calls=%d", out, err, calls)
	}
	empty := scopedBasicContext{base: WithPlanCallbacks(context.Background(), PlanCallbacks{})}
	out, err = updatePlan(empty, UpdatePlanArgs{Step: "owned", Status: "complete"})
	if err != nil || out.Status != "no_active_plan" {
		t.Fatalf("%+v %v", out, err)
	}
	completed, err := announceCompletion(empty, AnnounceCompletionArgs{OutcomeObserved: "must not complete"})
	if err != nil || completed.Status != "no_active_plan" {
		t.Fatalf("%+v %v", completed, err)
	}
}

func TestScopedPlanRuntimeAuthoritativeNil(t *testing.T) {
	base := WithPlanCallbacks(context.Background(), PlanCallbacks{
		Update: func(string, string) agent.PlanStepApplyResult {
			t.Fatal("callback fallback")
			return agent.PlanStepApplyResult{}
		},
		Revise: func(agent.PlanRevisionRequest) (agent.PlanRevisionResult, error) {
			t.Fatal("revision callback fallback")
			return agent.PlanRevisionResult{}, nil
		},
	})
	ctx := scopedBasicContext{base: agent.WithPlanRuntime(base, nil)}
	for _, args := range []UpdatePlanArgs{{Step: "phase", Status: "running"}, {Action: UpdatePlanRevise, Revision: &agent.PlanRevisionRequest{}}} {
		result, err := updatePlan(ctx, args)
		if err != nil || result.Status != "no_active_plan" {
			t.Fatalf("%+v %v", result, err)
		}
	}
	complete, err := announceCompletion(ctx, AnnounceCompletionArgs{OutcomeObserved: "no fallback"})
	if err != nil || complete.Status != "no_active_plan" {
		t.Fatalf("%+v %v", complete, err)
	}
}

func TestScopedPlanRuntimeOverridesCallbacks(t *testing.T) {
	runtime := agent.NewPlanRuntime(agent.NewPlanState("owned", agent.PlanDocumentInfo{PlanID: "owned", Revision: 1, Lifecycle: agent.PlanLifecycleApproved}, []agent.PlanStepInfo{{Name: "phase", Status: "pending"}}), agent.PlanRuntimeOptions{})
	base := WithPlanCallbacks(context.Background(), PlanCallbacks{Update: func(string, string) agent.PlanStepApplyResult {
		t.Fatal("callback fallback")
		return agent.PlanStepApplyResult{}
	}})
	result, err := updatePlan(scopedBasicContext{base: agent.WithPlanRuntime(base, runtime)}, UpdatePlanArgs{Step: "phase", Status: "running"})
	if err != nil || result.Status != "ok" || result.Snapshot == nil || result.Snapshot.Steps[0].Status != "running" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestScopedTaskResults(t *testing.T) {
	a, b := NewTaskResultStore(), NewTaskResultStore()
	id := a.Store("owned", "complete controlled output", "summary")
	global := GetTaskResultStore().Store("foreign", "global secret", "summary")
	ctx := scopedBasicContext{base: WithTaskResultStore(context.Background(), a)}
	out, err := readTaskResult(ctx, ReadTaskResultArgs{ResultID: id})
	if err != nil || out.Content != "complete controlled output" {
		t.Fatalf("%+v %v", out, err)
	}
	for _, tc := range []struct {
		ctx context.Context
		id  string
	}{{WithTaskResultStore(context.Background(), b), id}, {WithTaskResultStore(context.Background(), a), global}} {
		out, err = readTaskResult(scopedBasicContext{base: tc.ctx}, ReadTaskResultArgs{ResultID: tc.id})
		if err != nil || out.Error == "" || out.Content != "" {
			t.Fatalf("leaked: %+v %v", out, err)
		}
	}
}

func TestScopedPlanRevisionAppliesScopeImmediately(t *testing.T) {
	s := agent.PlanSnapshot{PlanID: "owned", Revision: 1, Sequence: 1, Goal: "Hello", Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleApproved, Context: "design", WhatNotToDo: "boundaries", Verification: "Hello"}, Steps: []agent.PlanStepInfo{{Name: "greeting", Description: "write", Details: "Hello", Summary: "greeting", Outcome: "Hello", Files: []agent.PlanFileChange{{Path: "/outside/hello.txt", Kind: "new"}}, Verify: "printf Hello", VerifyKind: "behavior", Status: "pending"}}}
	runtime := agent.NewPlanRuntimeFromSnapshot(s, agent.PlanRuntimeOptions{Verify: func(string) (int, string, error) { t.Fatal("revision invoked verification"); return 0, "", nil }})
	req := agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: 1, Reason: "new scope", RequiresApproval: true, Steps: s.Definition().Steps}
	req.Steps[0].Outcome = "HelloWorld"
	result, err := updatePlan(scopedBasicContext{base: agent.WithPlanRuntime(context.Background(), runtime)}, UpdatePlanArgs{Action: UpdatePlanRevise, Revision: &req})
	if err != nil || result.Status != "applied" || result.Snapshot == nil || result.Snapshot.Revision != 2 || result.Snapshot.PendingRevision != nil || result.Snapshot.Steps[0].Outcome != "HelloWorld" {
		t.Fatalf("%+v %v", result, err)
	}
}
