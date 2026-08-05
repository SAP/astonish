package launcher

import "testing"

// TestMainThreadToolAllowlist_IncludesTreeSitter guards the fix for slow code
// mode: the structural navigation tools must be available to the top-level
// agent directly, so it does not fall back to grep_search + repeated read_file
// (which inflates context and slows inference). See
// docs/architecture/code-intelligence.md and the ce7f4295 session analysis.
func TestMainThreadToolAllowlist_IncludesTreeSitter(t *testing.T) {
	allow := mainThreadToolAllowlist()
	for _, name := range []string{"repo_map", "code_definition", "code_references"} {
		if !allow[name] {
			t.Errorf("expected %q in the main-thread tool allowlist (structural navigation must be directly available)", name)
		}
	}
}

func TestMainThreadToolAllowlist_CoreEditingTools(t *testing.T) {
	allow := mainThreadToolAllowlist()
	for _, name := range []string{"read_file", "write_file", "edit_file", "shell_command", "grep_search", "find_files"} {
		if !allow[name] {
			t.Errorf("expected core editing tool %q in the main-thread allowlist", name)
		}
	}
}
