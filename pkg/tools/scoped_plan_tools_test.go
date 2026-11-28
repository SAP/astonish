package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
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

type scopedPlanRunContext struct{ tool.Context }

func (scopedPlanRunContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

// runScopedPlanJSON exercises the provider-facing tool decoder, not updatePlan directly.
func runScopedPlanJSON(t *testing.T, ctx tool.Context, input map[string]any) UpdatePlanResult {
	t.Helper()
	got, err := NewUpdatePlanTool()
	if err != nil {
		t.Fatal(err)
	}
	runner, ok := got.(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("update_plan %T is not runnable", got)
	}
	out, err := runner.Run(scopedPlanRunContext{Context: ctx}, input)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var result UpdatePlanResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func scopedStepPatchFixture() agent.PlanSnapshot {
	return agent.PlanSnapshot{
		PlanID: "owned", Revision: 1, Sequence: 1, Goal: "Scoped patch",
		Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleApproved, Context: "design", WhatNotToDo: "boundaries", Verification: "required outcome"},
		Steps: []agent.PlanStepInfo{
			{Name: "first", Description: "first phase", Details: "implement first", Summary: "first outcome", Outcome: "first works", Files: []agent.PlanFileChange{{Path: "pkg/widget/first.go", Kind: "modify"}}, Verify: "printf first", VerifyKind: agent.VerifyKindUnit, Status: "pending", ParallelGroup: "wave-1"},
			{Name: "final", Description: "final phase", Details: "implement final", Summary: "final outcome", Outcome: "final works", Files: []agent.PlanFileChange{{Path: "pkg/widget/final.go", Kind: "modify"}}, Verify: "printf final", VerifyKind: agent.VerifyKindUnit, Status: "pending", ParallelGroup: "wave-2"},
		},
	}
}

func TestScopedPlanStepPatchPreservesEvidence(t *testing.T) {
	verifies, commits := 0, 0
	var durable agent.PlanSnapshot
	runtime := agent.NewPlanRuntimeFromSnapshot(scopedStepPatchFixture(), agent.PlanRuntimeOptions{
		Verify:         func(cmd string) (int, string, error) { verifies++; return 0, cmd, nil },
		CommitSnapshot: func(s agent.PlanSnapshot) error { commits++; durable = s.Clone(); return nil },
	})
	base := WithPlanCallbacks(context.Background(), PlanCallbacks{Revise: func(agent.PlanRevisionRequest) (agent.PlanRevisionResult, error) {
		t.Fatal("bound runtime fell back to callback")
		return agent.PlanRevisionResult{}, nil
	}})
	ctx := scopedBasicContext{base: agent.WithPlanRuntime(base, runtime)}
	completed := runScopedPlanJSON(t, ctx, map[string]any{"step": "first", "status": "complete"})
	if completed.Status != "ok" || completed.Snapshot == nil || completed.Snapshot.Steps[0].Evidence == "" || verifies != 1 {
		t.Fatalf("completion: %+v verifies=%d", completed, verifies)
	}
	before := runtime.Snapshot()
	result := runScopedPlanJSON(t, ctx, map[string]any{"action": "revise", "revision": map[string]any{
		"plan_id": before.PlanID, "base_revision": before.Revision, "reason": "focused final command",
		"step_patches": []any{map[string]any{"name": "final", "verify": "printf revised"}},
	}})
	if result.Status != "applied" || result.Snapshot == nil {
		t.Fatalf("revision: %+v", result)
	}
	after := *result.Snapshot
	if after.Revision != before.Revision+1 || after.Document.Lifecycle != agent.PlanLifecycleApproved || verifies != 1 {
		t.Fatalf("unexpected revision/approval/verification: %+v verifies=%d", after, verifies)
	}
	if !reflect.DeepEqual(after.Steps[0], before.Steps[0]) {
		t.Fatalf("earlier evidence or definition changed: %+v", after.Steps[0])
	}
	wantFinal := before.Steps[1]
	wantFinal.Verify = "printf revised"
	if !reflect.DeepEqual(after.Steps[1], wantFinal) || !reflect.DeepEqual(after.Definition().Document, before.Definition().Document) || after.Goal != before.Goal {
		t.Fatalf("omitted data changed: %+v", after)
	}
	if !reflect.DeepEqual(durable, after) {
		t.Fatal("returned snapshot differs from committed snapshot")
	}
	// The public decoder rejects runtime fields before dispatch.
	got, err := NewUpdatePlanTool()
	if err != nil {
		t.Fatal(err)
	}
	runner := got.(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	for _, field := range []string{"status", "evidence"} {
		_, err := runner.Run(scopedPlanRunContext{Context: ctx}, map[string]any{"action": "revise", "revision": map[string]any{
			"plan_id": after.PlanID, "base_revision": after.Revision, "reason": "forged runtime field",
			"step_patches": []any{map[string]any{"name": "final", "verify": "printf forged", field: "forged"}},
		}})
		if err == nil || !reflect.DeepEqual(runtime.Snapshot(), after) || !reflect.DeepEqual(durable, after) {
			t.Fatalf("runtime field %s admitted: %v", field, err)
		}
	}
	// An invalid full replacement + patch request must not commit or execute.
	committed := commits
	result = runScopedPlanJSON(t, ctx, map[string]any{"action": "revise", "revision": map[string]any{
		"plan_id": after.PlanID, "base_revision": after.Revision, "reason": "invalid mixed forms",
		"steps": after.Definition().Steps, "step_patches": []any{map[string]any{"name": "final", "verify": "printf invalid"}},
	}})
	if result.Status == "applied" || result.Status == "no_op" || result.Message == "" || result.Snapshot == nil {
		t.Fatalf("mixed forms accepted: %+v", result)
	}
	if commits != committed || verifies != 1 || !reflect.DeepEqual(runtime.Snapshot(), after) || !reflect.DeepEqual(durable, after) || !reflect.DeepEqual(*result.Snapshot, after) {
		t.Fatal("invalid mixed revision mutated state")
	}
}

func TestScopedPlanStepPatchModeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		po       *agent.PromptOverrides
		want     string
		planning bool
	}{
		{"normal", &agent.PromptOverrides{}, "applied", false},
		{"plan", &agent.PromptOverrides{PlanMode: true}, "applied", true},
		{"graph", &agent.PromptOverrides{GraphPlanMode: true}, "applied", true},
		{"ask", &agent.PromptOverrides{AskMode: true}, "blocked_revision_mode", false},
		{"bot", &agent.PromptOverrides{Bot: &agent.BotPromptContext{}}, "unsupported_bot_revision", false},
		{"nil", &agent.PromptOverrides{}, "no_active_plan", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commits, verifies := 0, 0
			runtime := agent.NewPlanRuntimeFromSnapshot(scopedStepPatchFixture(), agent.PlanRuntimeOptions{
				Verify:         func(string) (int, string, error) { verifies++; return 0, "passed", nil },
				CommitSnapshot: func(agent.PlanSnapshot) error { commits++; return nil },
			})
			before := runtime.Snapshot()
			bound := runtime
			if tc.name == "nil" {
				bound = nil
			}
			base := WithPlanCallbacks(context.Background(), PlanCallbacks{Revise: func(agent.PlanRevisionRequest) (agent.PlanRevisionResult, error) {
				t.Fatal("callback fallback")
				return agent.PlanRevisionResult{}, nil
			}})
			ctx := scopedBasicContext{base: agent.WithPromptOverrides(agent.WithPlanRuntime(base, bound), tc.po)}
			result := runScopedPlanJSON(t, ctx, map[string]any{"action": "revise", "revision": map[string]any{"plan_id": before.PlanID, "base_revision": before.Revision, "reason": "focused command", "step_patches": []any{map[string]any{"name": "final", "verify": "printf revised"}}}})
			if result.Status != tc.want || verifies != 0 {
				t.Fatalf("%+v verifies=%d", result, verifies)
			}
			if tc.want != "applied" {
				if commits != 0 || !reflect.DeepEqual(runtime.Snapshot(), before) {
					t.Fatal("blocked patch mutated state")
				}
				if tc.name == "nil" && result.Snapshot != nil {
					t.Fatal("nil binding leaked a snapshot")
				}
				return
			}
			if commits != 1 || result.Snapshot == nil || result.Snapshot.Revision != 2 {
				t.Fatalf("revision not committed once: %+v commits=%d", result, commits)
			}
			if !tc.planning {
				if result.Snapshot.Document.Lifecycle != agent.PlanLifecycleApproved {
					t.Fatal("normal revision withdrew approval")
				}
				return
			}
			if result.Snapshot.Document.Lifecycle == agent.PlanLifecycleApproved {
				t.Fatal("planning revision retained execution approval")
			}
			blocked := runScopedPlanJSON(t, ctx, map[string]any{"step": "first", "status": "complete"})
			if blocked.Status != "blocked_plan_execution" || commits != 1 || verifies != 0 {
				t.Fatalf("planning executed: %+v", blocked)
			}
			// Switching to Normal alone does not authorize execution: ordinary approval is still required.
			normal := scopedBasicContext{base: agent.WithPlanRuntime(context.Background(), runtime)}
			blocked = runScopedPlanJSON(t, normal, map[string]any{"step": "first", "status": "complete"})
			if blocked.Status != agent.PlanRuntimeUnapproved || verifies != 0 {
				t.Fatalf("approval bypassed: %+v", blocked)
			}
			if err := runtime.SetApproved(); err != nil {
				t.Fatal(err)
			}
			approved := runScopedPlanJSON(t, normal, map[string]any{"step": "first", "status": "complete"})
			if approved.Status != "ok" || verifies != 1 {
				t.Fatalf("approved execution: %+v verifies=%d", approved, verifies)
			}
		})
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
