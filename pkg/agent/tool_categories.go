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
- Produce a concise, concrete implementation plan: the files/commands you would touch and the order of steps.
- Do NOT attempt to execute the plan. End by asking the user to exit Plan mode (shift+tab) to proceed with execution.`

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
