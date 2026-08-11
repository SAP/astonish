package agent

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/SAP/astonish/pkg/skills"
	"google.golang.org/adk/tool"
)

// AgentIdentity holds the agent's configured persona for web portal interactions.
// When populated, it is rendered into the system prompt so the LLM knows what
// name, username, and email to use when filling registration forms.
type AgentIdentity struct {
	Name     string
	Username string
	Email    string
	Bio      string
	Website  string
	Locale   string
	Timezone string
}

// IsConfigured returns true if at least one identity field is set.
func (id *AgentIdentity) IsConfigured() bool {
	return id != nil && (id.Name != "" || id.Username != "" || id.Email != "")
}

// SystemPromptBuilder constructs context-aware system prompts for chat mode.
//
// The prompt is organized into three tiers:
//   - Tier 1 (Static Core): Identity, behavior, permissions, environment, capabilities.
//     ~800 tokens. Stable across turns for KV-cache reuse.
//   - Tier 2 (Indexed Guidance): Detailed how-to docs for each capability, stored as
//     memory/guidance/*.md and retrieved via the vector store. Zero tokens in the prompt.
//   - Tier 3 (Per-Turn Dynamic): Channel hints, scheduler hints, session context,
//     execution plans, and auto-retrieved knowledge. These are appended at the end
//     of the system prompt so the static prefix remains cacheable by providers.
//
// For code mode (astonish code), use CodeSystemPromptBuilder which embeds this
// struct and installs a BuildOverride to emit code-specific sections.
type SystemPromptBuilder struct {
	Tools                 []tool.Tool
	Toolsets              []tool.Toolset
	WorkspaceDir          string
	CustomPrompt          string
	InstructionsContent   string                       // Contents of INSTRUCTIONS.md (behavior directives)
	WebSearchAvailable    bool                         // Whether a web search MCP tool is configured
	WebExtractAvailable   bool                         // Whether a web extract MCP tool is configured
	WebSearchToolName     string                       // Name of the configured search tool (e.g. "tavily-search")
	WebExtractToolName    string                       // Name of the configured extract tool (e.g. "tavily-extract")
	BrowserAvailable      bool                         // Whether built-in browser tools are registered
	MemorySearchAvailable bool                         // Whether semantic memory search is available
	ChannelHints          string                       // Channel-specific output constraints (empty = console mode)
	SchedulerHints        string                       // Scheduler-specific output constraints (empty = not a scheduled run)
	SkillIndex            string                       // Lightweight skill listing (Tier 1 — names and descriptions only)
	Identity              *AgentIdentity               // Agent persona for web portal interactions
	FleetSection          string                       // Pre-built "Available Fleets" section (empty = no fleets loaded)
	SessionContext        string                       // Per-turn context injected by the caller (e.g., fleet plan wizard instructions)
	Timezone              string                       // IANA timezone (e.g. "America/New_York")
	RelevantKnowledge     string                       // Per-turn auto-retrieved knowledge from vector store (empty = none)
	RelevantTools         string                       // Per-turn auto-retrieved tool matches from tool index (empty = none)
	Catalog               []*ToolGroup                 // Tool groups available for delegation via delegate_tasks (nil = no delegation)
	MCPAccessFilter       func(serverName string) bool // Per-turn filter for MCP groups in catalog (nil = allow all)
	SandboxEnabled        bool                         // Whether a sandbox backend is configured
	SandboxWorkspaceDir   string                       // Persistent workspace dir inside sandbox (e.g. "/sandbox" or "/root")

	// BuildOverride, when non-nil, is called by Build() instead of the default
	// builder logic. Installed by CodeSystemPromptBuilder to emit code-mode
	// sections while keeping all per-turn field mutations on this base struct.
	BuildOverride func(b *SystemPromptBuilder) string
}

// Clone creates a shallow copy of the SystemPromptBuilder suitable for
// per-request mutation. Static fields (Tools, Toolsets, Catalog, etc.) are
// shared read-only with the original — only per-turn fields should be
// modified on the clone.
func (b *SystemPromptBuilder) Clone() *SystemPromptBuilder {
	if b == nil {
		return nil
	}
	clone := *b // shallow copy — all fields are value types, strings, slices (shared read-only), or func
	return &clone
}

// PromptOverrides carries per-turn system prompt field overrides that are
// injected into the context by callers (scheduler, channel manager, API runner)
// and applied to the cloned SystemPromptBuilder inside Run().
// This eliminates shared mutable state across concurrent requests.
type PromptOverrides struct {
	ChannelHints   string // Channel-specific output constraints
	SchedulerHints string // Scheduler-specific output constraints
	SessionContext string // Per-turn session context (fleet wizard, etc.)
	SkillIndex     string // Per-request merged skill index (platform + org + team)

	// PinnedToolGroups lists tool group names that should always be injected
	// into the LLM request regardless of ToolIndex scoring. Used by wizard
	// sessions to ensure critical tools (e.g., save_sandbox_template, save_fleet_plan)
	// remain available across all turns of a multi-turn guided conversation.
	PinnedToolGroups []string

	// PlanMode, when true, enables a hard runtime gate for the turn: mutating
	// tools (anything not in SafeTools, plus delegate_tasks) are refused at
	// execution time and the model is reminded it is in plan mode. This is a
	// stronger guarantee than SessionContext prose alone, which the model can
	// ignore. Callers that set PlanMode should also inject the plan-mode
	// SessionContext so the model produces a plan rather than attempting work.
	PlanMode bool

	// GraphPlanMode, when true, enables the phased Graph-Optimized Plan runtime
	// gate for the turn (code mode only): a per-session phase state machine
	// (GraphPlanState) determines the tool allow-list, advanced by the gplan_*
	// transition tools. Mutually exclusive with PlanMode. Callers that set this
	// should also inject GraphPlanModeSystemContext as SessionContext.
	GraphPlanMode bool

	// AskMode, when true, enables the ask-mode gate: only read-only tools are
	// allowed; the model is instructed to research and explain rather than plan
	// or execute. Mutually exclusive with PlanMode and GraphPlanMode.
	AskMode bool

	// Web search/extract are resolved per request from the platform→team cascade
	// so a singleton ChatAgent re-inited without tenant context still advertises
	// the platform-selected tools for every user. Nil pointers mean "leave builder as-is".
	WebSearchAvailable  *bool
	WebSearchToolName   string
	WebExtractAvailable *bool
	WebExtractToolName  string
}

type promptOverridesKey struct{}

// WithPromptOverrides attaches PromptOverrides to a context.
func WithPromptOverrides(ctx context.Context, po *PromptOverrides) context.Context {
	return context.WithValue(ctx, promptOverridesKey{}, po)
}

// PromptOverridesFromContext retrieves PromptOverrides from the context, if set.
func PromptOverridesFromContext(ctx context.Context) *PromptOverrides {
	po, _ := ctx.Value(promptOverridesKey{}).(*PromptOverrides)
	return po
}

// Build constructs the full system prompt.
//
// The output is deliberately compact (~800 tokens static) to maximize
// attention budget for smaller models. Detailed guidance for complex
// capabilities (browser, credentials, scheduling, etc.) is stored in
// memory/guidance/*.md and delivered via auto-knowledge retrieval.
//
// When BuildOverride is set (installed by CodeSystemPromptBuilder), it is
// called instead and this method returns its result directly.
func (b *SystemPromptBuilder) Build() string {
	if b.BuildOverride != nil {
		return b.BuildOverride(b)
	}
	var sb strings.Builder

	// ── Tier 1: Static Core ──────────────────────────────────────

	// 1. Identity
	sb.WriteString("You are Astonish, an AI assistant with access to tools.\n")
	sb.WriteString("You help users accomplish tasks by calling tools and reasoning through problems.\n\n")

	// 1b. Channel-specific output constraints (set per-turn by channel manager)
	if b.ChannelHints != "" {
		sb.WriteString("## Output Constraints\n\n")
		sb.WriteString(b.ChannelHints)
		sb.WriteString("\n\n")
	}

	// 1c. Scheduler-specific output constraints (set per-turn by scheduler executor)
	if b.SchedulerHints != "" {
		sb.WriteString("## Execution Context\n\n")
		sb.WriteString(b.SchedulerHints)
		sb.WriteString("\n\n")
	}

	// 1d. Per-turn session context (e.g., fleet plan wizard instructions)
	if b.SessionContext != "" {
		sb.WriteString("## Session Task\n\n")
		sb.WriteString(b.SessionContext)
		sb.WriteString("\n\n")
	}

	// 2. Custom prompt (if set by user in config.yaml)
	if b.CustomPrompt != "" {
		sb.WriteString(b.CustomPrompt)
		sb.WriteString("\n\n")
	}

	// 2b. Behavior Instructions (from INSTRUCTIONS.md — user-editable)
	if b.InstructionsContent != "" {
		sb.WriteString("## Behavior Instructions\n\n")
		sb.WriteString(b.InstructionsContent)
		sb.WriteString("\n\n")
	}

	// 3. Tool Use (compact — detailed guidance is in memory/guidance/*.md)
	sb.WriteString("## Tool Use\n\n")
	sb.WriteString("- ALWAYS attempt tasks using tools first. Never explain how the user could do it.\n")
	sb.WriteString("- When multiple approaches exist, briefly present options and ask the user which they prefer.\n")
	sb.WriteString("- If a tool fails, try a different approach before giving up.\n")
	sb.WriteString("- Prefer read_file/edit_file/write_file over shell sed/awk/echo/cat for file operations.\n")
	sb.WriteString("- For repository navigation and source inspection, use dedicated tools: file_tree (structure), find_files (glob/discovery), grep_search (text/regex search), read_file (contents). Do NOT use shell_command with ls/find/grep/rg/cat/head/tail just to browse or search files. Reserve shell_command for executing project behavior: git, builds, tests, linters, package managers, CLIs, servers, and scripts.\n")
	sb.WriteString("- Do NOT re-read a file already read this session unless it changed. If you need multiple regions of a file, batch them in a single `read_file` call with a wide enough range, or read the whole file once. Do not read the same file in many small sequential chunks.\n")
	sb.WriteString("- http_request CANNOT reach private/RFC1918 IPs (192.168.x.x, 10.x.x.x, 172.16-31.x.x) or localhost. Use curl via shell_command for private network endpoints.\n")
	sb.WriteString("- For multi-step tasks, execute sequentially, report progress, and search memory first for prior solutions.\n")
	sb.WriteString("- After completing a task where you overcame obstacles or discovered non-obvious solutions, save the knowledge using memory_save. Search memory_search(\"memory usage\") first to retrieve the full saving guidelines.\n")
	sb.WriteString("- When the user asks you to do something, briefly acknowledge before starting work.\n")
	if b.SkillIndex != "" {
		sb.WriteString("- **Skill-first rule:** When a task matches any Available Skill, you MUST call `skill_lookup` to load it — no exceptions. Do this alongside your first batch of tool calls (e.g. parallel with memory_search). When you call `skill_lookup(name)`, the response includes a `files_manifest` of any additional files (scripts/, references/, etc.). Use `skill_lookup(name, file: \"...\")` to load specific files. The skill provides canonical commands and context that may be newer than stored memory. Having prior knowledge of a working method is NOT a reason to skip loading the skill.\n")
	}
	if b.hasSearchToolsTool() {
		sb.WriteString("- When you're unsure which tool or tool group to use for a task, call `search_tools` with a description of what you need. Do NOT guess tool availability — verify via `search_tools`.\n")
		sb.WriteString("- When asked to list available tools, call `search_tools(query=\"*\")` to get the verified complete inventory. Do not reconstruct the list from memory.\n")
	}

	// 3b. Knowledge Context — teaches the model about injected knowledge
	sb.WriteString("\n## Knowledge Context\n\n")
	sb.WriteString("Your system prompt may include a `[Knowledge For This Task]` section at the end. ")
	sb.WriteString("This contains VERIFIED information retrieved from memory — real IPs, working commands, credentials, and workarounds proven in previous sessions.\n\n")
	sb.WriteString("ALWAYS use the specific details from knowledge sections (IPs, ports, URLs, tool choices, commands) instead of defaults or assumptions. ")
	sb.WriteString("If knowledge says to use a specific IP, use that IP — not localhost or a standard default. ")
	sb.WriteString("If knowledge says to use a specific tool or approach, follow it exactly.\n")
	sb.WriteString("The knowledge section already contains the most relevant memory results for this task — do not call memory_search to re-fetch information already present in it.\n")

	// 4. Environment
	sb.WriteString("\n## Environment\n\n")
	if b.WorkspaceDir != "" {
		sb.WriteString(fmt.Sprintf("- Working directory: %s\n", b.WorkspaceDir))
	}
	sb.WriteString(fmt.Sprintf("- OS: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	if b.Timezone != "" {
		sb.WriteString(fmt.Sprintf("- Timezone: %s\n", b.Timezone))
	}
	if b.SandboxEnabled {
		wsDir := b.SandboxWorkspaceDir
		if wsDir == "" {
			wsDir = "/root"
		}
		sb.WriteString(fmt.Sprintf("\n**Sandbox:** Commands run inside an isolated sandbox container. Persistent workspace: `%s`. ", wsDir))
		sb.WriteString("Always clone repos and store work there (NOT /tmp). ")
		sb.WriteString("If the sandbox was recycled (files missing), silently re-clone and continue — do not ask the user.\n")
	}

	// 5. Agent Identity (for web portal interactions)
	if b.Identity.IsConfigured() {
		sb.WriteString("\n## Agent Identity\n\n")
		sb.WriteString("You have a configured identity for web portal registrations and interactions. ")
		sb.WriteString("Use these details when filling registration forms, creating profiles, or identifying yourself on websites:\n\n")
		if b.Identity.Name != "" {
			sb.WriteString(fmt.Sprintf("- **Name:** %s\n", b.Identity.Name))
		}
		if b.Identity.Username != "" {
			sb.WriteString(fmt.Sprintf("- **Username:** %s\n", b.Identity.Username))
		}
		if b.Identity.Email != "" {
			sb.WriteString(fmt.Sprintf("- **Email:** %s\n", b.Identity.Email))
		}
		if b.Identity.Bio != "" {
			sb.WriteString(fmt.Sprintf("- **Bio:** %s\n", b.Identity.Bio))
		}
		if b.Identity.Website != "" {
			sb.WriteString(fmt.Sprintf("- **Website:** %s\n", b.Identity.Website))
		}
		if b.Identity.Locale != "" {
			sb.WriteString(fmt.Sprintf("- **Locale:** %s\n", b.Identity.Locale))
		}
		if b.Identity.Timezone != "" {
			sb.WriteString(fmt.Sprintf("- **Timezone:** %s\n", b.Identity.Timezone))
		}
		sb.WriteString("\n")
		sb.WriteString("**Guidelines:**\n")
		sb.WriteString("- If the username is taken on a portal, try appending digits or underscores (e.g. `username_01`)\n")
		sb.WriteString("- For email verification, use the `email_wait` tool to wait for the confirmation email, then extract the verification link\n")
		sb.WriteString("- If you encounter a CAPTCHA during registration, use `browser_request_human` to hand control to the user\n")
		sb.WriteString("- Always save successful account registrations to persistent memory (credential store for passwords, MEMORY.md for account details)\n")
	}

	// 6. Capabilities (dynamic list + guidance hint)
	sb.WriteString("\n## Capabilities\n\n")
	capsLine := b.buildCapabilitiesLine()
	sb.WriteString(fmt.Sprintf("You have tools for: %s.\n", capsLine))
	guidanceCaps := []string{"browser automation", "credential management", "job scheduling", "task delegation", "process management", "web access patterns", "memory usage"}
	sb.WriteString(fmt.Sprintf("Step-by-step guidance for complex capabilities (%s) is stored in memory. ", strings.Join(guidanceCaps, ", ")))
	sb.WriteString("Use `memory_search` with the capability name (e.g., \"browser automation\", \"credential management\", ")
	sb.WriteString("\"job scheduling\") to retrieve instructions before using a complex feature for the first time in a conversation.\n")

	// 6a2. Web search/extract tool hints — tell the LLM the exact tool names
	// so it doesn't fall back to web_fetch when a dedicated search tool is configured.
	if b.WebSearchAvailable && b.WebSearchToolName != "" {
		sb.WriteString(fmt.Sprintf("\n**Web search tool:** `%s` — this is the configured search tool (General → Web Tools). Use it for internet search and quick factual lookups (definitions, facts, finding URLs). Do not substitute other tools for web search when this tool is available. For research tasks that require gathering, comparing, or analyzing information from the web, use `delegate_tasks` with appropriate tool groups (web, browser) instead. Search indexes may be stale — when you need live/current data from a specific website, delegate with browser tools to navigate the site directly.\n", b.WebSearchToolName))
	}
	if b.WebExtractAvailable && b.WebExtractToolName != "" {
		sb.WriteString(fmt.Sprintf("**Web extract tool:** `%s` — use this tool to extract content from URLs when `web_fetch` fails.\n", b.WebExtractToolName))
	}

	// 6a3. Credential system — always present when credential tools are configured.
	if b.hasCredentialTools() {
		sb.WriteString("\n**Credentials:** Encrypted vault (no files on disk). `resolve_credential` returns `{{CREDENTIAL:name:field}}` placeholders — auto-substituted in `shell_command`/`process_write`/`browser_type`. For HTTP APIs use `http_request(credential=\"name\")`.\n")
	}

	// 6b. Task delegation — list available tool groups for delegate_tasks
	if len(b.Catalog) > 0 {
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
		sb.WriteString("- If a tool is missing: `search_tools`, then call the bare tool name (e.g. `send_email`). Retry. Do not wrap one call in a sub-agent\n")
		sb.WriteString("- `delegate_tasks` is for **parallelism and context isolation**, not error recovery\n\n")

		sb.WriteString("**Planning strategy:**\n")
		sb.WriteString("1. For multi-step tasks, call `announce_plan` first to show the user your approach as a visible checklist.\n")
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
		for _, g := range b.Catalog {
			// MCP tool access control: skip groups for inaccessible MCP servers
			if serverName, isMCP := mcpServerNameFromGroup(g.Name); isMCP {
				if b.MCPAccessFilter != nil && !b.MCPAccessFilter(serverName) {
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
		sb.WriteString("\n**MCP tools (main thread):** After `search_tools`, call the **bare** tool name (e.g. `send_email`). ")
		sb.WriteString("Do **not** invent `mcp:email/send_email` (app-only form). Do **not** delegate a single MCP call.\n")
	}

	// 6c2. Skill index (lightweight listing of available CLI tool skills)
	// Always include built-in skills even if no user/platform skills are configured.
	if b.SkillIndex != "" {
		sb.WriteString("\n")
		sb.WriteString(b.SkillIndex)
	} else {
		// No injected skill index — generate one with just built-in skills
		builtinIndex := skills.BuildSkillIndex(nil)
		if builtinIndex != "" {
			sb.WriteString("\n")
			sb.WriteString(builtinIndex)
		}
	}

	// 6j. Fleet awareness (when fleet definitions are loaded)
	if b.FleetSection != "" {
		sb.WriteString(b.FleetSection)
	}

	// 6k. Generative UI critical rules (always present in prompt)
	sb.WriteString("\n## Visual Apps (Generative UI)\n\n")
	sb.WriteString("When users ask to build a UI, dashboard, app, or visual component, generate a React component inside an `astonish-app` code fence.\n")
	sb.WriteString("**MANDATORY:** Before generating ANY visual app, call `skill_lookup` with name `generative-ui` to load the complete documentation. Do NOT generate app code without loading this skill first — it contains the only correct APIs, design system, and sandbox constraints.\n")
	sb.WriteString("Sandbox hard constraints (violations cause runtime errors):\n")
	sb.WriteString("- fetch(), XMLHttpRequest, and axios are BLOCKED — the skill documents the correct alternatives\n")
	sb.WriteString("- Only react, recharts, lucide-react, and astonish modules exist — no other imports work\n")
	sb.WriteString("- No component libraries (no shadcn/ui, no Material UI) — use native HTML + Tailwind\n")

	// 6l. Reports contract (always present — must be unconditional, not
	// hidden behind vector retrieval, because the LLM cannot retrieve
	// guidance it doesn't know exists).
	sb.WriteString("\n## Reports\n\n")
	sb.WriteString("For any report, analysis, review, summary, or document the user may share or export: save it as a `.md` file via `write_file`, then emit an `astonish-report` fence in your reply. Both steps are required every time. Reports may include mermaid diagrams for flows and architectures.\n\n")
	sb.WriteString("```astonish-report\npath: <exact path passed to write_file>\ntitle: <human-readable title>\n```\n\n")
	sb.WriteString("The fence's `path` MUST match the `file_path` you used. Without the fence the file appears as a small download card instead of an inline report. Without `write_file` the fence is ignored. Do NOT use `astonish-app` for reports — that fence is for interactive UIs.\n")

	// ── Tier 3: Per-Turn Dynamic ─────────────────────────────────
	// Execution plans and auto-retrieved knowledge are appended here at
	// the end of the system prompt. Placing them last means the static
	// prefix (Tier 1 + Tier 2) remains stable for provider KV-cache
	// prefix matching, while the dynamic tail changes per turn.

	// Per-turn relevant tools from the tool index — these tools are
	// dynamically injected and available for direct invocation.
	if b.RelevantTools != "" {
		sb.WriteString("\n## Relevant Tools For This Request\n\n")
		sb.WriteString("These tools are available for this request — call them directly. ")
		sb.WriteString("Use `search_tools` if you need additional tools not listed here.\n\n")
		sb.WriteString(b.RelevantTools)
	}

	if b.RelevantKnowledge != "" {
		sb.WriteString("\n## Knowledge For This Task\n\n")
		sb.WriteString("CRITICAL — You MUST apply the following knowledge when executing the user's current request. ")
		sb.WriteString("It contains proven commands, specific flags, and workarounds that are KNOWN TO WORK ")
		sb.WriteString("from previous sessions. Use the exact commands and approaches described here.\n")
		sb.WriteString("Note: This knowledge does NOT replace loading relevant skills via `skill_lookup` — always load matching skills for up-to-date context.\n\n")
		sb.WriteString(b.RelevantKnowledge)
	}

	// NOTE: Date/time is NOT included here. It is prepended to each user
	// message via NewTimestampedUserContent(), keeping the system prompt
	// prefix as stable as possible for provider KV-cache reuse.

	return sb.String()
}

// buildCapabilitiesLine produces a comma-separated list of available capability
// names based on which tools are registered.
func (b *SystemPromptBuilder) buildCapabilitiesLine() string {
	var caps []string

	// Always present (core tools)
	caps = append(caps, "file operations", "shell commands", "web fetching")

	if b.BrowserAvailable {
		caps = append(caps, "browser automation")
	}
	if b.hasCredentialTools() {
		caps = append(caps, "credential management")
	}
	if b.hasSchedulerTools() {
		caps = append(caps, "job scheduling")
	}
	if b.hasProcessTools() {
		caps = append(caps, "process management")
	}
	if b.hasHttpRequestTool() {
		caps = append(caps, "HTTP API requests")
	}
	if b.hasDelegateTasksTool() {
		caps = append(caps, "task delegation")
	}
	if b.hasFlowTools() {
		caps = append(caps, "flow execution")
	}
	if b.hasCodeIntelTools() {
		caps = append(caps, "code intelligence")
	}
	if b.MemorySearchAvailable {
		caps = append(caps, "persistent memory")
	}
	if b.WebSearchAvailable {
		if b.WebSearchToolName != "" {
			caps = append(caps, fmt.Sprintf("web search via `%s`", b.WebSearchToolName))
		} else {
			caps = append(caps, "web search")
		}
	}
	if b.WebExtractAvailable {
		if b.WebExtractToolName != "" {
			caps = append(caps, fmt.Sprintf("web content extraction via `%s`", b.WebExtractToolName))
		} else {
			caps = append(caps, "web content extraction")
		}
	}
	if b.hasEmailTools() {
		caps = append(caps, "email")
	}
	if b.FleetSection != "" {
		caps = append(caps, "fleet agents")
	}

	return strings.Join(caps, ", ")
}

// ToolCount returns the total number of tools available.
func (b *SystemPromptBuilder) ToolCount() int {
	count := len(b.Tools)
	if len(b.Toolsets) > 0 {
		ctx := &minimalReadonlyContext{Context: context.Background()}
		for _, ts := range b.Toolsets {
			mcpTools, err := ts.Tools(ctx)
			if err != nil {
				continue
			}
			count += len(mcpTools)
		}
	}
	return count
}

// hasSchedulerTools returns true if schedule_job is among the available tools.
func (b *SystemPromptBuilder) hasSchedulerTools() bool {
	for _, t := range b.Tools {
		if t.Name() == "schedule_job" {
			return true
		}
	}
	return false
}

// hasCredentialTools returns true if save_credential is among the available tools.
func (b *SystemPromptBuilder) hasCredentialTools() bool {
	for _, t := range b.Tools {
		if t.Name() == "save_credential" {
			return true
		}
	}
	return false
}

// hasProcessTools returns true if process_read is among the available tools.
func (b *SystemPromptBuilder) hasProcessTools() bool {
	for _, t := range b.Tools {
		if t.Name() == "process_read" {
			return true
		}
	}
	return false
}

// hasHttpRequestTool returns true if http_request is among the available tools.
func (b *SystemPromptBuilder) hasHttpRequestTool() bool {
	for _, t := range b.Tools {
		if t.Name() == "http_request" {
			return true
		}
	}
	return false
}

// hasDelegateTasksTool returns true if delegate_tasks is among the available tools.
func (b *SystemPromptBuilder) hasDelegateTasksTool() bool {
	for _, t := range b.Tools {
		if t.Name() == "delegate_tasks" {
			return true
		}
	}
	return false
}

// hasHandoffTool returns true if browser_request_human is among the available tools.
func (b *SystemPromptBuilder) hasHandoffTool() bool {
	for _, t := range b.Tools {
		if t.Name() == "browser_request_human" {
			return true
		}
	}
	return false
}

// hasEmailTools returns true if email_list is among the available tools.
func (b *SystemPromptBuilder) hasEmailTools() bool {
	for _, t := range b.Tools {
		if t.Name() == "email_list" {
			return true
		}
	}
	return false
}

// hasSearchToolsTool returns true if search_tools is among the available tools.
func (b *SystemPromptBuilder) hasSearchToolsTool() bool {
	for _, t := range b.Tools {
		if t.Name() == "search_tools" {
			return true
		}
	}
	return false
}

func (b *SystemPromptBuilder) hasCodeIntelTools() bool {
	for _, t := range b.Tools {
		if t.Name() == "repo_map" {
			return true
		}
	}
	return false
}

// hasFlowTools returns true if search_flows is among the available tools.
func (b *SystemPromptBuilder) hasFlowTools() bool {
	for _, t := range b.Tools {
		if t.Name() == "search_flows" {
			return true
		}
	}
	return false
}

// ToolNames returns a list of all available tool names.
func (b *SystemPromptBuilder) ToolNames() []string {
	var names []string
	for _, t := range b.Tools {
		names = append(names, t.Name())
	}
	if len(b.Toolsets) > 0 {
		ctx := &minimalReadonlyContext{Context: context.Background()}
		for _, ts := range b.Toolsets {
			mcpTools, err := ts.Tools(ctx)
			if err != nil {
				continue
			}
			for _, t := range mcpTools {
				names = append(names, t.Name())
			}
		}
	}
	return names
}
