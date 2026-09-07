package agent

import (
	"strings"
	"testing"
)

func TestIsFailedFixFollowup(t *testing.T) {
	yes := []string{
		"Zero difference, zero!",
		"nothing changed at all",
		"the badge still doesn't show",
		"it does not show the cost savings",
		"same problem after rebuild",
		"How many times I will need to say the problem is not the binary?",
		"I ALWAYS run make build-all",
		"always rebuild before running",
		"this used to work yesterday",
		"the previous fix didn't stick",
		"still broken on restore",
		"this doesn't work",
		"Error: No such object: astonish-session",
		"how do you considered as completed when all phases completed",
		"undefined: OptionalTool",
		"build failed",
	}
	for _, s := range yes {
		if !IsFailedFixFollowup(s) {
			t.Errorf("IsFailedFixFollowup(%q) = false, want true", s)
		}
	}
	no := []string{
		"",
		"add a button to the footer",
		"I always use tabs in Go",
		"explain how routing works",
		"plan a new model picker",
	}
	for _, s := range no {
		if IsFailedFixFollowup(s) {
			t.Errorf("IsFailedFixFollowup(%q) = true, want false", s)
		}
	}
}

func TestAppendFailedFixFollowupContext(t *testing.T) {
	got := AppendFailedFixFollowupContext("")
	if got != FailedFixFollowupContext {
		t.Fatalf("empty: got %q", got)
	}
	withPlan := AppendFailedFixFollowupContext("You are in GRAPH-OPTIMIZED PLAN MODE.")
	if !strings.Contains(withPlan, "GRAPH-OPTIMIZED PLAN MODE") || !strings.Contains(withPlan, FailedFixFollowupContext) {
		t.Fatalf("append: got %q", withPlan)
	}
	if AppendFailedFixFollowupContext(withPlan) != withPlan {
		t.Fatal("second append should be a no-op")
	}
}

func TestIsLiveSurfaceFollowup(t *testing.T) {
	yes := []string{
		"Do you have chromium available in shell_command?",
		"which chromium",
		"browser_navigate failed to get browser page",
		"no running sandbox for session",
		"CDP is not bound to a session container",
		"is the overlayfs mounted",
		"in the session the binary is missing",
		"CloakBrowser chrome segfault",
		"astonish-session-c1b27dfb is up",
		"chromium is not installed",
	}
	for _, s := range yes {
		if !IsLiveSurfaceFollowup(s) {
			t.Errorf("IsLiveSurfaceFollowup(%q) = false, want true", s)
		}
	}
	no := []string{
		"",
		"add a button to the footer",
		"the badge still doesn't show",
		"zero difference after rebuild",
		"explain how routing works",
	}
	for _, s := range no {
		if IsLiveSurfaceFollowup(s) {
			t.Errorf("IsLiveSurfaceFollowup(%q) = true, want false", s)
		}
	}
}

func TestFollowupInvestigationContext_LiveWinsAndStudioSandbox(t *testing.T) {
	chromium := "Do you have chromium available in shell_command?"
	if got := FollowupInvestigationContext(chromium, true, true); got != LiveSurfaceFollowupContext {
		t.Fatalf("code+live: got %q", got)
	}
	if got := FollowupInvestigationContext(chromium, false, true); got != LiveSurfaceFollowupContext {
		t.Fatalf("studio+live: got %q", got)
	}

	noDiff := "No difference"
	if got := FollowupInvestigationContext(noDiff, true, false); got != FailedFixFollowupContext {
		t.Fatalf("code no-difference without live keywords should be debug-regression, got %q", got)
	}
	if got := FollowupInvestigationContext(noDiff, false, true); got != LiveSurfaceFollowupContext {
		t.Fatalf("studio sandbox no-difference should inspect live, got %q", got)
	}
	if got := FollowupInvestigationContext(noDiff, false, false); got != "" {
		t.Fatalf("studio without sandbox should not inject, got %q", got)
	}

	both := "browser_navigate still broken, zero difference"
	if got := FollowupInvestigationContext(both, true, true); got != LiveSurfaceFollowupContext {
		t.Fatalf("live keywords win over failed-fix, got %q", got)
	}
}

func TestFailedFixFollowup_TurnContextIncludesBlock(t *testing.T) {
	overrides := &PromptOverrides{SessionContext: GraphPlanModeSystemContext}
	if !IsFailedFixFollowup("zero difference, I always rebuild") {
		t.Fatal("precondition")
	}
	overrides.SessionContext = AppendFailedFixFollowupContext(overrides.SessionContext)
	content := buildTurnContextContent(overrides, "", "")
	if content == nil || len(content.Parts) == 0 {
		t.Fatal("expected turn context content")
	}
	text := content.Parts[0].Text
	if !strings.Contains(text, FailedFixFollowupContext) {
		t.Fatalf("turn context missing follow-up block:\n%s", text)
	}
	if !strings.Contains(text, "GRAPH-OPTIMIZED PLAN MODE") {
		t.Fatalf("turn context dropped plan-mode context:\n%s", text)
	}
}

func TestCodeSystemPromptBuilder_SetsCodeMode(t *testing.T) {
	cb := NewCodeSystemPromptBuilder(&SystemPromptBuilder{})
	if !cb.CodeMode {
		t.Fatal("NewCodeSystemPromptBuilder must set CodeMode")
	}
	clone := cb.SystemPromptBuilder.Clone()
	if clone == nil || !clone.CodeMode {
		t.Fatal("cloned system prompt must retain CodeMode")
	}
}
