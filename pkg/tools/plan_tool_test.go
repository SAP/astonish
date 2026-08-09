package tools

import (
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

func TestAnnouncePlanTool_PassesDetailsThrough(t *testing.T) {
	orig := planStateCallback
	origProgress := planProgressCallback
	defer func() {
		planStateCallback = orig
		planProgressCallback = origProgress
	}()

	var gotSteps []agent.PlanStepInfo
	SetPlanStateCallback(func(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo) { gotSteps = steps })
	SetPlanProgressCallback(func(agent.SubTaskProgressEvent) {})

	_, err := announcePlan(nil, AnnouncePlanArgs{
		Goal: "g",
		Steps: []PlanStepInput{
			{Name: "a", Description: "desc a", Details: "do x then y"},
		},
	})
	if err != nil {
		t.Fatalf("announcePlan error: %v", err)
	}
	if len(gotSteps) != 1 || gotSteps[0].Details != "do x then y" {
		t.Fatalf("details not passed through: %+v", gotSteps)
	}
}
