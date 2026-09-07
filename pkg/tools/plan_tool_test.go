package tools

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
)

func TestUpdatePlanTool_DrivesCallbackAndEmitsEvent(t *testing.T) {
	// Save and restore package-level callbacks.
	origUpdate := planStepUpdateCallback
	origProgress := planProgressCallback
	defer func() {
		planStepUpdateCallback = origUpdate
		planProgressCallback = origProgress
	}()

	var gotStep, gotStatus string
	SetPlanStepUpdateCallback(func(step, status string) agent.PlanStepApplyResult {
		gotStep, gotStatus = step, status
		return agent.PlanStepApplyResult{Name: step, Applied: "complete", Code: agent.PlanStepOK}
	})
	var emitted agent.SubTaskProgressEvent
	SetPlanProgressCallback(func(e agent.SubTaskProgressEvent) { emitted = e })

	res, err := updatePlan(nil, UpdatePlanArgs{Step: "explore", Status: "done"})
	if err != nil {
		t.Fatalf("updatePlan error: %v", err)
	}
	if res.Status != "ok" || res.Step != "explore" || res.Applied != "complete" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if gotStep != "explore" || gotStatus != "done" {
		t.Errorf("callback got (%q,%q), want (explore,done)", gotStep, gotStatus)
	}
	if emitted.Type != "plan_step_update" || emitted.StepName != "explore" || emitted.StepStatus != "complete" {
		t.Errorf("unexpected SSE event: %+v", emitted)
	}
}

func TestUpdatePlanTool_StepNotFound(t *testing.T) {
	orig := planStepUpdateCallback
	origNames := planKnownStepsCallback
	defer func() {
		planStepUpdateCallback = orig
		planKnownStepsCallback = origNames
	}()
	SetPlanStepUpdateCallback(func(step, status string) agent.PlanStepApplyResult { return agent.PlanStepApplyResult{} })
	SetPlanKnownStepsCallback(func() []string { return []string{"explore-repos", "write-report"} })

	res, err := updatePlan(nil, UpdatePlanArgs{Step: "missing", Status: "running"})
	if err != nil {
		t.Fatalf("updatePlan error: %v", err)
	}
	if res.Status != "step_not_found" {
		t.Errorf("status = %q, want step_not_found", res.Status)
	}
	if !strings.Contains(res.Message, "explore-repos") || !strings.Contains(res.Message, "write-report") {
		t.Errorf("message = %q, want valid step names", res.Message)
	}
	if len(res.Steps) != 2 || res.Steps[0] != "explore-repos" {
		t.Errorf("Steps = %v, want [explore-repos write-report]", res.Steps)
	}
}

func TestUpdatePlanTool_NoActivePlan(t *testing.T) {
	orig := planStepUpdateCallback
	defer func() { planStepUpdateCallback = orig }()
	planStepUpdateCallback = nil

	res, err := updatePlan(nil, UpdatePlanArgs{Step: "x", Status: "running"})
	if err != nil {
		t.Fatalf("updatePlan error: %v", err)
	}
	if res.Status != "no_active_plan" {
		t.Errorf("status = %q, want no_active_plan", res.Status)
	}
}

func TestAnnouncePlanTool_RejectedPlanDoesNotEmit(t *testing.T) {
	origState := planStateCallback
	origProgress := planProgressCallback
	defer func() {
		planStateCallback = origState
		planProgressCallback = origProgress
	}()

	emitted := false
	SetPlanStateCallback(func(string, agent.PlanDocumentInfo, []agent.PlanStepInfo) bool {
		return false
	})
	SetPlanProgressCallback(func(agent.SubTaskProgressEvent) { emitted = true })

	res, err := announcePlan(nil, AnnouncePlanArgs{
		Goal:         "replacement",
		Context:      "Test context for validation of the replacement path.",
		WhatNotToDo:  "Do not change update_plan.",
		Verification: "go test ./pkg/agent/ -run TestChatAgent_RestoreApprovedPlanPreservesStatus",
		Steps: []PlanStepInput{{
			Name:        "replace",
			Description: "replace approved plan",
			Details:     "Rewrite pkg/agent/plan_state.go SetActivePlan to reject replacements.",
			Summary:     "Test summary",
			Outcome:     "A second announce_plan cannot overwrite an approved plan",
			Files:       []PlanFileChangeInput{{Path: "pkg/agent/plan_state.go", Kind: "modify"}},
			Verify:      "go test ./pkg/agent/ -run TestChatAgent_TrySetActivePlan",
			VerifyKind:  agent.VerifyKindUnit,
		}},
	})
	if err != nil {
		t.Fatalf("announcePlan error: %v", err)
	}
	if res.Status != "blocked_active_approved_plan" {
		t.Fatalf("status = %q, want blocked_active_approved_plan", res.Status)
	}
	if emitted {
		t.Fatal("rejected announcement must not emit plan_announced")
	}
}

func TestAnnouncePlanTool_PassesDetailsThrough(t *testing.T) {
	orig := planStateCallback
	origProgress := planProgressCallback
	defer func() {
		planStateCallback = orig
		planProgressCallback = origProgress
	}()

	var gotSteps []agent.PlanStepInfo
	var capturedEvent agent.SubTaskProgressEvent
	SetPlanStateCallback(func(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo) bool {
		gotSteps = steps
		return true
	})
	SetPlanProgressCallback(func(e agent.SubTaskProgressEvent) { capturedEvent = e })

	_, err := announcePlan(nil, AnnouncePlanArgs{
		Goal:         "g",
		Context:      "Test context for passthrough of details into PlanState.",
		WhatNotToDo:  "Do not change the Backend interface.",
		Verification: "go test ./pkg/agent/...",
		Steps: []PlanStepInput{
			{
				Name:        "a",
				Description: "desc a",
				Details:     "do x then y",
				Summary:     "Test summary for passthrough",
				Outcome:     "Details survive announce_plan into PlanState",
				Files:       []PlanFileChangeInput{{Path: "pkg/a.go", Kind: "modify"}},
				Verify:      "go test ./pkg/agent/ -run TestAnnouncePlan",
				VerifyKind:  agent.VerifyKindUnit,
			},
		},
	})
	if err != nil {
		t.Fatalf("announcePlan error: %v", err)
	}
	if len(gotSteps) != 1 || gotSteps[0].Details != "do x then y" {
		t.Fatalf("details not passed through: %+v", gotSteps)
	}
	if capturedEvent.PlanContext != "Test context for passthrough of details into PlanState." {
		t.Errorf("PlanContext = %q, want %q", capturedEvent.PlanContext, "Test context for passthrough of details into PlanState.")
	}
}

func TestAnnouncePlanTool_RejectsIncompleteSteps(t *testing.T) {
	orig := planStateCallback
	defer func() { planStateCallback = orig }()
	called := false
	SetPlanStateCallback(func(string, agent.PlanDocumentInfo, []agent.PlanStepInfo) bool {
		called = true
		return true
	})

	res, err := announcePlan(nil, AnnouncePlanArgs{
		Goal:  "g",
		Steps: []PlanStepInput{{Name: "a", Description: "desc a"}},
	})
	if err != nil {
		t.Fatalf("announcePlan error: %v", err)
	}
	if res.Status != "incomplete_plan" {
		t.Fatalf("status = %q, want incomplete_plan", res.Status)
	}
	if !strings.Contains(res.Message, "details") || !strings.Contains(res.Message, "files") {
		t.Fatalf("message = %q, want details and files", res.Message)
	}
	if !strings.Contains(res.Message, "slice by user-visible capability") {
		t.Errorf("message should tell the model how to split, got %q", res.Message)
	}
	if !strings.Contains(res.Message, "context") {
		t.Errorf("message should mention missing context, got %q", res.Message)
	}
	if called {
		t.Fatal("incomplete plan must not store PlanState")
	}
}

func TestAnnouncePlanTool_RejectsEmptyContext(t *testing.T) {
	planStateCallback = func(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo) bool {
		t.Fatal("state callback should not be called for incomplete plan")
		return true
	}
	defer func() { planStateCallback = nil }()

	args := AnnouncePlanArgs{
		Goal:    "test",
		Context: "", // empty — should be rejected
		Steps: []PlanStepInput{{
			Name:        "a",
			Description: "desc a",
			Details:     "do the thing",
			Summary:     "user sees the thing",
			Files:       []PlanFileChangeInput{{Path: "pkg/foo.go"}},
		}},
	}
	res, err := announcePlan(nil, args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "incomplete_plan" {
		t.Fatalf("status = %q, want incomplete_plan", res.Status)
	}
	if !strings.Contains(res.Message, "context") {
		t.Errorf("message should mention missing context, got %q", res.Message)
	}
}

func TestAnnouncePlanTool_RejectsMissingSummary(t *testing.T) {
	planStateCallback = func(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo) bool {
		t.Fatal("state callback should not be called for incomplete plan")
		return true
	}
	defer func() { planStateCallback = nil }()

	args := AnnouncePlanArgs{
		Goal:    "test",
		Context: "This is the context for the plan",
		Steps: []PlanStepInput{{
			Name:        "a",
			Description: "desc a",
			Details:     "do the thing",
			Summary:     "", // empty — should be rejected
			Files:       []PlanFileChangeInput{{Path: "pkg/foo.go"}},
		}},
	}
	res, err := announcePlan(nil, args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "incomplete_plan" {
		t.Fatalf("status = %q, want incomplete_plan", res.Status)
	}
	if !strings.Contains(res.Message, "summary") {
		t.Errorf("message should mention missing summary, got %q", res.Message)
	}
}

func TestAnnouncePlanTool_AcceptsCompleteArgs(t *testing.T) {
	var called bool
	planStateCallback = func(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo) bool {
		called = true
		return true
	}
	planProgressCallback = func(event agent.SubTaskProgressEvent) {}
	defer func() {
		planStateCallback = nil
		planProgressCallback = nil
	}()

	args := AnnouncePlanArgs{
		Goal:         "test",
		Context:      "This plan fixes the widget. We chose approach A because...",
		WhatNotToDo:  "Do not change the API interface.",
		Verification: "go test ./pkg/widget/...",
		Steps: []PlanStepInput{{
			Name:        "impl",
			Description: "Implement the widget fix",
			Details:     "Change WidgetFoo in pkg/widget/foo.go to handle nil",
			Summary:     "Users no longer see a crash when opening an empty widget",
			Outcome:     "Opening an empty widget no longer crashes",
			Files:       []PlanFileChangeInput{{Path: "pkg/widget/foo.go", Kind: "modify"}},
			Verify:      "go test ./pkg/widget/...",
			VerifyKind:  agent.VerifyKindUnit,
		}},
	}
	res, err := announcePlan(nil, args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok; message = %q", res.Status, res.Message)
	}
	if !called {
		t.Fatal("planStateCallback should have been called")
	}
}

func TestAnnouncePlanTool_RejectsMissingOutcomeAndVerify(t *testing.T) {
	planStateCallback = func(string, agent.PlanDocumentInfo, []agent.PlanStepInfo) bool {
		t.Fatal("state callback should not be called")
		return true
	}
	defer func() { planStateCallback = nil }()

	res, err := announcePlan(nil, AnnouncePlanArgs{
		Goal:         "test",
		Context:      "This is the context for the plan",
		WhatNotToDo:  "Do not change the API.",
		Verification: "go test ./pkg/widget/...",
		Steps: []PlanStepInput{{
			Name:        "a",
			Description: "desc a",
			Details:     "do the thing",
			Summary:     "user sees the thing",
			Files:       []PlanFileChangeInput{{Path: "pkg/foo.go"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "incomplete_plan" {
		t.Fatalf("status = %q, want incomplete_plan; message = %q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "outcome") || !strings.Contains(res.Message, "verify") {
		t.Errorf("message = %q, want outcome and verify", res.Message)
	}
}

func TestAnnouncePlanTool_RejectsUnitVerifyOnRunningSurface(t *testing.T) {
	called := false
	planStateCallback = func(string, agent.PlanDocumentInfo, []agent.PlanStepInfo) bool {
		called = true
		return true
	}
	defer func() { planStateCallback = nil }()

	res, err := announcePlan(nil, AnnouncePlanArgs{
		Goal:         "replace incus",
		Context:      "THE PROBLEM: multiple sandbox backends. THE APPROACH: Docker+overlay.",
		WhatNotToDo:  "Do not change the Backend interface.",
		Verification: "docker inspect astonish-session-test",
		Steps: []PlanStepInput{{
			Name:        "new-docker-backend",
			Description: "write docker backend",
			Details:     "create pkg/sandbox/docker",
			Summary:     "Docker backend exists",
			Outcome:     "A session container exists",
			Files:       []PlanFileChangeInput{{Path: "pkg/sandbox/docker/backend.go", Kind: "new"}},
			Verify:      "go test ./pkg/sandbox/docker/...",
			VerifyKind:  agent.VerifyKindUnit,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "incomplete_plan" {
		t.Fatalf("status = %q, want incomplete_plan; message = %q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "behavior") {
		t.Errorf("message = %q, want behavior requirement", res.Message)
	}
	if called {
		t.Fatal("invalid plan must not store PlanState")
	}
}

func TestAnnounceCompletionTool_RequiresOutcome(t *testing.T) {
	orig := planCompletionCallback
	defer func() { planCompletionCallback = orig }()
	called := false
	SetPlanCompletionCallback(func(outcomeObserved, unverified string) agent.PlanCompletionResult {
		called = true
		return agent.PlanCompletionResult{Code: "ok"}
	})

	res, err := announceCompletion(nil, AnnounceCompletionArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "incomplete" {
		t.Fatalf("status = %q, want incomplete", res.Status)
	}
	if called {
		t.Fatal("empty outcome_observed must not invoke the callback")
	}
}

func TestAnnounceCompletionTool_DrivesCallback(t *testing.T) {
	orig := planCompletionCallback
	defer func() { planCompletionCallback = orig }()
	var gotOutcome, gotUnverified string
	SetPlanCompletionCallback(func(outcomeObserved, unverified string) agent.PlanCompletionResult {
		gotOutcome, gotUnverified = outcomeObserved, unverified
		return agent.PlanCompletionResult{Code: "ok", Message: "accepted", Log: "exit 0"}
	})

	res, err := announceCompletion(nil, AnnounceCompletionArgs{
		OutcomeObserved: "docker inspect shows the session container",
		Unverified:      "no UI smoke",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" || res.Log != "exit 0" {
		t.Fatalf("result = %+v", res)
	}
	if gotOutcome != "docker inspect shows the session container" || gotUnverified != "no UI smoke" {
		t.Fatalf("callback got (%q, %q)", gotOutcome, gotUnverified)
	}
}
