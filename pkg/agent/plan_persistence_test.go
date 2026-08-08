package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatAgent_SetActivePlanWritesPlanFile(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "sess.PLAN.md")

	c := &ChatAgent{}
	c.SetPlanFilePath(planPath)

	plan := NewPlanState("Build feature", []PlanStepInfo{
		{Name: "explore", Description: "investigate"},
		{Name: "implement", Description: "write code"},
	})
	c.SetActivePlan(plan)

	// File is written immediately on announce.
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("PLAN.md not written on announce: %v", err)
	}
	initial := string(data)
	if !strings.Contains(initial, "**Goal:** Build feature") {
		t.Errorf("missing goal in PLAN.md:\n%s", initial)
	}
	if !strings.Contains(initial, "- [ ] **explore**") {
		t.Errorf("expected explore pending:\n%s", initial)
	}

	// A phase transition rewrites the file.
	if got := plan.AdvanceOnToolStart(); got != "explore" {
		t.Fatalf("AdvanceOnToolStart = %q, want explore", got)
	}
	data, err = os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("re-read PLAN.md: %v", err)
	}
	if !strings.Contains(string(data), "- [~] **explore**") {
		t.Errorf("expected explore running after advance:\n%s", string(data))
	}
}

func TestChatAgent_SetActivePlanNoFileWhenPathEmpty(t *testing.T) {
	c := &ChatAgent{}
	// No SetPlanFilePath call → persistence disabled.
	plan := NewPlanState("goal", []PlanStepInfo{{Name: "a", Description: "b"}})
	// Must not panic and must not attempt any write.
	c.SetActivePlan(plan)
	plan.AdvanceOnToolStart()
	if c.GetActivePlan() != plan {
		t.Fatal("active plan not stored")
	}
}

func TestChatAgent_UpdatePlanRewritesPlanFile(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "sess.PLAN.md")

	c := &ChatAgent{}
	c.SetPlanFilePath(planPath)
	plan := NewPlanState("Build", []PlanStepInfo{
		{Name: "explore", Description: "investigate", Details: "read files\ncheck deps"},
		{Name: "implement", Description: "write code"},
	})
	c.SetActivePlan(plan)

	// Details are persisted from the start.
	data, _ := os.ReadFile(planPath)
	if !strings.Contains(string(data), "read files") || !strings.Contains(string(data), "check deps") {
		t.Errorf("details not persisted:\n%s", string(data))
	}

	// Simulate update_plan marking explore complete.
	name, status := plan.SetStepStatus("explore", "done")
	if name != "explore" || status != "complete" {
		t.Fatalf("SetStepStatus = (%q,%q)", name, status)
	}
	data, _ = os.ReadFile(planPath)
	if !strings.Contains(string(data), "- [x] **explore**") {
		t.Errorf("explore not marked complete in file:\n%s", string(data))
	}
	if !strings.Contains(string(data), "- [ ] **implement**") {
		t.Errorf("implement should still be pending:\n%s", string(data))
	}
}
