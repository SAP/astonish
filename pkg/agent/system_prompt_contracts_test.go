package agent

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update golden files")

// ─── Helpers ─────────────────────────────────────────────────────────────────

// maximalBuilder returns a SystemPromptBuilder with every feature enabled,
// producing the most complete prompt possible. This is the configuration
// used for golden file comparison and maximal contract assertions.
func maximalBuilder() *SystemPromptBuilder {
	return &SystemPromptBuilder{
		WorkspaceDir:          "/home/user/project",
		CustomPrompt:          "You are a helpful assistant.",
		InstructionsContent:   "Always be concise.",
		ProjectContext:        "Use tabs.",
		BrowserAvailable:      true,
		MemorySearchAvailable: true,
		WebSearchAvailable:    true,
		WebExtractAvailable:   true,
		WebSearchToolName:     "tavily-search",
		WebExtractToolName:    "tavily-extract",
		Timezone:              "America/New_York",
		PlanFilePersistence:   true,
		SkillIndex:            "## Available Skills\n\n- **docker** — Docker container management\n- **git** — Git workflow helpers\n",
		FleetSection:          "\n## Available Fleets\n\n- **infra-fleet** — Infrastructure management fleet\n",
		ChannelHints:          "Format as plain text. No markdown.",
		SchedulerHints:        "This is a scheduled daily check.",
		SessionContext:        "You are in fleet wizard mode.",
		RelevantKnowledge:     "**infra/portainer.md** (53%)\nPortainer runs at 192.168.1.223:9000",
		RelevantTools:         "**browser** group:\n  - `browser_take_screenshot` — Capture a screenshot\n",
		Identity: &AgentIdentity{
			Name:     "Astonish Bot",
			Username: "astonish_ai",
			Email:    "bot@example.com",
			Bio:      "An AI assistant",
			Website:  "https://example.com",
			Locale:   "en-US",
			Timezone: "America/New_York",
		},
		Catalog: []*ToolGroup{
			{
				Name:        "core",
				Description: "Core file and shell tools",
				Tools:       mockTools("read_file", "write_file", "shell_command"),
			},
			{
				Name:        "browser",
				Description: "Browser automation tools",
				Tools:       mockTools("browser_navigate", "browser_click"),
			},
		},
		Tools: mockTools(
			"read_file", "write_file", "shell_command",
			"save_credential", "schedule_job", "process_read",
			"http_request", "delegate_tasks", "email_list",
			"browser_navigate", "browser_request_human",
			"search_tools", "search_flows", "memory_search", "repo_map",
		),
	}
}

// minimalBuilder returns a SystemPromptBuilder with no optional features.
func minimalBuilder() *SystemPromptBuilder {
	return &SystemPromptBuilder{}
}

// assertContains checks that prompt contains substr, failing with a descriptive message.
func assertContains(t *testing.T, prompt, substr, description string) {
	t.Helper()
	if !strings.Contains(prompt, substr) {
		t.Errorf("CONTRACT VIOLATION: %s\n  expected prompt to contain: %q", description, substr)
	}
}

// assertNotContains checks that prompt does NOT contain substr.
func assertNotContains(t *testing.T, prompt, substr, description string) {
	t.Helper()
	if strings.Contains(prompt, substr) {
		t.Errorf("UNEXPECTED SECTION: %s\n  prompt should NOT contain: %q", description, substr)
	}
}

// normalizePrompt replaces platform-dependent values with canonical placeholders
// so the golden file is stable across OS/arch combinations.
var osLineRe = regexp.MustCompile(`- OS: \S+/\S+`)

func normalizePrompt(s string) string {
	return osLineRe.ReplaceAllString(s, "- OS: PLATFORM/ARCH")
}

// ─── Golden Snapshot Test ────────────────────────────────────────────────────

func TestSystemPromptBuilder_Golden(t *testing.T) {
	builder := maximalBuilder()
	prompt := normalizePrompt(builder.Build())

	goldenPath := filepath.Join("testdata", "system_prompt_golden.txt")

	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(prompt), 0644); err != nil {
			t.Fatalf("failed to write golden file: %v", err)
		}
		t.Logf("updated golden file: %s (%d bytes)", goldenPath, len(prompt))
		return
	}

	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden file not found: %s\nRun with -update to generate it:\n  go test ./pkg/agent -run TestSystemPromptBuilder_Golden -update", goldenPath)
	}

	goldenStr := normalizePrompt(string(golden))

	if prompt != goldenStr {
		// Find first differing line for a useful error message
		promptLines := strings.Split(prompt, "\n")
		goldenLines := strings.Split(goldenStr, "\n")

		firstDiff := -1
		maxLines := len(promptLines)
		if len(goldenLines) > maxLines {
			maxLines = len(goldenLines)
		}

		for i := 0; i < maxLines; i++ {
			var pLine, gLine string
			if i < len(promptLines) {
				pLine = promptLines[i]
			}
			if i < len(goldenLines) {
				gLine = goldenLines[i]
			}
			if pLine != gLine {
				firstDiff = i + 1 // 1-indexed
				t.Errorf("golden file mismatch at line %d:\n  got:    %q\n  want:   %q\n\nIf this change is intentional, run:\n  go test ./pkg/agent -run TestSystemPromptBuilder_Golden -update",
					firstDiff, pLine, gLine)
				break
			}
		}

		if firstDiff == -1 {
			// Lines match but lengths differ
			t.Errorf("golden file mismatch: got %d lines, want %d lines\n\nRun with -update to regenerate:\n  go test ./pkg/agent -run TestSystemPromptBuilder_Golden -update",
				len(promptLines), len(goldenLines))
		}
	}
}

// ─── Structural Contract Tests ───────────────────────────────────────────────
//
// These tests verify that specific strings the frontend and backend depend on
// are present in the system prompt. Each assertion is tagged with a description
// explaining WHY it matters (which component breaks without it).

func TestSystemPromptContracts_GenerativeUI(t *testing.T) {
	prompt := maximalBuilder().Build()

	// The section header itself
	assertContains(t, prompt, "## Visual Apps (Generative UI)", "Visual Apps section header — frontend AppPreviewCard relies on LLM producing astonish-app fences")

	// Contract 1: astonish-app code fence syntax
	// Backend: chat_runner.go:533 appPreviewFenceRe matches ```astonish-app
	// Frontend: StudioChat.tsx:681-703 + inline regex at :2204
	assertContains(t, prompt, "astonish-app", "astonish-app fence name — backend regex appPreviewFenceRe and frontend regex both match this exact string")

	// Contract: skill_lookup instruction — the model MUST be told to load the
	// generative-ui skill before generating any visual app. This replaced the
	// inline API documentation that was previously in Tier 1.
	assertContains(t, prompt, "skill_lookup", "skill_lookup instruction — model must call skill_lookup to load generative-ui docs before generating apps")
	assertContains(t, prompt, "generative-ui", "generative-ui skill name — must appear in Available Skills index and/or the instruction text")

	// Contract 10: No fetch/XMLHttpRequest — sandbox restriction
	// These hard constraints remain in Tier 1 because violations cause runtime errors.
	assertContains(t, prompt, "fetch()", "fetch() blocked — sandbox blocks raw fetch, LLM must be warned even before loading skill")
	assertContains(t, prompt, "XMLHttpRequest", "XMLHttpRequest blocked — sandbox blocks raw XHR")
	assertContains(t, prompt, "axios", "axios blocked — sandbox blocks third-party HTTP libs")

	// Contract: Available modules constraint
	assertContains(t, prompt, "lucide-react", "lucide-react available — sandbox module list in Tier 1 hard constraints")
	assertContains(t, prompt, "recharts", "recharts available — sandbox module list in Tier 1 hard constraints")

	// Contract: No component libraries
	assertContains(t, prompt, "No component libraries", "no component libraries — sandbox constraint remains in Tier 1")

	// Contract 3: Mermaid diagrams for reports
	assertContains(t, prompt, "mermaid", "mermaid blocks — MermaidBlock.tsx renders ```mermaid fences inside report markdown")

	// Contract 20: Reports use a TWO-STEP contract — write_file PLUS astonish-report fence.
	// Both signals are required for an artifact to be promoted to inline report
	// rendering on the frontend (EmbeddedFileViewer). The prompt MUST teach the
	// LLM both halves of the contract:
	//   1. write_file creates the markdown file on disk;
	//   2. an ```astonish-report fence with `path:` flips the IsReport gate so
	//      the chat UI embeds the artifact inline instead of as a download card.
	// These three assertions prevent silent regression where the prompt teaches
	// only one side of the contract and the gate effectively goes back to "any
	// last-turn markdown file is a report" (the b5310ae regression).
	assertContains(t, prompt, "write_file", "write_file tool — Step 1 of the report contract: persist the markdown file on disk")
	assertContains(t, prompt, "astonish-report", "astonish-report fence — Step 2 of the report contract: signal the file as a report so the frontend embeds it inline")
	assertContains(t, prompt, "path:", "astonish-report fence requires `path:` — the path MUST match the absolute path passed to write_file or the marker is silently dropped")
}

func TestSystemPromptContracts_Delegation(t *testing.T) {
	// Delegation contracts require Catalog to be set
	builder := maximalBuilder()
	prompt := builder.Build()

	// Contract 11: delegate_tasks tool
	// Backend: chat_runner.go processes delegate_tasks, emits subtask_progress events
	// Frontend: TaskPlanPanel.tsx renders delegation progress
	assertContains(t, prompt, "## Task Delegation", "Task Delegation section — required when Catalog is configured")
	assertContains(t, prompt, "delegate_tasks", "delegate_tasks tool name — backend processes this tool, frontend renders subtask_progress events")

	// Contract 2: announce_plan tool
	// Backend: chat_runner.go:357-425 suppresses announce_plan from event stream, emits plan events
	// Frontend: PlanPanel.tsx renders plan steps
	assertContains(t, prompt, "announce_plan", "announce_plan tool — backend suppresses from stream and emits plan events; PlanPanel.tsx renders")

	// Contract 4: plan_step field
	// Backend: chat_runner.go:424 emits step progress
	// Frontend: TaskPlanPanel.tsx renders step-linked task progress
	assertContains(t, prompt, "plan_step", "plan_step field — backend emits step progress events; TaskPlanPanel.tsx renders step-linked tasks")

	// Planning strategy guidance
	assertContains(t, prompt, "Planning strategy", "Planning strategy section — guides LLM on how to structure delegation")

	// Available tool groups listed
	assertContains(t, prompt, "Available tool groups", "tool groups listing — LLM needs to know which groups exist for delegate_tasks tools parameter")
	assertContains(t, prompt, "**core**", "core tool group listed")
	assertContains(t, prompt, "**browser**", "browser tool group listed")

	// Tool group examples
	assertContains(t, prompt, `tools: ["browser"]`, "tool group usage example — shows LLM the syntax for requesting groups")
}

func TestSystemPromptContracts_ToolUse(t *testing.T) {
	prompt := maximalBuilder().Build()

	// Contract 15: memory_search — present unconditionally in Tool Use section
	assertContains(t, prompt, "memory_search", "memory_search — prompt always references memory search for guidance retrieval")
	assertContains(t, prompt, "memory_save", "memory_save — prompt tells LLM to save knowledge after overcoming obstacles")

	// Contract 17: http_request RFC1918 restriction
	assertContains(t, prompt, "http_request", "http_request tool name — Tool Use section has RFC1918 restriction")
	assertContains(t, prompt, "192.168", "RFC1918 192.168.x.x restriction — http_request cannot reach private IPs")
	assertContains(t, prompt, "10.x.x.x", "RFC1918 10.x.x.x restriction")
	assertContains(t, prompt, "localhost", "localhost restriction — http_request cannot reach localhost")
	assertContains(t, prompt, "curl", "curl fallback — shell_command + curl for private endpoints")

	// Contract 16: search_tools guidance (conditional on tool presence)
	assertContains(t, prompt, "search_tools", "search_tools guidance — present because search_tools tool is in Tools list")
	assertContains(t, prompt, `search_tools(query="*")`, "search_tools list-all guidance — LLM should use this to list all tools")

	// Contract 18: skill_lookup (conditional on SkillIndex)
	assertContains(t, prompt, "skill_lookup", "skill_lookup — present because SkillIndex is set")
	assertContains(t, prompt, "Skill-first rule", "skill-first rule — LLM must always load matching skills")

	// Tool use core rules
	assertContains(t, prompt, "## Tool Use", "Tool Use section header")
	assertContains(t, prompt, "read_file/edit_file/write_file", "file tool preference — prefer dedicated tools over shell sed/awk")
	assertContains(t, prompt, "shell_command", "shell_command mentioned — for private network fallback and general use")

	// Contract: repo navigation tools preferred over shell for source inspection
	assertContains(t, prompt, "file_tree (structure), find_files (glob/discovery), grep_search (text/regex search), read_file (contents)", "repo navigation tool preference — dedicated tools over shell ls/find/grep")
	assertContains(t, prompt, "Do NOT use shell_command with ls/find/grep/rg/cat/head/tail", "negative rule — no shell for browsing/searching files")
}

func TestSystemPromptContracts_Identity(t *testing.T) {
	prompt := maximalBuilder().Build()

	// Contract 12: browser_request_human (in identity section)
	assertContains(t, prompt, "browser_request_human", "browser_request_human — identity section tells LLM to hand off CAPTCHAs to user")

	// Contract 13: email_wait
	assertContains(t, prompt, "email_wait", "email_wait — identity section tells LLM to wait for verification emails")

	// Contract 14: credential store
	assertContains(t, prompt, "credential store", "credential store — identity section tells LLM to save credentials after registration")

	// Identity fields rendered
	assertContains(t, prompt, "## Agent Identity", "Agent Identity section header")
	assertContains(t, prompt, "**Name:** Astonish Bot", "identity name rendered")
	assertContains(t, prompt, "**Username:** astonish_ai", "identity username rendered")
	assertContains(t, prompt, "**Email:** bot@example.com", "identity email rendered")
}

func TestSystemPromptContracts_Knowledge(t *testing.T) {
	prompt := maximalBuilder().Build()

	// Knowledge Context section — always present, teaches model about injected knowledge
	assertContains(t, prompt, "## Knowledge Context", "Knowledge Context section — teaches model how to use injected knowledge")
	assertContains(t, prompt, "VERIFIED information", "knowledge is described as verified")
	assertContains(t, prompt, "specific details from knowledge sections", "instructions to use knowledge details over defaults")

	// Per-turn knowledge injection
	assertContains(t, prompt, "## Knowledge For This Task", "Knowledge For This Task section — present when RelevantKnowledge is set")
	assertContains(t, prompt, "Portainer runs at 192.168.1.223:9000", "injected knowledge content rendered")

	// Relevant tools injection
	assertContains(t, prompt, "## Relevant Tools For This Request", "Relevant Tools section — present when RelevantTools is set")
	assertContains(t, prompt, "browser_take_screenshot", "injected relevant tool rendered")
}

func TestSystemPromptContracts_Environment(t *testing.T) {
	prompt := maximalBuilder().Build()

	assertContains(t, prompt, "## Environment", "Environment section header")
	assertContains(t, prompt, "Working directory: /home/user/project", "workspace dir in environment")
	assertContains(t, prompt, "Timezone: America/New_York", "timezone in environment")
	assertContains(t, prompt, "OS:", "OS info in environment")
	assertContains(t, prompt, "PLAN.md", "plan-file persistence guidance in environment")
}

func TestSystemPromptContracts_Capabilities(t *testing.T) {
	prompt := maximalBuilder().Build()

	assertContains(t, prompt, "## Capabilities", "Capabilities section header")

	// All capabilities that should be listed with maximal builder
	for _, cap := range []string{
		"file operations",
		"shell commands",
		"web fetching",
		"browser automation",
		"credential management",
		"job scheduling",
		"process management",
		"HTTP API requests",
		"task delegation",
		"flow execution",
		"code intelligence",
		"persistent memory",
		"email",
		"fleet agents",
	} {
		assertContains(t, prompt, cap, fmt.Sprintf("capability %q listed", cap))
	}

	// Named tool references in capabilities
	assertContains(t, prompt, "tavily-search", "web search tool name in capabilities")
	assertContains(t, prompt, "tavily-extract", "web extract tool name in capabilities")
}

func TestSystemPromptContracts_DynamicSections(t *testing.T) {
	prompt := maximalBuilder().Build()

	// Channel hints
	assertContains(t, prompt, "## Output Constraints", "Output Constraints section — present when ChannelHints set")
	assertContains(t, prompt, "Format as plain text", "channel hint content rendered")

	// Scheduler hints
	assertContains(t, prompt, "## Execution Context", "Execution Context section — present when SchedulerHints set")
	assertContains(t, prompt, "scheduled daily check", "scheduler hint content rendered")

	// Session context
	assertContains(t, prompt, "## Session Task", "Session Task section — present when SessionContext set")
	assertContains(t, prompt, "fleet wizard mode", "session context content rendered")

	// Custom prompt
	assertContains(t, prompt, "You are a helpful assistant.", "custom prompt content rendered")

	// Behavior instructions
	assertContains(t, prompt, "## Behavior Instructions", "Behavior Instructions section header")
	assertContains(t, prompt, "Always be concise.", "instructions content rendered")
}

// ─── Multi-Configuration Tests ───────────────────────────────────────────────
//
// These tests verify that conditional sections correctly appear or disappear
// based on builder configuration.

func TestSystemPromptContracts_Conditional_Minimal(t *testing.T) {
	prompt := minimalBuilder().Build()

	// Core sections should always be present
	assertContains(t, prompt, "You are Astonish", "identity preamble always present")
	assertContains(t, prompt, "## Tool Use", "Tool Use always present")
	assertContains(t, prompt, "## Knowledge Context", "Knowledge Context always present")
	assertContains(t, prompt, "## Environment", "Environment always present")
	assertContains(t, prompt, "## Capabilities", "Capabilities always present")
	assertContains(t, prompt, "## Visual Apps (Generative UI)", "Visual Apps always present")

	// Optional sections should NOT be present
	assertNotContains(t, prompt, "## Agent Identity", "no identity when Identity nil")
	assertNotContains(t, prompt, "## Task Delegation", "no delegation when Catalog nil")
	assertNotContains(t, prompt, "## Output Constraints", "no channel hints when ChannelHints empty")
	assertNotContains(t, prompt, "## Execution Context", "no scheduler hints when SchedulerHints empty")
	assertNotContains(t, prompt, "## Session Task", "no session context when SessionContext empty")
	assertNotContains(t, prompt, "## Behavior Instructions", "no instructions when InstructionsContent empty")
	assertNotContains(t, prompt, "## Knowledge For This Task", "no knowledge when RelevantKnowledge empty")
	assertNotContains(t, prompt, "## Relevant Tools For This Request", "no relevant tools when RelevantTools empty")
	assertNotContains(t, prompt, "## Available Fleets", "no fleets when FleetSection empty")
	assertNotContains(t, prompt, `search_tools(query="*")`, "no search_tools guidance when tool not present")

	// Built-in skills (e.g. generative-ui) are ALWAYS present in the Available
	// Skills section, even when no user/platform skills are configured. This is
	// intentional — the model must always know to call skill_lookup for app generation.
	assertContains(t, prompt, "## Available Skills", "built-in skills always present even with empty SkillIndex")
	assertContains(t, prompt, "skill_lookup", "skill_lookup always present — needed for generative-ui skill")
	assertContains(t, prompt, "generative-ui", "generative-ui built-in skill always listed")
}

func TestSystemPromptContracts_Conditional_CatalogOnly(t *testing.T) {
	builder := minimalBuilder()
	builder.Catalog = []*ToolGroup{
		{Name: "core", Description: "Core tools", Tools: mockTools("read_file")},
	}
	prompt := builder.Build()

	assertContains(t, prompt, "## Task Delegation", "delegation section appears with Catalog")
	assertContains(t, prompt, "announce_plan", "announce_plan mentioned with Catalog")
	assertContains(t, prompt, "plan_step", "plan_step mentioned with Catalog")
	assertContains(t, prompt, "delegate_tasks", "delegate_tasks mentioned with Catalog")
	assertContains(t, prompt, "**core**", "core group listed")
}

func TestSystemPromptContracts_Conditional_IdentityOnly(t *testing.T) {
	builder := minimalBuilder()
	builder.Identity = &AgentIdentity{
		Name:  "Test Bot",
		Email: "test@example.com",
	}
	prompt := builder.Build()

	assertContains(t, prompt, "## Agent Identity", "identity section appears with Identity")
	assertContains(t, prompt, "**Name:** Test Bot", "identity name rendered")
	assertContains(t, prompt, "email_wait", "email_wait guidance in identity section")
	assertContains(t, prompt, "browser_request_human", "browser_request_human guidance in identity section")
	assertContains(t, prompt, "credential store", "credential store guidance in identity section")

	// Fields not set should not appear
	assertNotContains(t, prompt, "**Username:**", "no username when not set")
	assertNotContains(t, prompt, "**Bio:**", "no bio when not set")
	assertNotContains(t, prompt, "**Website:**", "no website when not set")
}

func TestSystemPromptContracts_Conditional_SkillIndexOnly(t *testing.T) {
	builder := minimalBuilder()
	builder.SkillIndex = "## Available Skills\n\n- **docker** — manage containers\n"
	prompt := builder.Build()

	assertContains(t, prompt, "skill_lookup", "skill_lookup mentioned when SkillIndex set")
	assertContains(t, prompt, "Skill-first rule", "skill-first rule when SkillIndex set")
	assertContains(t, prompt, "## Available Skills", "skill index section rendered")
	assertContains(t, prompt, "**docker**", "skill entry rendered")
}

func TestSystemPromptContracts_Conditional_SearchToolsOnly(t *testing.T) {
	builder := minimalBuilder()
	builder.Tools = mockTools("read_file", "search_tools")
	prompt := builder.Build()

	assertContains(t, prompt, "search_tools", "search_tools guidance when tool present")
	assertContains(t, prompt, `search_tools(query="*")`, "list-all guidance when search_tools present")
}

func TestSystemPromptContracts_Conditional_CodeNavigationRule(t *testing.T) {
	// With tree-sitter code-intel tools present, the MUST rule steering the
	// model to code_definition/code_references over grep_search must render.
	builder := minimalBuilder()
	builder.Tools = mockTools("read_file", "grep_search", "repo_map", "code_definition", "code_references")
	prompt := builder.Build()
	assertContains(t, prompt, "Code navigation rule (MUST)", "code-nav rule header when code-intel tools present")
	assertContains(t, prompt, "code_definition", "code_definition referenced in the rule")
	assertContains(t, prompt, "code_references", "code_references referenced in the rule")

	// Without code-intel tools (e.g. a channel/non-code agent), the rule must
	// NOT appear — grep_search stays domain-agnostic and the prompt stays lean.
	plain := minimalBuilder()
	plain.Tools = mockTools("read_file", "grep_search")
	assertNotContains(t, plain.Build(), "Code navigation rule (MUST)", "no code-nav rule when tree-sitter tools absent")
}

func TestSystemPromptContracts_Conditional_WebTools(t *testing.T) {
	builder := minimalBuilder()
	builder.WebSearchAvailable = true
	builder.WebSearchToolName = "my-search"
	builder.WebExtractAvailable = true
	builder.WebExtractToolName = "my-extract"
	prompt := builder.Build()

	assertContains(t, prompt, "**Web search tool:** `my-search`", "web search tool hint when configured")
	assertContains(t, prompt, "**Web extract tool:** `my-extract`", "web extract tool hint when configured")
	assertContains(t, prompt, "web search via `my-search`", "named search capability in capabilities line")
}

func TestSystemPromptContracts_Conditional_FleetOnly(t *testing.T) {
	builder := minimalBuilder()
	builder.FleetSection = "\n## Available Fleets\n\n- **my-fleet** — My test fleet\n"
	prompt := builder.Build()

	assertContains(t, prompt, "## Available Fleets", "fleet section rendered when FleetSection set")
	assertContains(t, prompt, "fleet agents", "fleet agents capability listed")
}

func TestSystemPromptContracts_Conditional_MemorySearch(t *testing.T) {
	builder := minimalBuilder()
	builder.MemorySearchAvailable = true
	prompt := builder.Build()

	assertContains(t, prompt, "persistent memory", "persistent memory capability when MemorySearchAvailable")
}

func TestSystemPromptContracts_Conditional_BrowserAvailable(t *testing.T) {
	builder := minimalBuilder()
	builder.BrowserAvailable = true
	prompt := builder.Build()

	assertContains(t, prompt, "browser automation", "browser automation capability when BrowserAvailable")
}

// ─── Size Regression Guards ──────────────────────────────────────────────────

func TestSystemPromptBuilder_MinimalSize(t *testing.T) {
	prompt := minimalBuilder().Build()
	// Minimal prompt includes always-present sections: Tool Use (incl.
	// structural-navigation guidance), Knowledge Context, Environment,
	// Capabilities, and Visual Apps (Generative UI). Budget ~15% above current.
	if len(prompt) > 5700 {
		t.Errorf("minimal prompt too large: %d bytes (limit 5700)", len(prompt))
	}
	if len(prompt) < 2000 {
		t.Errorf("minimal prompt suspiciously small: %d bytes (expected > 2000)", len(prompt))
	}
	t.Logf("minimal prompt size: %d bytes", len(prompt))
}

func TestSystemPromptBuilder_MaximalSize(t *testing.T) {
	prompt := maximalBuilder().Build()
	// Maximal prompt with all features enabled — budget ceiling reflects
	// post-Reports-section size, the Project Guidance section (AGENTS.md), the
	// code-navigation MUST rule (only rendered when tree-sitter code-intel tools
	// are present), and the PLAN.md persistence guidance (code mode). The Reports
	// two-step contract is static (~600 bytes) because the LLM cannot retrieve
	// guidance it doesn't know exists. The ceiling was raised from 12800→13000 to
	// admit the dependency-tracing planning-strategy line (trace callers/tests/docs
	// before decomposing — no partial implementations) added to the always-on
	// delegation block. See system_prompt_builder.go "## Reports" / "## Project
	// Guidance" / "## Tool Use" / "## Environment".
	if len(prompt) > 13000 {
		t.Errorf("maximal prompt too large: %d bytes (limit 13000)", len(prompt))
	}
	if len(prompt) < 5000 {
		t.Errorf("maximal prompt suspiciously small: %d bytes (expected > 5000)", len(prompt))
	}
	t.Logf("maximal prompt size: %d bytes", len(prompt))
}
