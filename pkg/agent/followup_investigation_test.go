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
