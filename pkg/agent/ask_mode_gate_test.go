package agent

import (
	"strings"
	"testing"
)

// askModeToolBlocked mirrors the runtime gate decision in chat_agent_run.go:
// in ask mode, delegate_tasks, announce_plan, and any non-read-only tool are refused.
func askModeToolBlocked(name string) bool {
	return name == "delegate_tasks" || name == "announce_plan" || !IsToolSafe(name)
}

func TestAskModeGate_BlocksMutatingTools(t *testing.T) {
	blocked := []string{
		"write_file",
		"edit_file",
		"shell_command",
		"delegate_tasks",
		"announce_plan",
		"memory_save",
	}
	for _, name := range blocked {
		if !askModeToolBlocked(name) {
			t.Errorf("ask mode should block mutating/delegation/plan tool %q", name)
		}
	}
}

func TestAskModeGate_AllowsReadOnlyTools(t *testing.T) {
	allowed := []string{
		"read_file",
		"grep_search",
		"find_files",
		"file_tree",
		"memory_search",
		"repo_map",
		"code_definition",
		"code_references",
		"codegraph_explore",
		"web_fetch",
		"read_pdf",
		"filter_json",
		"skill_lookup",
	}
	for _, name := range allowed {
		if askModeToolBlocked(name) {
			t.Errorf("ask mode should allow read-only tool %q", name)
		}
	}
}

func TestAskModeBlockedMessage_NamesToolAndMode(t *testing.T) {
	msg := AskModeBlockedMessage("write_file")
	if !strings.Contains(msg, "write_file") {
		t.Errorf("blocked message should name the tool, got %q", msg)
	}
	if !strings.Contains(msg, "Ask mode") && !strings.Contains(msg, "ASK MODE") {
		t.Errorf("blocked message should mention Ask mode, got %q", msg)
	}
}

func TestAskModeSystemContext_ResearchLanguage(t *testing.T) {
	for _, want := range []string{"ASK MODE", "research", "read-only", "announce_plan"} {
		if !strings.Contains(AskModeSystemContext, want) {
			t.Errorf("AskModeSystemContext should mention %q", want)
		}
	}
	// Must not encourage planning
	if strings.Contains(AskModeSystemContext, "produce a COMPLETE plan") {
		t.Error("AskModeSystemContext should not instruct the model to produce a plan")
	}
}
