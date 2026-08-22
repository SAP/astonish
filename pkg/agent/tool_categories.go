package agent

import (
	"fmt"
	"os"
	"strings"

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

When your plan is finalized, record it with announce_plan (goal + ordered, dependency-first phases). For each phase:
- 'files': REQUIRED. List every file the phase touches (marked new/modify/delete) — the symbol AND its callers, tests, generated code, migrations, docs.
- 'details': REQUIRED. Write a concrete, self-contained implementation spec — exact function/type/method names, signature changes, call-site updates, and test names. Execution must proceed from this text without a second codegraph pass. A sketch is incomplete.
- 'verify': the command that proves the phase is done (build/test/lint).

Before calling announce_plan, run this COMPLETENESS SELF-CHECK:
- [ ] If you are changing backend code: did you check whether the frontend (web/src/) consumes the affected API/event/type? If yes, add a phase for the frontend change.
- [ ] If you are changing the Studio Chat (web/src/components/StudioChat.tsx or SSE events): did you check whether the terminal TUI (pkg/tui/) has equivalent rendering that needs updating?
- [ ] If you are changing a type/interface: did codegraph show ALL callers? Add phases for every caller that needs updating.
- [ ] Did you check docs/architecture/ for documentation that describes the subsystem you're changing? If it exists, add a phase to update it.
- [ ] Did you check for existing tests (*_test.go, *.test.ts) covering the code you're changing? Add a phase for test updates or new tests.
- [ ] If you are adding a new tool or SSE event: does it need documentation in AGENTS.md or docs/architecture/?
- [ ] Are there any breaking changes or user-visible behavior differences? Surface them in the plan's context or as explicit decisions.

If any check reveals a gap, add the missing phase BEFORE calling announce_plan. Do NOT announce an incomplete plan.

Call announce_plan WITHOUT any preceding prose or summary — the plan document is shown directly to the user and speaks for itself. Do NOT write a "Here's my plan..." narration before the tool call. This persists the full plan to a session PLAN.md that survives context compaction and is shown to the user. Do NOT hand-write PLAN.md yourself. (You will drive phase status with update_plan once execution begins. When executing, treat PLAN.md as the authoritative source — do NOT re-investigate files or symbols already confirmed in the plan unless the code has changed since planning.)`

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

// GraphPlanModeSystemContext is the per-turn instruction injected when
// Graph-Optimized Plan mode is active (code mode only). The runtime enforces
// the phase gate; this prompt teaches the model the "plan-for-the-plan"
// discipline so it works WITH the gate instead of fighting it. It must stay in
// sync with the TUI mirror (pkg/tui/app.go: graphPlanModeSystemContext).
const GraphPlanModeSystemContext = `You are in Astonish GRAPH-OPTIMIZED PLAN MODE. This is a hard constraint enforced by the runtime through a staged tool gate, not a suggestion. Like Plan mode, this is a NO-CHANGES mode: write_file, edit_file, shell_command and every other mutating tool are DISABLED in every phase and will be refused.

The runtime advances through four phases. Each phase unlocks a specific set of tools; you move between phases by calling small transition tools. Do NOT try to call a tool before its phase — the gate will refuse it and tell you which phase it belongs to.

PHASE 1 — GRAPH (current at turn start). Only ` + "`codegraph_explore`" + ` and ` + "`find_files`" + ` are available. codegraph is a pre-computed knowledge graph of this repo: symbols, call edges, dependencies, cross-file references, and change blast-radius. Query it FIRST to understand the code you will touch — it answers most structural questions in 1-4 calls with far fewer tokens than grep. Compound your findings as you go: never re-query the graph for something already in your context. When you have identified the exact regions you need to read, call ` + "`gplan_reads`" + ` with the synthesized read list (each entry: path + why you need it). Only include paths that ` + "`codegraph_explore`" + ` explicitly returned — do NOT guess or infer filenames; if you need a file but do not have its confirmed path, use ` + "`find_files`" + ` to locate it first. This advances you to the READ phase. If codegraph returns no coverage (language unsupported / not indexed), call ` + "`gplan_gaps`" + ` immediately to skip straight to the GAP phase.

PHASE 2 — READ. ` + "`read_file`" + ` (and read_pdf/filter_json) unlock, plus codegraph_explore. There is no read quota — read every region on the list you recorded with gplan_reads. Never ` + "`read_file`" + ` a path whose contents are already in this turn's context, and do not re-search for information you already have. When you have read everything the graph pointed you to, decide: if genuine gaps remain that codegraph could not answer, call ` + "`gplan_gaps`" + ` with those gaps (each: the question + why codegraph was insufficient) to advance to the GAP phase. If there are no gaps, call ` + "`gplan_finalize`" + ` to skip straight to the PLAN phase.

PHASE 3 — GAP (complementary). The remaining read-only tools unlock: grep_search, find_files, file_tree, repo_map, code_definition, code_references, web_fetch, memory_search, memory_get, skill_lookup — and delegate_tasks. Use these ONLY for the genuine gaps codegraph could not fill. Prefer ` + "`delegate_tasks`" + ` with read-only ` + "`tools`" + ` filters (e.g. ["grep_search","read_file","code_references"]) to fan out independent gap questions in parallel. Do not re-answer anything already established. When gaps are closed, call ` + "`gplan_finalize`" + ` to advance to the PLAN phase.

PHASE 4 — PLAN. ` + "`announce_plan`" + ` unlocks. Call it WITHOUT any preceding prose — the plan document is shown directly to the user. Record the finalized plan: goal + ordered, dependency-first phases. For each phase list its affected files (each marked new/modify/delete — the symbol AND its callers, tests, generated code, migrations, docs, so nothing is left unwired); write a concrete, self-contained 'details' field describing exactly what to do (specific functions/structs to add or change, the exact logic, new fields, interface updates — enough detail that execution can proceed directly from it without re-reading the code); and give a verify step (the build/test/lint command that proves the phase is done); and write a 'summary' for each phase — a 1-2 sentence plain-English explanation of what the phase accomplishes from the user's perspective (e.g. 'Updates the TUI to show both the main answer and the memory-save note instead of hiding the answer'). File paths must be confirmed: only record a file path in 'details' or 'files' if codegraph_explore, code_definition, find_files, or read_file explicitly returned that exact path this session — do NOT infer paths from symbol names or directory conventions; if a path was not confirmed, call find_files before adding it to the plan. This persists the full plan to a session PLAN.md shown to the user; do NOT hand-write PLAN.md. End by asking the user to exit to Normal mode (shift+tab) before any execution. When executing later, treat PLAN.md as authoritative — do NOT re-investigate files or symbols already confirmed in the plan.

Before calling announce_plan, run this COMPLETENESS SELF-CHECK:
- [ ] If you are changing backend code: did you check whether the frontend (web/src/) consumes the affected API/event/type? If yes, add a phase for the frontend change.
- [ ] If you are changing the Studio Chat (web/src/components/StudioChat.tsx or SSE events): did you check whether the terminal TUI (pkg/tui/) has equivalent rendering that needs updating?
- [ ] If you are changing a type/interface: did codegraph show ALL callers? Add phases for every caller that needs updating.
- [ ] Did you check docs/architecture/ for documentation that describes the subsystem you're changing? If it exists, add a phase to update it.
- [ ] Did you check for existing tests (*_test.go, *.test.ts) covering the code you're changing? Add a phase for test updates or new tests.
- [ ] If you are adding a new tool or SSE event: does it need documentation in AGENTS.md or docs/architecture/?
- [ ] Are there any breaking changes or user-visible behavior differences? Surface them in the plan's context or as explicit decisions.

If any check reveals a gap, add the missing phase BEFORE calling announce_plan. Do NOT announce an incomplete plan.

Produce a COMPLETE plan — cover every dependency the change reaches, order phases dependency-first, and surface any human decisions (breaking changes, alternatives with trade-offs, ambiguous requirements) explicitly. Spend effort proportional to blast radius. Stop when you can name every affected file and why — not because a counter tripped. Never re-query codegraph or grep for a fact already established.`

// PlanExecutionSystemContext is injected as the per-turn SystemContext when the
// user approves a plan and on every subsequent Normal turn while that plan is
// still the approved execution. It inlines PLAN.md so the model does not have
// to re-read or reconstruct the plan after compaction.
//
// Placeholders: "__PLAN_PATH__" (absolute sidecar path) and "__PLAN_BODY__"
// (the current PLAN.md contents). Use BuildPlanExecutionSystemContext.
const PlanExecutionSystemContext = `You are now in EXECUTION MODE — an approved plan is active.

The approved plan is inlined below. Treat it as the authoritative source of phases, files, details,
and progress. Do NOT call announce_plan (that tool exists only in Plan mode). Do NOT reconstruct
the plan from conversation history.

__PLAN_BODY__

Sidecar path (for update_plan persistence): __PLAN_PATH__

EXECUTION RULES:
1. Follow the plan phase by phase. For each wave of phases that share the same parallel_group
   label, dispatch ALL of them in a single delegate_tasks call — do not execute them one by one.
   Set the plan_step field on each delegated task to the phase name so progress is tracked
   correctly. Phases with no parallel_group label, or with a unique label, run on the main
   thread in dependency order. Mark each main-thread phase running with update_plan before you
   start it, and complete/failed when you finish.
2. DO NOT RE-INVESTIGATE. The plan's details and file paths were confirmed during planning —
   trust them. Do not restart repository discovery. Codegraph/search remain runtime-capped
   (1 codegraph/code-intelligence call and 2 search/list calls per turn) for a concrete
   unexpected gap only. Source reads are not capped.
3. ALLOWED READS: (a) a file you are about to edit/create (read it once immediately before
   writing to get the exact current content; do not re-read a path already in this turn's
   context), (b) files the plan's 'details' explicitly instruct you to read as part of the
   implementation.
4. IF A FILE PATH IN THE PLAN IS WRONG: use find_files once to locate the correct path, then
   proceed — do not re-read the surrounding area.
5. IF A COMPILATION ERROR requires understanding a type or import: use code_definition for that
   one symbol, then continue.
6. The approved plan is authoritative. Do NOT call announce_plan — it exists only in Plan mode and
   the runtime will reject it here. Use update_plan to record progress without replacing the plan.
7. DO NOT write a preamble or summary. Start executing the inlined plan immediately.`

// BuildPlanExecutionSystemContext returns PlanExecutionSystemContext with the
// plan path and inlined PLAN.md body filled in. If planPath is empty, falls
// back to the bare relative "PLAN.md". Missing or unreadable files still
// produce a usable context that names the path.
func BuildPlanExecutionSystemContext(planPath string) string {
	if planPath == "" {
		planPath = "PLAN.md"
	}
	body := ""
	if data, err := os.ReadFile(planPath); err == nil && len(data) > 0 {
		body = strings.TrimRight(string(data), "\n")
	}
	if body == "" {
		body = "(PLAN.md is empty or missing — follow any Progress/Phases already in context; do not re-announce a plan.)"
	}
	s := strings.ReplaceAll(PlanExecutionSystemContext, "__PLAN_PATH__", planPath)
	return strings.ReplaceAll(s, "__PLAN_BODY__", body)
}

// GraphPlanBlockedMessage is returned to the model when it calls a tool that is
// not allowed in the current Graph-Optimized Plan phase. Returning a result
// (not an error) lets the model self-correct and advance phases legitimately.
func GraphPlanBlockedMessage(toolName string, phase GraphPlanPhase) string {
	switch phase {
	case GraphPlanPhaseGraph:
		return fmt.Sprintf(
			"Blocked: `%s` is not available in the GRAPH phase of Graph-Optimized Plan mode. "+
				"Only `codegraph_explore` and `find_files` are available now — query the code graph first. "+
				"When you know exactly which regions to read, call `gplan_reads` to advance to the READ phase "+
				"(or `gplan_gaps` if codegraph has no coverage for this repo/language).",
			toolName,
		)
	case GraphPlanPhaseRead:
		return fmt.Sprintf(
			"Blocked: `%s` is not available in the READ phase. You may `read_file` (and read_pdf/filter_json) "+
				"and `codegraph_explore`. Read the regions codegraph identified. To unlock grep_search/find_files/"+
				"tree-sitter/delegate_tasks, call `gplan_gaps` with the genuine gaps codegraph could not answer; "+
				"if there are none, call `gplan_finalize` to record the plan.",
			toolName,
		)
	case GraphPlanPhaseGap:
		return fmt.Sprintf(
			"Blocked: `%s` cannot run in Graph-Optimized Plan mode — it is a mutating tool. This is a NO-CHANGES "+
				"mode. Finish gap-filling with the read-only tools, then call `gplan_finalize` to record the plan.",
			toolName,
		)
	case GraphPlanPhasePlan:
		return fmt.Sprintf(
			"Blocked: `%s` cannot run in Graph-Optimized Plan mode — it is a mutating tool and this is a "+
				"NO-CHANGES mode. Record the plan with `announce_plan`, then ask the user to exit to Normal mode "+
				"(shift+tab) before any execution.",
			toolName,
		)
	default:
		return fmt.Sprintf("Blocked: `%s` is not available in the current Graph-Optimized Plan phase.", toolName)
	}
}

// AskModeSystemContext is the per-turn instruction injected when ask mode is
// active (code mode only). The runtime gate blocks all mutating tools,
// delegate_tasks, and announce_plan. This prompt teaches the model to research
// and explain rather than plan or execute.
const AskModeSystemContext = `You are in Astonish ASK MODE. This is a hard constraint enforced by the runtime, not a suggestion.

RULES:
- You are in a RESEARCH-ONLY mode. Your job is to answer questions, explain architecture, discuss possible solutions, and help the user understand how things work.
- You MUST NOT make any changes. Mutating tools (write_file, edit_file, shell_command, and every other non-read-only tool), delegate_tasks, and announce_plan are DISABLED by the runtime and will be refused if you call them.
- You MUST NOT produce implementation plans or attempt to execute anything. This is not Plan mode — do not use announce_plan.
- You MAY use read-only tools (read_file, grep_search, find_files, file_tree, code_definition, code_references, repo_map, codegraph_explore, memory_search, web_fetch, etc.) to investigate the codebase and gather information.
- Focus on providing clear, accurate, well-researched answers. Cite specific files, functions, and line numbers when relevant.
- If the user asks you to make changes or create a plan, remind them they are in Ask mode and suggest switching to Normal or Plan mode (shift+tab).`

// approvedPlanExecutionToolBlocked reports whether a tool would replace the
// authoritative plan during its approved implementation turn.
func approvedPlanExecutionToolBlocked(name string) bool {
	return name == "announce_plan"
}

const (
	ApprovedExecutionMaxCodegraphCalls = 1
	ApprovedExecutionMaxSearchCalls    = 2
)

// approvedExecutionResearchKind classifies discovery calls that must remain
// bounded after a plan is approved. Empty means the tool is not rediscovery
// (source reads are allowed without a quota).
func approvedExecutionResearchKind(name string) string {
	switch name {
	case "codegraph_explore", "repo_map", "code_definition", "code_references":
		return "codegraph"
	case "grep_search", "find_files", "file_tree":
		return "search"
	default:
		return ""
	}
}

func approvedExecutionResearchLimit(kind string) int {
	switch kind {
	case "codegraph":
		return ApprovedExecutionMaxCodegraphCalls
	case "search":
		return ApprovedExecutionMaxSearchCalls
	default:
		return 0
	}
}

// ApprovedPlanExecutionResearchBlockedMessage explains the bounded exception
// policy: execution may inspect narrowly, but cannot restart planning research.
func ApprovedPlanExecutionResearchBlockedMessage(kind string, limit int) string {
	return fmt.Sprintf("Blocked: approved-plan execution reached its %s research limit (%d). Follow the persisted plan, edit the named files, and use update_plan; do not restart repository discovery.", kind, limit)
}

// ApprovedPlanExecutionBlockedMessage explains why an approved execution turn
// may update progress but cannot replace the plan the user approved.
func ApprovedPlanExecutionBlockedMessage() string {
	return "Blocked: `announce_plan` cannot run while an approved plan is executing. " +
		"Continue implementing the active plan and use `update_plan` to record phase progress; do not replace the approved plan."
}

// AnnouncePlanNotInPlanModeBlockedMessage is returned when the model calls
// announce_plan outside Plan / Graph-Optimized Plan mode.
func AnnouncePlanNotInPlanModeBlockedMessage() string {
	return "Blocked: `announce_plan` can only run in Plan mode (shift+tab). " +
		"You are in Normal mode. If an approved PLAN.md is inlined in this turn, follow it with `update_plan`. " +
		"To record a new plan, ask the user to switch to Plan mode."
}

// AskModeBlockedMessage is returned to the model when it calls a mutating tool
// while ask mode is active. Returning a result (rather than an error that
// aborts the turn) lets the model self-correct and continue answering.
func AskModeBlockedMessage(toolName string) string {
	return fmt.Sprintf(
		"Blocked: `%s` cannot run in Ask mode. You are in ASK MODE — a research-only mode for answering questions. "+
			"No changes, planning, or execution are allowed. Use read-only tools to investigate and provide your answer.",
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
	"perplexity_web_search":     true,
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
	// codegraph_explore is the read-only knowledge-graph query tool exposed by
	// the codegraph MCP server. It only reads the pre-computed graph and source,
	// so it auto-approves (no per-call prompt) and is the sole tool available in
	// the GRAPH phase of Graph-Optimized Plan mode.
	"codegraph_explore": true,
	// Graph-Optimized Plan phase-transition tools. They only mutate per-session
	// phase state (no FS/shell), so they are safe and always allowed in that
	// mode's gate.
	"gplan_reads":    true,
	"gplan_gaps":     true,
	"gplan_finalize": true,
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
