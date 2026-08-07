package agent

import (
	"fmt"

	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
)

// PlanModeSystemContext is the per-turn instruction injected when plan mode is
// active. It is the single source of truth shared by every chat surface (code
// mode, platform chat, Studio). The runtime gate in WrapToolsForChat enforces
// the "no changes" rule regardless of whether the model honors this prose.
const PlanModeSystemContext = `You are in Astonish PLAN MODE. This is a hard constraint enforced by the runtime, not a suggestion.

RULES:
- You MUST NOT make any changes. Mutating tools (write_file, edit_file, shell_command, and every other non-read-only tool) and delegate_tasks are DISABLED by the runtime and will be refused if you call them.
- You MAY use read-only tools (read_file, grep_search, find_files, file_tree, code_definition, code_references, repo_map, memory_search, etc.) to investigate and build an accurate plan.
- Do NOT attempt to execute the plan. End by asking the user to exit Plan mode (shift+tab) to proceed with execution.

Your job is to produce a COMPLETE plan the user can approve with confidence — not a partial sketch. Work through these four disciplines:

1. INVESTIGATE THOROUGHLY. Understand the code you will touch before you plan it. Use repo_map once to orient in unfamiliar areas, then code_definition to read the actual declaration of each symbol you will change, and code_references to enumerate ALL its call sites. Read the real regions with read_file. Batch independent read-only lookups in the same turn so they run in parallel. Keep investigating until you are confident no affected file, caller, interface, type, test, migration, generated file, or doc remains unexamined — first-pass results routinely miss dependents.

2. COVER EVERY DEPENDENCY — NO PARTIAL IMPLEMENTATIONS. A complete plan touches every layer the change reaches: the symbol itself AND all its callers, the interfaces/schemas/types it depends on, the tests that exercise it, any generated code that must be regenerated, migrations, and the docs (AGENTS.md / docs/architecture) the project requires. Order phases dependency-first: shared types and interfaces before the consumers that use them. Verify that no phase leaves orphaned or unwired code — every new symbol must be integrated by the end of the plan.

3. SURFACE DECISIONS FOR THE USER. Call out anything that needs a human decision — breaking changes, meaningful alternative approaches with their trade-offs, or ambiguous requirements — explicitly in the plan so the user can decide before execution begins. If a pivotal requirement is genuinely ambiguous and you cannot resolve it by reading the code, ask ONE concise clarifying question rather than guessing.

4. BE EFFICIENT — SPEND EFFORT PROPORTIONAL TO BLAST RADIUS. A one-file tweak needs a quick look; a cross-cutting change needs full tracing. Stop exploring once you can name every file you would change and why — do not read the whole repo. Prefer structural tools (code_definition/code_references) over broad grep, and never re-read a file already in your context.

When your plan is finalized, record it with announce_plan (goal + ordered, dependency-first phases). For each phase, list its affected files (each marked new/modify/delete), put the concrete approach in details, and give a verify step (the build/test/lint command that proves the phase is done). This persists the plan to a session PLAN.md that survives context compaction; do NOT hand-write PLAN.md yourself. (You will drive phase status with update_plan once execution begins.)`

// PlanModeBlockedMessage is returned to the model when it calls a mutating tool
// while plan mode is active. Returning a result (rather than an error that
// aborts the turn) lets the model self-correct and continue producing the plan.
func PlanModeBlockedMessage(toolName string) string {
	return fmt.Sprintf(
		"Blocked: `%s` cannot run in Plan mode. You are in PLAN MODE — no changes are allowed. "+
			"Present your implementation plan instead and ask the user to exit Plan mode (shift+tab) before any execution.",
		toolName,
	)
}

// SafeTools are read-only tools that auto-approve in chat mode.
// These tools cannot modify the filesystem or execute commands.
var SafeTools = map[string]bool{
	"read_file":   true,
	"file_tree":   true,
	"find_files":  true,
	"grep_search": true,
	// Tree-sitter structural navigation — read-only symbol lookups. They parse
	// source and return definition/reference locations; they never modify the
	// filesystem, so they must be safe (auto-approve) and allowed in Plan mode.
	"repo_map":                  true,
	"code_definition":           true,
	"code_references":           true,
	"git_diff_add_line_numbers": true,
	"filter_json":               true,
	"web_fetch":                 true,
	"read_pdf":                  true,
	"memory_search":             true,
	"memory_get":                true,
	"skill_lookup":              true,
	"process_list":              true,
	"process_read":              true,
	// announce_plan records the execution plan (in-memory PlanState + the
	// session PLAN.md sidecar). It performs no arbitrary FS mutation, shell
	// execution, or delegation, so it is safe to run — and MUST be allowed in
	// Plan mode so the model can record its finalized plan while planning.
	"announce_plan": true,
	// update_plan transitions a single plan phase (running/complete/failed).
	// Like announce_plan it only touches plan state + the PLAN.md sidecar, so
	// it is safe and allowed in Plan mode.
	"update_plan": true,
	// Browser observation tools (read-only)
	"browser_snapshot":         true,
	"browser_take_screenshot":  true,
	"browser_console_messages": true,
	"browser_network_requests": true,
	// Browser navigation & interaction (operates in sandboxed browser)
	"browser_navigate":         true,
	"browser_navigate_back":    true,
	"browser_click":            true,
	"browser_type":             true,
	"browser_hover":            true,
	"browser_drag":             true,
	"browser_press_key":        true,
	"browser_select_option":    true,
	"browser_fill_form":        true,
	"browser_highlight":        true,
	"browser_clear_highlights": true,
	"browser_move_cursor":      true,
	// Browser management
	"browser_tabs":          true,
	"browser_close":         true,
	"browser_resize":        true,
	"browser_fullscreen":    true,
	"browser_wait_for":      true,
	"browser_file_upload":   true,
	"browser_handle_dialog": true,
	// Browser advanced
	"browser_evaluate":      true,
	"browser_run_code":      true,
	"browser_pdf":           true,
	"browser_response_body": true,
	// Browser state & emulation (Phase 2)
	"browser_cookies":         true,
	"browser_storage":         true,
	"browser_set_offline":     true,
	"browser_set_headers":     true,
	"browser_set_credentials": true,
	"browser_set_geolocation": true,
	"browser_set_media":       true,
	"browser_set_timezone":    true,
	"browser_set_locale":      true,
	"browser_set_device":      true,
}

// IsToolSafe returns true if the tool is read-only and safe to auto-approve.
func IsToolSafe(name string) bool {
	return SafeTools[name]
}

// WrapToolsForChat wraps tools with approval gates based on their category.
// Safe (read-only) tools are returned unwrapped and will auto-execute.
// Protected (write/exec) tools get wrapped in ProtectedTool for approval.
// If autoApprove is true, all tools are returned unwrapped.
func WrapToolsForChat(allTools []tool.Tool, state session.State,
	chatAgent *AstonishAgent, yieldFunc func(*session.Event, error) bool,
	autoApprove bool) []tool.Tool {

	if autoApprove {
		return allTools
	}

	wrapped := make([]tool.Tool, len(allTools))
	for i, t := range allTools {
		if IsToolSafe(t.Name()) {
			wrapped[i] = t
		} else {
			wrapped[i] = &ProtectedTool{
				Tool:      t,
				State:     state,
				Agent:     chatAgent,
				YieldFunc: yieldFunc,
			}
		}
	}
	return wrapped
}
