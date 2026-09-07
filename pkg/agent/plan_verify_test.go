package agent

import (
	"strings"
	"testing"
)

func TestApplyPlanStepUpdate_RunsVerify(t *testing.T) {
	c := &ChatAgent{
		PlanVerify: func(command string) (int, string, error) {
			if command != "go test ./pkg/agent/ -run TestApply" {
				t.Errorf("verify command = %q", command)
			}
			return 0, "PASS", nil
		},
	}
	plan := NewPlanState("goal", PlanDocumentInfo{}, []PlanStepInfo{
		{
			Name:       "types",
			Verify:     "go test ./pkg/agent/ -run TestApply",
			VerifyKind: VerifyKindUnit,
			Files:      []PlanFileChange{{Path: "pkg/agent/plan_verify.go", Kind: "modify"}},
		},
	})
	c.SetActivePlan(plan)
	c.MarkActivePlanApproved()

	res := c.ApplyPlanStepUpdate("types", "complete")
	if res.Code != PlanStepOK || res.Applied != "complete" {
		t.Fatalf("result = %+v, want ok/complete", res)
	}
	info, ok := plan.StepLookup("types")
	if !ok || info.Status != "complete" {
		t.Fatalf("step status = %+v", info)
	}
	if !strings.Contains(info.Evidence, "exit 0") {
		t.Errorf("evidence = %q, want exit 0", info.Evidence)
	}
}

func TestApplyPlanStepUpdate_VerifyFailed(t *testing.T) {
	c := &ChatAgent{
		PlanVerify: func(string) (int, string, error) {
			return 1, "FAIL: TestFoo", nil
		},
	}
	plan := NewPlanState("goal", PlanDocumentInfo{}, []PlanStepInfo{
		{Name: "types", Verify: "go test ./pkg/foo", VerifyKind: VerifyKindUnit, Files: []PlanFileChange{{Path: "pkg/agent/x.go"}}},
	})
	c.SetActivePlan(plan)
	c.MarkActivePlanApproved()

	res := c.ApplyPlanStepUpdate("types", "complete")
	if res.Code != PlanStepVerifyFailed || res.Applied != "failed" {
		t.Fatalf("result = %+v, want verify_failed/failed", res)
	}
	info, _ := plan.StepLookup("types")
	if info.Status != "failed" {
		t.Fatalf("status = %q, want failed", info.Status)
	}
	if !c.PlanVerifyFailed() {
		t.Fatal("PlanVerifyFailed should be true")
	}
	if !strings.Contains(info.Evidence, "exit 1") {
		t.Errorf("evidence = %q", info.Evidence)
	}
}

func TestApplyPlanStepUpdate_NoVerify(t *testing.T) {
	c := &ChatAgent{}
	plan := NewPlanState("goal", PlanDocumentInfo{}, []PlanStepInfo{
		{Name: "legacy", Files: []PlanFileChange{{Path: "pkg/agent/x.go"}}},
	})
	c.SetActivePlan(plan)
	c.MarkActivePlanApproved()

	res := c.ApplyPlanStepUpdate("legacy", "complete")
	if res.Code != PlanStepNoVerify {
		t.Fatalf("code = %q, want no_verify", res.Code)
	}
	info, _ := plan.StepLookup("legacy")
	if info.Status == "complete" {
		t.Fatal("legacy phase with empty verify must not complete")
	}
}

func TestApplyPlanStepUpdate_BlockedUntilApproved(t *testing.T) {
	c := &ChatAgent{
		PlanVerify: func(string) (int, string, error) { return 0, "", nil },
	}
	plan := NewPlanState("goal", PlanDocumentInfo{}, []PlanStepInfo{
		{Name: "types", Verify: "true", VerifyKind: VerifyKindUnit, Files: []PlanFileChange{{Path: "pkg/agent/x.go"}}},
	})
	c.SetActivePlan(plan)

	res := c.ApplyPlanStepUpdate("types", "complete")
	if res.Code != PlanStepBlockedPlan {
		t.Fatalf("code = %q, want blocked_plan_mode", res.Code)
	}
}

func TestApplyPlanStepUpdate_SerialBlocked(t *testing.T) {
	c := &ChatAgent{}
	plan := NewPlanState("goal", PlanDocumentInfo{}, []PlanStepInfo{
		{Name: "one", Verify: "true", VerifyKind: VerifyKindUnit, Files: []PlanFileChange{{Path: "pkg/agent/a.go"}}},
		{Name: "two", Verify: "true", VerifyKind: VerifyKindUnit, Files: []PlanFileChange{{Path: "pkg/agent/b.go"}}},
	})
	c.SetActivePlan(plan)
	c.MarkActivePlanApproved()

	res := c.ApplyPlanStepUpdate("two", "running")
	if res.Code != PlanStepSerialBlocked {
		t.Fatalf("code = %q, want serial_blocked (%+v)", res.Code, res)
	}
}

func TestApplyPlanStepUpdate_DeleteBlocked(t *testing.T) {
	c := &ChatAgent{}
	plan := NewPlanState("goal", PlanDocumentInfo{}, []PlanStepInfo{
		{
			Name:       "delete-incus",
			Verify:     "test ! -e pkg/sandbox/incus",
			VerifyKind: VerifyKindBehavior,
			Files:      []PlanFileChange{{Path: "pkg/sandbox/incus_backend.go", Kind: "delete"}},
		},
	})
	c.SetActivePlan(plan)
	c.MarkActivePlanApproved()

	res := c.ApplyPlanStepUpdate("delete-incus", "running")
	if res.Code != PlanStepDeleteBlocked {
		t.Fatalf("code = %q, want delete_blocked (%+v)", res.Code, res)
	}
}

func TestPlanState_CompleteTaskDoesNotCompleteStep(t *testing.T) {
	ps := NewPlanState("test", PlanDocumentInfo{}, []PlanStepInfo{
		{Name: "research-news", Description: "Search news"},
	})
	ps.StartStep("research-news", "apple-search")
	if got := ps.CompleteTask("research-news", "apple-search"); got != "research-news" {
		t.Fatalf("ready = %q", got)
	}
	_, steps := ps.Snapshot()
	if steps[0].status != "running" {
		t.Fatalf("status = %q, want running", steps[0].status)
	}
}

func TestDefaultPlanVerify_True(t *testing.T) {
	exit, _, err := DefaultPlanVerify(t.TempDir(), "true")
	if err != nil {
		t.Fatal(err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
}

func TestAnnounceCompletion_RequiresAllComplete(t *testing.T) {
	c := &ChatAgent{
		PlanVerify: func(string) (int, string, error) { return 0, "ok", nil },
	}
	plan := NewPlanState("goal", PlanDocumentInfo{Verification: "true"}, []PlanStepInfo{
		{Name: "one", Verify: "true", Status: "running"},
	})
	c.SetActivePlan(plan)
	c.MarkActivePlanApproved()

	res := c.AnnounceCompletion("it works", "")
	if res.Code != PlanCompletionIncomplete {
		t.Fatalf("code = %q, want incomplete", res.Code)
	}
}

func TestAnnounceCompletion_WritesResults(t *testing.T) {
	c := &ChatAgent{
		PlanVerify: func(command string) (int, string, error) {
			if command != "true" {
				t.Errorf("e2e command = %q", command)
			}
			return 0, "ok", nil
		},
	}
	plan := NewPlanState("goal", PlanDocumentInfo{Verification: "true"}, []PlanStepInfo{
		{Name: "one", Verify: "true", Status: "complete"},
	})
	c.SetActivePlan(plan)
	c.MarkActivePlanApproved()

	if plan.IsFullyAccepted() {
		t.Fatal("checkboxes without Results must not be fully accepted")
	}
	res := c.AnnounceCompletion("the helper no longer panics", "")
	if res.Code != PlanCompletionOK {
		t.Fatalf("code = %q message=%q", res.Code, res.Message)
	}
	if !plan.IsFullyAccepted() {
		t.Fatal("expected fully accepted after Results")
	}
	if !strings.Contains(plan.SnapshotDoc().Results, "the helper no longer panics") {
		t.Fatalf("results = %q", plan.SnapshotDoc().Results)
	}
}

func TestAnnounceCompletion_E2EFailed(t *testing.T) {
	c := &ChatAgent{
		PlanVerify: func(string) (int, string, error) { return 2, "nope", nil },
	}
	plan := NewPlanState("goal", PlanDocumentInfo{Verification: "false"}, []PlanStepInfo{
		{Name: "one", Verify: "true", Status: "complete"},
	})
	c.SetActivePlan(plan)
	c.MarkActivePlanApproved()

	res := c.AnnounceCompletion("claimed done", "")
	if res.Code != PlanCompletionVerifyFailed {
		t.Fatalf("code = %q, want verify_failed", res.Code)
	}
	if plan.IsFullyAccepted() {
		t.Fatal("failed e2e must not accept the plan")
	}
}

func TestDefaultPlanVerify_False(t *testing.T) {
	exit, _, err := DefaultPlanVerify(t.TempDir(), "false")
	if err != nil {
		t.Fatal(err)
	}
	if exit == 0 {
		t.Fatal("false should be non-zero")
	}
}
