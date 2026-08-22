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

	plan := NewPlanState("Build feature", PlanDocumentInfo{}, []PlanStepInfo{
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
	plan := NewPlanState("goal", PlanDocumentInfo{}, []PlanStepInfo{{Name: "a", Description: "b"}})
	// Must not panic and must not attempt any write.
	c.SetActivePlan(plan)
	plan.AdvanceOnToolStart()
	if c.GetActivePlan() != plan {
		t.Fatal("active plan not stored")
	}
}

func TestChatAgent_ApprovedPlanRejectsReplacementAndStaleWrites(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "sess.PLAN.md")

	c := &ChatAgent{}
	c.SetPlanFilePath(planPath)
	approved := NewPlanState("Approved goal", PlanDocumentInfo{}, []PlanStepInfo{
		{Name: "implement", Description: "write approved code"},
	})
	if !c.TrySetActivePlan(approved) {
		t.Fatal("first announcement should be accepted")
	}
	c.MarkActivePlanApproved()

	replacement := NewPlanState("Replacement goal", PlanDocumentInfo{}, []PlanStepInfo{
		{Name: "other", Description: "replace approved code"},
	})
	if c.TrySetActivePlan(replacement) {
		t.Fatal("approved plan must reject replacement")
	}
	if c.GetActivePlan() != approved {
		t.Fatal("rejected replacement changed the active plan")
	}

	// update_plan remains valid after approval and persists against the sealed plan.
	name, status := approved.SetStepStatus("implement", "running")
	if name != "implement" || status != "running" {
		t.Fatalf("approved update = (%q, %q), want (implement, running)", name, status)
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read approved plan: %v", err)
	}
	if !strings.Contains(string(data), "**Goal:** Approved goal") ||
		!strings.Contains(string(data), "- [~] **implement**") ||
		strings.Contains(string(data), "Replacement goal") {
		t.Fatalf("approved PLAN.md was replaced or not updated:\n%s", data)
	}
}

func TestChatAgent_ReplacedPlanIgnoresStaleOnChange(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "sess.PLAN.md")

	c := &ChatAgent{}
	c.SetPlanFilePath(planPath)
	oldPlan := NewPlanState("Old goal", PlanDocumentInfo{}, []PlanStepInfo{
		{Name: "old", Description: "old work"},
	})
	if !c.TrySetActivePlan(oldPlan) {
		t.Fatal("first announcement should be accepted")
	}

	c.AllowActivePlanReplacement()
	newPlan := NewPlanState("New goal", PlanDocumentInfo{}, []PlanStepInfo{
		{Name: "new", Description: "new work"},
	})
	if !c.TrySetActivePlan(newPlan) {
		t.Fatal("explicit planning revision should be accepted")
	}

	// Simulate a callback from the superseded plan completing after replacement.
	oldPlan.SetStepStatus("old", "complete")
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read current plan: %v", err)
	}
	if !strings.Contains(string(data), "**Goal:** New goal") || strings.Contains(string(data), "Old goal") {
		t.Fatalf("stale callback rewrote PLAN.md:\n%s", data)
	}
}

func TestChatAgent_RestoreApprovedPlanPreservesStatus(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "sess.PLAN.md")

	c := &ChatAgent{}
	c.SetPlanFilePath(planPath)
	plan := NewPlanState("Build", PlanDocumentInfo{}, []PlanStepInfo{
		{Name: "explore", Description: "investigate", Details: "read files", Files: []PlanFileChange{{Path: "pkg/a.go", Kind: "modify"}}},
		{Name: "implement", Description: "write code", Details: "edit pkg/a.go", Files: []PlanFileChange{{Path: "pkg/a.go", Kind: "modify"}}},
	})
	if !c.TrySetActivePlan(plan) {
		t.Fatal("announce should be accepted")
	}
	if name, status := plan.SetStepStatus("explore", "complete"); name != "explore" || status != "complete" {
		t.Fatalf("SetStepStatus = (%q,%q)", name, status)
	}
	if name, status := plan.SetStepStatus("implement", "running"); name != "implement" || status != "running" {
		t.Fatalf("SetStepStatus = (%q,%q)", name, status)
	}

	c2 := &ChatAgent{}
	c2.SetPlanFilePath(planPath)
	if err := c2.RestoreApprovedPlan(); err != nil {
		t.Fatalf("RestoreApprovedPlan: %v", err)
	}
	if !c2.IsActivePlanApproved() {
		t.Fatal("restored plan must be marked approved")
	}
	restored := c2.GetActivePlan()
	if restored == nil {
		t.Fatal("restored plan is nil")
	}
	_, steps := restored.SnapshotInfo()
	if len(steps) != 2 {
		t.Fatalf("steps = %d", len(steps))
	}
	if steps[0].Status != "complete" || steps[1].Status != "running" {
		t.Fatalf("restored statuses = %q/%q, want complete/running", steps[0].Status, steps[1].Status)
	}
}

func TestChatAgent_UpdatePlanRewritesPlanFile(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "sess.PLAN.md")

	c := &ChatAgent{}
	c.SetPlanFilePath(planPath)
	plan := NewPlanState("Build", PlanDocumentInfo{}, []PlanStepInfo{
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
