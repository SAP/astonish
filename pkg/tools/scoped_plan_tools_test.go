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
