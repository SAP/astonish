package launcher

import (
	"testing"

	"github.com/SAP/astonish/pkg/credentials"
)

func TestSubAgentCredentialStoreWiring(t *testing.T) {
	t.Run("nil store remains a nil interface", func(t *testing.T) {
		var store *credentials.Store
		resolver := optionalCredentialResolver(store)
		if resolver != nil {
			t.Fatalf("optionalCredentialResolver(nil) = %#v, want nil interface", resolver)
		}
	})

	t.Run("available store is wired as resolver", func(t *testing.T) {
		store, err := credentials.Open(t.TempDir())
		if err != nil {
			t.Fatalf("open credential store: %v", err)
		}

		resolver := optionalCredentialResolver(store)
		if resolver == nil {
			t.Fatal("optionalCredentialResolver(store) returned nil")
		}
		if got := resolver.Get("missing"); got != nil {
			t.Fatalf("resolver.Get(missing) = %#v, want nil", got)
		}
		if resolver != store {
			t.Fatal("resolver does not preserve the opened credential store")
		}
	})
}

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

// TestMainThreadToolAllowlist_InteractiveTerminalTools guards the fix for the
// interactive-terminal drive loop: shell_command runs in a PTY and can return
// waiting_for_input=true, so the top-level agent must directly hold the process_*
// tools to respond (process_write) without a search_tools detour — matching chat
// mode. See docs/architecture/terminal-app.md and the ff25d217 session analysis.
func TestMainThreadToolAllowlist_InteractiveTerminalTools(t *testing.T) {
	allow := mainThreadToolAllowlist()
	for _, name := range []string{"process_read", "process_write", "process_kill", "process_list"} {
		if !allow[name] {
			t.Errorf("expected interactive-terminal tool %q in the main-thread allowlist", name)
		}
	}
}

// TestMainThreadToolAllowlist_PlanTools guards the fix for the plan-tracking
// loop: both announce_plan and update_plan are surfaced directly to the
// top-level agent. The system prompt instructs the agent to call update_plan as
// it works on the main thread; if update_plan is missing from this allowlist it
// is relegated to the deferred "core" group (reachable only via search_tools)
// and calling it fails at runtime with "tool 'update_plan' not found" — the
// exact asymmetry where announce_plan worked but update_plan did not.
func TestMainThreadToolAllowlist_PlanTools(t *testing.T) {
	allow := mainThreadToolAllowlist()
	for _, name := range []string{"announce_plan", "update_plan"} {
		if !allow[name] {
			t.Errorf("expected plan tool %q in the main-thread allowlist (update_plan must be a direct companion to announce_plan)", name)
		}
	}
}

// TestMainThreadToolAllowlist_GraphPlanTransitionTools guards the fix for the
// Graph-Optimized Plan mode phase-transition tools. The phase state machine
// lives on the main-thread ChatAgent; advancing it from a sub-agent would
// silently fail because sub-agents have no ActiveGraphPlan. Without this entry,
// the model enters the GRAPH phase via codegraph_explore but then receives
// "tool 'gplan_reads' not found" when it tries to advance — leaving it stuck
// in the GRAPH phase with both read_file and grep_search gated out.
func TestMainThreadToolAllowlist_GraphPlanTransitionTools(t *testing.T) {
	allow := mainThreadToolAllowlist()
	for _, name := range []string{"gplan_reads", "gplan_gaps", "gplan_finalize"} {
		if !allow[name] {
			t.Errorf("expected graph-plan transition tool %q in the main-thread allowlist (phase transitions must target the main ChatAgent)", name)
		}
	}
}
