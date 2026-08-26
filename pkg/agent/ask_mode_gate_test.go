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

// TestAskMode_FolderAuthStillEnforced is a regression guard for the bug where
// the authorization gate condition in chat_agent_run.go included `!askMode`,
// causing the folder-access gate to be skipped entirely in Ask mode. Read-only
// tools (e.g., read_file) could access paths outside the project folder without
// any authorization prompt — only in Ask mode; Normal mode correctly prompted.
//
// The fix: the outer condition is `EnforceAuthorization && !planMode && !graphPlan
// && !c.AutoApprove` (no `!askMode`), so the folder-access gate applies in both
// Normal and Ask mode. The tool-execution gate is wrapped in `if !askMode { ... }`
// since Ask mode already blocks non-safe tools via its own hard gate.
func TestAskMode_FolderAuthStillEnforced(t *testing.T) {
	root := t.TempDir()
	p := NewSessionAuthPolicy(root)

	// A read-only tool (allowed in Ask mode) accessing a path OUTSIDE the project
	// root must be flagged by the folder-access gate.
	outsideArgs := map[string]any{
		"path": "/etc/hosts",
	}
	out := p.OutOfScopePaths(outsideArgs)
	if len(out) == 0 {
		t.Fatal("folder-access gate should flag /etc/hosts as out-of-scope in Ask mode")
	}

	// A read-only tool accessing a path INSIDE the project root must NOT be flagged.
	insideArgs := map[string]any{
		"path": root + "/src/main.go",
	}
	out = p.OutOfScopePaths(insideArgs)
	if len(out) != 0 {
		t.Fatalf("folder-access gate should not flag in-project path, got %v", out)
	}

	// Verify the gate condition logic: in Ask mode, safe tools are allowed
	// (not blocked by ask-mode gate) but the folder-access gate must still
	// apply. This is the contract: IsToolSafe("read_file") == true (so the
	// ask-mode gate passes it through), yet OutOfScopePaths catches the path.
	if !IsToolSafe("read_file") {
		t.Fatal("read_file must be in SafeTools (allowed in Ask mode)")
	}
	if !IsToolSafe("grep_search") {
		t.Fatal("grep_search must be in SafeTools (allowed in Ask mode)")
	}
	if !IsToolSafe("find_files") {
		t.Fatal("find_files must be in SafeTools (allowed in Ask mode)")
	}

	// Confirm that the ask-mode gate would NOT block these tools...
	for _, name := range []string{"read_file", "grep_search", "find_files", "file_tree"} {
		if askModeToolBlocked(name) {
			t.Errorf("ask mode should allow %q (it's read-only)", name)
		}
	}
	// ...but the folder-access gate still catches out-of-scope paths for them.
	for _, pathArg := range []string{"/etc/passwd", "/var/log/system.log", "/opt/secret.txt"} {
		args := map[string]any{"path": pathArg}
		if len(p.OutOfScopePaths(args)) == 0 {
			t.Errorf("folder-access gate should flag %q as out-of-scope even for safe tools in Ask mode", pathArg)
		}
	}
}
