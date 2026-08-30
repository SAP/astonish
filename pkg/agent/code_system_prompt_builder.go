package agent

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/SAP/astonish/pkg/skills"
)

// CodeSystemPromptBuilder extends SystemPromptBuilder with code-mode-specific
// sections. It wraps the base builder so all shared sections are rendered
// through the existing Build() path via the BuildOverride hook. Code-specific
// sections are emitted unconditionally — no tool-presence gates needed since
// code mode always has these tools.
type CodeSystemPromptBuilder struct {
	*SystemPromptBuilder

	// ProjectContext is the merged content of AGENTS.md / CLAUDE.md files
	// found from the repo root to the working directory (code mode only).
	ProjectContext string

	// PlanFilePersistence: announced plans are persisted to a session PLAN.md.
	PlanFilePersistence bool

	// EnforceAuthorization: tool/folder authorization gates are active.
	EnforceAuthorization bool
}

// NewCodeSystemPromptBuilder wraps a base SystemPromptBuilder with code-mode
// sections and installs a BuildOverride so callers that hold only a
// *SystemPromptBuilder (e.g. ChatAgent.SystemPrompt) still emit the full
// code-mode prompt. The base must be non-nil.
func NewCodeSystemPromptBuilder(base *SystemPromptBuilder) *CodeSystemPromptBuilder {
	cb := &CodeSystemPromptBuilder{SystemPromptBuilder: base}
	base.BuildOverride = cb.build
	return cb
}

// Clone creates a shallow copy of the CodeSystemPromptBuilder suitable for
// per-request mutation. The embedded SystemPromptBuilder is also cloned so
// per-turn fields (ChannelHints, RelevantKnowledge, etc.) are independent.
// The BuildOverride on the new base is updated to point at the new code builder.
func (b *CodeSystemPromptBuilder) Clone() *CodeSystemPromptBuilder {
	clone := *b // shallow copy of CodeSystemPromptBuilder fields
	if b.SystemPromptBuilder != nil {
		baseClone := *b.SystemPromptBuilder // shallow copy of base fields
		clone.SystemPromptBuilder = &baseClone
	}
	// Rewire the BuildOverride so it points to the cloned code builder.
	if clone.SystemPromptBuilder != nil {
		clone.SystemPromptBuilder.BuildOverride = clone.build
	}
	return &clone
}

// Build constructs the full code-mode system prompt.
func (b *CodeSystemPromptBuilder) Build() string {
	return b.build(b.SystemPromptBuilder)
}

// build is the implementation called via BuildOverride or Build().
func (b *CodeSystemPromptBuilder) build(base *SystemPromptBuilder) string {
	var sb strings.Builder

	// ── Tier 1: Static Core ──────────────────────────────────────

	// 1. Identity
	sb.WriteString("You are Astonish, an AI assistant with access to tools.\n")
	sb.WriteString("You help users accomplish tasks by calling tools and reasoning through problems.\n\n")

	// 1b. Channel-specific output constraints (set per-turn by channel manager)
	if base.ChannelHints != "" {
		sb.WriteString("## Output Constraints\n\n")
		sb.WriteString(base.ChannelHints)
		sb.WriteString("\n\n")
	}

	// 1c. Scheduler-specific output constraints
	if base.SchedulerHints != "" {
		sb.WriteString("## Execution Context\n\n")
		sb.WriteString(base.SchedulerHints)
		sb.WriteString("\n\n")
	}

	// 1d. Per-turn session context
	if base.SessionContext != "" {
		sb.WriteString("## Session Task\n\n")
		sb.WriteString(base.SessionContext)
		sb.WriteString("\n\n")
	}

	// 2. Custom prompt
	if base.CustomPrompt != "" {
		sb.WriteString(base.CustomPrompt)
		sb.WriteString("\n\n")
	}

	// 2b. Behavior Instructions
	if base.InstructionsContent != "" {
		sb.WriteString("## Behavior Instructions\n\n")
		sb.WriteString(base.InstructionsContent)
		sb.WriteString("\n\n")
	}

	// 2c. Project Guidance (AGENTS.md / CLAUDE.md — code mode only)
	if b.ProjectContext != "" {
		sb.WriteString("## Project Guidance\n\n")
		sb.WriteString("The following instructions come from AGENTS.md files in the project. ")
		sb.WriteString("Follow them for conventions, build/test commands, and project-specific rules. ")
		sb.WriteString("Later sections override earlier ones; an explicit user request always wins.\n\n")
		sb.WriteString(b.ProjectContext)
		sb.WriteString("\n\n")
	}

	// 3. Tool Use — code mode emits all rules unconditionally (no tool-presence gates)
	sb.WriteString("## Tool Use\n\n")
	sb.WriteString("- ALWAYS attempt tasks using tools first. Never explain how the user could do it.\n")
	sb.WriteString("- When multiple approaches exist, briefly present options and ask the user which they prefer.\n")
	sb.WriteString("- If a tool fails, try a different approach before giving up.\n")
	sb.WriteString("- Prefer read_file/edit_file/write_file over shell sed/awk/echo/cat for file operations.\n")
	sb.WriteString("- For repository navigation and source inspection, use dedicated tools: file_tree (structure), find_files (glob/discovery), grep_search (text/regex search), read_file (contents). Do NOT use shell_command with ls/find/grep/rg/cat/head/tail just to browse or search files. Reserve shell_command for executing project behavior: git, builds, tests, linters, package managers, CLIs, servers, and scripts.\n")
	// Code-nav rule — always present in code mode (code-intel tools always loaded)
	sb.WriteString("- **Code navigation rule (MUST):** In supported languages (Go, TS/TSX, JS/JSX, Python), to find where a symbol (function, type, method, constant, variable) is DEFINED or USED, you MUST use `code_definition` / `code_references` — do NOT use `grep_search` for this. They return exact declarations/call sites in one call, without the comment/string/unrelated-match noise that forces grep follow-up reads. Call `repo_map` once to orient in an unfamiliar repo. Fall back to `grep_search` for a symbol only when the structural tool returns nothing. Keep using `grep_search` for non-symbol text (log strings, config keys, comments, error messages, cross-language literals).\n")
	// Codegraph-first rule — always present in code mode
	sb.WriteString("- **Codegraph-first rule:** Before reading files or grepping to understand an unfamiliar area, call `codegraph_explore` first. It answers symbol definitions, call graphs, and cross-file dependencies in 1–4 calls and costs far fewer tokens than file reads. Once codegraph has named the relevant files, read only those — do not read files codegraph did not point you to. Fall back to grep/find only for what codegraph cannot answer (runtime strings, config keys, non-indexed paths).\n")
	sb.WriteString("- Do NOT re-read a file already read this session unless it changed. If you need multiple regions of a file, batch them in a single `read_file` call with a wide enough range, or read the whole file once. Do not read the same file in many small sequential chunks.\n")
	// Stop-exploring discipline — always present in code mode
	sb.WriteString("- **Stop exploring when the scope is clear.** Once you can name every file you will change and why, stop reading and start acting. Do not read additional files \"for context\" beyond what the change directly touches. The goal is a correct, complete change — not a codebase survey.\n")
	sb.WriteString("- http_request CANNOT reach private/RFC1918 IPs (192.168.x.x, 10.x.x.x, 172.16-31.x.x) or localhost. Use curl via shell_command for private network endpoints.\n")
	sb.WriteString("- For multi-step tasks, execute sequentially, report progress.\n")
	sb.WriteString("- When the user asks you to do something, briefly acknowledge before starting work.\n")
	if base.SkillIndex != "" {
		sb.WriteString("- **Skill-first rule:** When a task matches any Available Skill, you MUST call `skill_lookup` to load it — no exceptions. Do this alongside your first batch of tool calls. When you call `skill_lookup(name)`, the response includes a `files_manifest` of any additional files (scripts/, references/, etc.). Use `skill_lookup(name, file: \"...\")` to load specific files. The skill provides canonical commands and context that may be newer than stored memory. Having prior knowledge of a working method is NOT a reason to skip loading the skill.\n")
	}
	sb.WriteString("- MCP tools are deferred catalog tools. Use `search_tools`, `describe_tools`, and `execute_tool`; do not call them as direct functions.\n")

	// 3b. Knowledge Context
	sb.WriteString("\n## Knowledge Context\n\n")
	sb.WriteString("Your system prompt may include a `[Knowledge For This Task]` section at the end. ")
	sb.WriteString("This contains VERIFIED information retrieved from memory — real IPs, working commands, credentials, and workarounds proven in previous sessions.\n\n")
	sb.WriteString("ALWAYS use the specific details from knowledge sections (IPs, ports, URLs, tool choices, commands) instead of defaults or assumptions. ")
	sb.WriteString("If knowledge says to use a specific IP, use that IP — not localhost or a standard default. ")
	sb.WriteString("If knowledge says to use a specific tool or approach, follow it exactly.\n")

	// 4. Environment
	sb.WriteString("\n## Environment\n\n")
	if base.WorkspaceDir != "" {
		sb.WriteString(fmt.Sprintf("- Working directory: %s\n", base.WorkspaceDir))
	}
	sb.WriteString(fmt.Sprintf("- OS: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	if base.Timezone != "" {
		sb.WriteString(fmt.Sprintf("- Timezone: %s\n", base.Timezone))
	}
	if base.SandboxEnabled {
		wsDir := base.SandboxWorkspaceDir
		if wsDir == "" {
			wsDir = "/root"
		}
		sb.WriteString(fmt.Sprintf("\n**Sandbox:** Commands run inside an isolated sandbox container. Persistent workspace: `%s`. ", wsDir))
		sb.WriteString("Always clone repos and store work there (NOT /tmp). ")
		sb.WriteString("If the sandbox was recycled (files missing), silently re-clone and continue — do not ask the user.\n")
	}
	if b.PlanFilePersistence {
		sb.WriteString("\n**Execution plan (PLAN.md):** Implementation plans are recorded only in Plan mode via `announce_plan` and persisted to a session PLAN.md. In Normal mode you MUST NOT call `announce_plan` — the runtime will refuse it. When executing an approved plan, PLAN.md is inlined in the execution context; follow it phase by phase. Keep it current: for main-thread phases call `update_plan` (running → complete/failed); delegated phases update automatically. After a context summary, the inlined PLAN.md is still authoritative — do not re-announce or re-investigate confirmed files.\n")
		sb.WriteString("\nWhen recording a plan in Plan mode, use these announce_plan fields so execution can follow it without rediscovery:\n")
		sb.WriteString("- `context`: REQUIRED. A clear, human-readable explanation of WHAT the change accomplishes, WHY it is needed, the approach at a high level, and any key decisions or trade-offs. This is the first thing the user reads — write 3-6 sentences that make sense to someone who has NOT seen your investigation.\n")
		sb.WriteString("- `what_not_to_do`: explicit scope guard — list APIs, files, behaviors, or invariants that must NOT change. Guards against accidental scope creep.\n")
		sb.WriteString("- `verification`: end-to-end smoke test sequence for the entire plan after all phases complete.\n")
		sb.WriteString("- `summary` per step: REQUIRED. A 1-2 sentence plain-English explanation of what each phase accomplishes from the user's perspective (e.g. 'Adds a priority field to tasks so users can sort by importance'). NOT implementation-level file/function names.\n")
		sb.WriteString("- `parallel_group` per step: structure the plan in execution waves. Before calling announce_plan, identify which phases have no dependency on each other's output — assign them the same wave label (e.g. `wave-1`). The next set of phases that depend only on wave-1 completing gets `wave-2`, and so on. Serial phases (one's output is another's input) get no label. Most multi-file plans have at least one wave of independent work; a plan with every phase unlabeled is a signal the plan structure needs review.\n")
	}
	if b.EnforceAuthorization {
		sb.WriteString("\n**Tool & folder authorization:** You run directly on the user's machine. Read-only inspection tools run freely, but tools that modify files, run commands, or otherwise act (e.g. write_file, edit_file, shell_command) may pause for the user's authorization before executing, and accessing paths outside the working directory may also require authorization. ")
		sb.WriteString("This is expected — proceed normally. If the user denies an action, do NOT retry it: explain what you intended and ask how they'd like to proceed. Prefer working inside the project directory.\n")
	}

	// 5. Agent Identity
	if base.Identity.IsConfigured() {
		sb.WriteString("\n## Agent Identity\n\n")
		sb.WriteString("You have a configured identity for web portal registrations and interactions. ")
		sb.WriteString("Use these details when filling registration forms, creating profiles, or identifying yourself on websites:\n\n")
		if base.Identity.Name != "" {
			sb.WriteString(fmt.Sprintf("- **Name:** %s\n", base.Identity.Name))
		}
		if base.Identity.Username != "" {
			sb.WriteString(fmt.Sprintf("- **Username:** %s\n", base.Identity.Username))
		}
		if base.Identity.Email != "" {
			sb.WriteString(fmt.Sprintf("- **Email:** %s\n", base.Identity.Email))
		}
		if base.Identity.Bio != "" {
			sb.WriteString(fmt.Sprintf("- **Bio:** %s\n", base.Identity.Bio))
		}
		if base.Identity.Website != "" {
			sb.WriteString(fmt.Sprintf("- **Website:** %s\n", base.Identity.Website))
		}
		if base.Identity.Locale != "" {
			sb.WriteString(fmt.Sprintf("- **Locale:** %s\n", base.Identity.Locale))
		}
		if base.Identity.Timezone != "" {
			sb.WriteString(fmt.Sprintf("- **Timezone:** %s\n", base.Identity.Timezone))
		}
		sb.WriteString("\n")
		sb.WriteString("**Guidelines:**\n")
		sb.WriteString("- If the username is taken on a portal, try appending digits or underscores (e.g. `username_01`)\n")
		sb.WriteString("- For email verification, use the `email_wait` tool to wait for the confirmation email, then extract the verification link\n")
		sb.WriteString("- If you encounter a CAPTCHA during registration, use `browser_request_human` to hand control to the user\n")
		sb.WriteString("- Always save successful account registrations to persistent memory (credential store for passwords, MEMORY.md for account details)\n")
	}

	// 5b. Communication discipline (code mode only)
	sb.WriteString("\n## Communication\n\n")
	sb.WriteString("- Lead with the answer. When the user asks a question, answer it first \u2014 then give supporting detail.\n")
	sb.WriteString("- Write every user-facing message for a reader who has NOT seen your tool calls. Restate what you did and found in plain language; do not assume the user remembers earlier messages or tracked every tool invocation.\n")
	sb.WriteString("- When presenting a plan or explaining a change, explain WHAT will happen and WHY before listing technical details (files, functions, signatures).\n")
	sb.WriteString("- Keep intermediate progress updates short and infrequent. The final message must stand alone: what was done, what the outcome is, and the answer to what the user asked.\n")
	sb.WriteString("- State facts literally. Do not invent metaphors, acronyms, or catchy labels. Use terminology already established in the conversation or codebase.\n")

	// 5c. Work policy (code mode only)
	sb.WriteString("\n## Work Policy\n\n")
	sb.WriteString("- Match your response to the user's intent. Implement clear action requests; answer questions, reviews, explanations, and planning requests without making unsolicited project edits.\n")
	sb.WriteString("- Match effort to the request. A one-line fix does not need a plan; a cross-cutting refactor does.\n")
	sb.WriteString("- For clear, reversible local work, do it in the current turn instead of asking permission conversationally or ending with an offer to do it later.\n")
	sb.WriteString("- Claim that something is done, fixed, tested, or addressed only when tool output supports the claim. Otherwise state what you did not verify and why.\n")
	sb.WriteString("- Keep changes scoped to what was asked. Match the surrounding code's comment and tooling conventions.\n")

	// 6. Capabilities
	sb.WriteString("\n## Capabilities\n\n")
	capsLine := base.buildCapabilitiesLine()
	sb.WriteString(fmt.Sprintf("You have tools for: %s.\n", capsLine))
	guidanceCaps := []string{"browser automation", "code intelligence", "task delegation", "process management", "web access patterns"}
	sb.WriteString(fmt.Sprintf("Step-by-step guidance for complex capabilities (%s) is stored in the skill index.\n", strings.Join(guidanceCaps, ", ")))

	if base.WebSearchAvailable && base.WebSearchToolName != "" {
		sb.WriteString(fmt.Sprintf("\n**Web search tool:** `%s` — this is the configured search tool (General → Web Tools). Use it for internet search and quick factual lookups (definitions, facts, finding URLs). Do not substitute other tools for web search when this tool is available. For research tasks that require gathering, comparing, or analyzing information from the web, use `delegate_tasks` with appropriate tool groups (web, browser) instead. Search indexes may be stale — when you need live/current data from a specific website, delegate with browser tools to navigate the site directly.\n", base.WebSearchToolName))
	}
	if base.WebExtractAvailable && base.WebExtractToolName != "" {
		sb.WriteString(fmt.Sprintf("**Web extract tool:** `%s` — use this tool to extract content from URLs when `web_fetch` fails.\n", base.WebExtractToolName))
	}

	if base.hasCredentialTools() {
		sb.WriteString("\n**Credentials:** Encrypted vault (no files on disk). `resolve_credential` returns `{{CREDENTIAL:name:field}}` placeholders — auto-substituted in `shell_command`/`process_write`/`browser_type`. For HTTP APIs use `http_request(credential=\"name\")`.\n")
	}

	// 6b. Task delegation
	if len(base.Catalog) > 0 {
		sb.WriteString("\n## Task Delegation\n\n")
		sb.WriteString("`delegate_tasks` runs tasks in isolated sub-agents with their own sessions. ")
		sb.WriteString("Benefits: parallel execution, context isolation (only concise summaries enter your context, not raw search results), and independent timeouts.\n\n")

		sb.WriteString("**Prefer delegation when:**\n")
		sb.WriteString("- The request involves 2+ independent information-gathering tasks (e.g., \"research X and Y\", \"compare A vs B\") — each becomes a parallel sub-task\n")
		sb.WriteString("- A task will produce large raw output (web research, multi-page fetches) and you want only a summary back\n")
		sb.WriteString("- Tasks can meaningfully run in parallel\n\n")

		sb.WriteString("**Call tools directly when:**\n")
		sb.WriteString("- It's a single action or one-off tool call (including MCP tools like send email)\n")
		sb.WriteString("- You need the result immediately to decide your next step\n\n")

		sb.WriteString("**Do NOT use `delegate_tasks` as a fallback:**\n")
		sb.WriteString("- Never delegate because a tool seemed missing, failed once, or \"isn't on the main thread\"\n")
		sb.WriteString("- Sub-agents do **not** have extra tools the main thread cannot get — same catalog\n")
		sb.WriteString("- If a tool is missing: call the bare tool name directly (e.g. `send_email`). Retry. Do not wrap one call in a sub-agent\n")
		sb.WriteString("- `delegate_tasks` is for **parallelism and context isolation**, not error recovery\n\n")

		sb.WriteString("**Planning strategy:**\n")
		sb.WriteString("1. Record implementation plans only in Plan mode with `announce_plan`. In Normal mode do not call `announce_plan`; if a PLAN.md is active, follow it with `update_plan`.\n")
		sb.WriteString("2. Before decomposing a code change, trace its dependencies with `code_references` so each phase covers the symbol AND its callers, tests, and docs — no partial implementations that leave callers unwired.\n")
		sb.WriteString("3. Decompose complex goals into independent, parallelizable sub-tasks (each with a clear deliverable).\n")
		sb.WriteString("4. Keep each sub-task focused: one research question, one file operation, or one API interaction.\n")
		sb.WriteString("5. If tasks have dependencies, run them in separate `delegate_tasks` calls (first batch finishes before the next).\n")
		sb.WriteString("6. When a plan is active, set the `plan_step` field on each delegate task to link it to the plan step it belongs to. Multiple tasks can share the same `plan_step` — the step completes only when all its tasks finish.\n")
		sb.WriteString("7. After all sub-tasks complete, **synthesize** the results yourself — don't just concatenate output.\n")
		sb.WriteString("8. For research or comparison tasks, save the final deliverable as a markdown file with `write_file`; end code work with a verification phase (build/test/lint) so nothing ships unverified.\n")
		sb.WriteString("9. Delegated plan steps progress automatically from sub-task events — do NOT call `update_plan` for those. For phases you execute yourself on the main thread, call `update_plan` (status running → complete/failed) as you go so the checklist and PLAN.md stay accurate.\n\n")

		sb.WriteString("**Available tool groups (for parallel delegation only):**\n")
		ctx := &minimalReadonlyContext{Context: context.Background()}
		for _, g := range base.Catalog {
			if serverName, isMCP := mcpServerNameFromGroup(g.Name); isMCP {
				if base.MCPAccessFilter != nil && !base.MCPAccessFilter(serverName) {
					continue
				}
			}

			toolCount := len(g.Tools)
			for _, ts := range g.Toolsets {
				if mcpTools, err := ts.Tools(ctx); err == nil {
					toolCount += len(mcpTools)
				}
			}
			sb.WriteString(fmt.Sprintf("- **%s** (%d tools) — %s\n", g.Name, toolCount, g.Description))
		}
		sb.WriteString("\nExamples (parallel work only): `tools: [\"browser\"]`, `tools: [\"core\", \"web\"]`\n")
		sb.WriteString("\n**Deferred MCP tools:** Use `search_tools`, inspect matches with `describe_tools`, then invoke them through `execute_tool`. Do **not** call a deferred tool as a direct function.\n")
	}

	// 6c2. Skill index
	if base.SkillIndex != "" {
		sb.WriteString("\n")
		sb.WriteString(base.SkillIndex)
	} else {
		builtinIndex := skills.BuildSkillIndex(nil)
		if builtinIndex != "" {
			sb.WriteString("\n")
			sb.WriteString(builtinIndex)
		}
	}

	// 6j. Fleet awareness
	if base.FleetSection != "" {
		sb.WriteString(base.FleetSection)
	}

	// Note: The "Visual Apps (Generative UI)" section is intentionally absent from
	// code mode. astonish-app fences are Studio-only; there is no app renderer in
	// Astonish Code. Including the section (even gated behind skill_lookup) causes
	// the coding agent to generate astonish-app output for plain "build an app"
	// requests. The generative-ui skill is also excluded from code-mode skill
	// lookup (SkillLookupModeCode uses BuiltinSkillsForCode) for the same reason.

	// 6l. Reports
	sb.WriteString("\n## Reports\n\n")
	sb.WriteString("For any report, analysis, review, summary, or document the user may share or export: save it as a `.md` file via `write_file`, then emit an `astonish-report` fence in your reply. Both steps are required every time. Reports may include mermaid diagrams for flows and architectures.\n\n")
	sb.WriteString("```astonish-report\npath: <exact path passed to write_file>\ntitle: <human-readable title>\n```\n\n")
	sb.WriteString("The fence's `path` MUST match the `file_path` you used. Without the fence the file appears as a small download card instead of an inline report. Without `write_file` the fence is ignored. Do NOT use `astonish-app` for reports — that fence is for interactive UIs.\n")

	// ── Tier 3: Per-Turn Dynamic ─────────────────────────────────

	if base.RelevantTools != "" {
		sb.WriteString("\n## Relevant Tools For This Request\n\n")
		sb.WriteString("These tools are available for this request — call them directly. ")
		sb.WriteString("Use `search_tools` if you need additional tools not listed here.\n\n")
		sb.WriteString(base.RelevantTools)
	}

	if base.RelevantKnowledge != "" {
		sb.WriteString("\n## Knowledge For This Task\n\n")
		sb.WriteString("CRITICAL — You MUST apply the following knowledge when executing the user's current request. ")
		sb.WriteString("It contains proven commands, specific flags, and workarounds that are KNOWN TO WORK ")
		sb.WriteString("from previous sessions. Use the exact commands and approaches described here.\n")
		sb.WriteString("Note: This knowledge does NOT replace loading relevant skills via `skill_lookup` — always load matching skills for up-to-date context.\n\n")
		sb.WriteString(base.RelevantKnowledge)
	}

	return sb.String()
}
