package agent

import (
	"os"
	"strings"
	"testing"
)

// planModeToolBlocked mirrors the runtime gate decision in chat_agent_run.go:
// in plan mode, delegate_tasks and any non-read-only tool are refused.
func planModeToolBlocked(name string) bool {
	return name == "delegate_tasks" || !IsToolSafe(name)
}

func TestPlanModeGate_BlocksMutatingTools(t *testing.T) {
	blocked := []string{
		"write_file",
		"edit_file",
		"shell_command",
		"delegate_tasks",
		"memory_save",
	}
	for _, name := range blocked {
		if !planModeToolBlocked(name) {
			t.Errorf("plan mode should block mutating/delegation tool %q", name)
		}
	}
}

func TestPlanModeGate_AllowsReadOnlyTools(t *testing.T) {
	allowed := []string{
		"read_file",
		"grep_search",
		"find_files",
		"file_tree",
		"memory_search",
		// Tree-sitter structural navigation is read-only and must be usable
		// while planning (regression: these were wrongly blocked in Plan mode).
		"repo_map",
		"code_definition",
		"code_references",
		// announce_plan must be allowed so the model can record its finalized
		// plan (in-memory + session PLAN.md) while in Plan mode.
		"announce_plan",
	}
	for _, name := range allowed {
		if planModeToolBlocked(name) {
			t.Errorf("plan mode should allow read-only tool %q", name)
		}
	}
}

func TestPlanModeBlockedMessage_NamesToolAndMode(t *testing.T) {
	msg := PlanModeBlockedMessage("write_file")
	if !strings.Contains(msg, "write_file") {
		t.Errorf("blocked message should name the tool, got %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "plan mode") {
		t.Errorf("blocked message should remind the model it is in plan mode, got %q", msg)
	}
}

func TestApprovedPlanExecutionGate_BlocksOnlyReannouncement(t *testing.T) {
	if !approvedPlanExecutionToolBlocked("announce_plan") {
		t.Fatal("approved execution must block announce_plan")
	}
	for _, name := range []string{"update_plan", "write_file", "delegate_tasks"} {
		if approvedPlanExecutionToolBlocked(name) {
			t.Errorf("approved execution should allow %q", name)
		}
	}
	msg := ApprovedPlanExecutionBlockedMessage()
	for _, want := range []string{"announce_plan", "update_plan", "approved plan"} {
		if !strings.Contains(msg, want) {
			t.Errorf("blocked message should mention %q, got %q", want, msg)
		}
	}
}

func TestAnnouncePlanNotInPlanModeMessage(t *testing.T) {
	msg := AnnouncePlanNotInPlanModeBlockedMessage()
	for _, want := range []string{"announce_plan", "Plan mode", "Normal mode"} {
		if !strings.Contains(msg, want) {
			t.Errorf("blocked message should mention %q, got %q", want, msg)
		}
	}
}

func TestApprovedPlanExecutionResearch_AllowsReadsCapsDiscovery(t *testing.T) {
	if got := approvedExecutionResearchKind("read_file"); got != "" {
		t.Fatalf("read_file must not be classified as rediscovery, got %q", got)
	}
	if got := approvedExecutionResearchKind("read_pdf"); got != "" {
		t.Fatalf("read_pdf must not be classified as rediscovery, got %q", got)
	}
	if got := approvedExecutionResearchKind("write_file"); got != "" {
		t.Fatalf("write_file must not be classified as rediscovery, got %q", got)
	}
	if got := approvedExecutionResearchKind("codegraph_explore"); got != "codegraph" {
		t.Fatalf("codegraph_explore kind = %q, want codegraph", got)
	}
	if got := approvedExecutionResearchKind("grep_search"); got != "search" {
		t.Fatalf("grep_search kind = %q, want search", got)
	}

	if approvedExecutionResearchLimit("codegraph") != ApprovedExecutionMaxCodegraphCalls {
		t.Fatalf("codegraph limit = %d, want %d", approvedExecutionResearchLimit("codegraph"), ApprovedExecutionMaxCodegraphCalls)
	}
	if approvedExecutionResearchLimit("search") != ApprovedExecutionMaxSearchCalls {
		t.Fatalf("search limit = %d, want %d", approvedExecutionResearchLimit("search"), ApprovedExecutionMaxSearchCalls)
	}
	if approvedExecutionResearchLimit("read") != 0 {
		t.Fatal("read must have no execution research limit")
	}
}

func TestPlanExecutionSystemContext_ForbidsReannouncement(t *testing.T) {
	dir := t.TempDir()
	planPath := dir + "/session.PLAN.md"
	body := "# Execution Plan\n\n**Goal:** Ship it\n\n## Phases\n\n- [ ] **one** — First\n"
	if err := os.WriteFile(planPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := BuildPlanExecutionSystemContext(planPath)
	if !strings.Contains(ctx, "Do NOT call announce_plan") || !strings.Contains(ctx, "Use update_plan") {
		t.Fatalf("execution context must preserve the approved plan lifecycle:\n%s", ctx)
	}
	if !strings.Contains(ctx, "**Goal:** Ship it") || !strings.Contains(ctx, "**one**") {
		t.Fatalf("execution context must inline PLAN.md:\n%s", ctx)
	}
	if strings.Contains(ctx, "IMMEDIATE FIRST ACTION: Call read_file") {
		t.Fatal("execution context must not require a PLAN.md re-read")
	}
	if strings.Contains(ctx, "12 source reads") {
		t.Fatal("execution context must not impose a source-read ceiling")
	}
	if !strings.Contains(ctx, "once immediately before") {
		t.Fatal("execution context must tell the model to read a target file once immediately before writing")
	}
	if !strings.Contains(ctx, "do not re-read") {
		t.Fatal("execution context must tell the model not to re-read a path already in context")
	}
}

func TestPlanModeSystemContext_HardConstraintLanguage(t *testing.T) {
	// The prompt must clearly state the runtime enforces the no-changes rule
	// and enumerate that mutating tools + delegate_tasks are disabled.
	for _, want := range []string{"PLAN MODE", "delegate_tasks", "write_file", "read-only"} {
		if !strings.Contains(PlanModeSystemContext, want) {
			t.Errorf("PlanModeSystemContext should mention %q", want)
		}
	}
}
