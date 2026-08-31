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
	SetPlanStepUpdateCallback(func(step, status string) (string, string) {
		gotStep, gotStatus = step, status
		return step, "complete" // simulate canonical applied status
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
	defer func() { planStepUpdateCallback = orig }()
	SetPlanStepUpdateCallback(func(step, status string) (string, string) { return "", "" })

	res, err := updatePlan(nil, UpdatePlanArgs{Step: "missing", Status: "running"})
	if err != nil {
		t.Fatalf("updatePlan error: %v", err)
	}
	if res.Status != "step_not_found" {
		t.Errorf("status = %q, want step_not_found", res.Status)
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
		Goal:    "replacement",
		Context: "Test context for validation",
		Steps: []PlanStepInput{{
			Name:        "replace",
			Description: "replace approved plan",
			Details:     "Rewrite pkg/agent/plan_state.go SetActivePlan to reject replacements.",
			Summary:     "Test summary",
			Files:       []PlanFileChangeInput{{Path: "pkg/agent/plan_state.go", Kind: "modify"}},
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
		Goal:    "g",
		Context: "Test context for passthrough",
		Steps: []PlanStepInput{
			{
				Name:        "a",
				Description: "desc a",
				Details:     "do x then y",
				Summary:     "Test summary for passthrough",
				Files:       []PlanFileChangeInput{{Path: "pkg/a.go", Kind: "modify"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("announcePlan error: %v", err)
	}
	if len(gotSteps) != 1 || gotSteps[0].Details != "do x then y" {
		t.Fatalf("details not passed through: %+v", gotSteps)
	}
	if capturedEvent.PlanContext != "Test context for passthrough" {
		t.Errorf("PlanContext = %q, want %q", capturedEvent.PlanContext, "Test context for passthrough")
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
		Verification: "go test ./...",
		Steps: []PlanStepInput{{
			Name:        "impl",
			Description: "Implement the widget fix",
			Details:     "Change WidgetFoo in pkg/widget/foo.go to handle nil",
			Summary:     "Users no longer see a crash when opening an empty widget",
			Files:       []PlanFileChangeInput{{Path: "pkg/widget/foo.go", Kind: "modify"}},
			Verify:      "go test ./pkg/widget/...",
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
