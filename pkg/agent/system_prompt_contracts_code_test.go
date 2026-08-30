package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/tool"
)

// maximalCodeBuilder returns a CodeSystemPromptBuilder with every feature
// enabled, producing the most complete code-mode prompt. Used for golden file
// comparison and code-mode contract assertions.
func maximalCodeBuilder() *CodeSystemPromptBuilder {
	base := &SystemPromptBuilder{
		WorkspaceDir:        "/home/user/project",
		CustomPrompt:        "You are a helpful assistant.",
		InstructionsContent: "Always be concise.",
		BrowserAvailable:    true,
		WebSearchAvailable:  true,
		WebExtractAvailable: true,
		WebSearchToolName:   "tavily-search",
		WebExtractToolName:  "tavily-extract",
		Timezone:            "America/New_York",
		SkillIndex:          "## Available Skills\n\n- **docker** — Docker container management\n- **git** — Git workflow helpers\n",
		FleetSection:        "\n## Available Fleets\n\n- **infra-fleet** — Infrastructure management fleet\n",
		ChannelHints:        "Format as plain text. No markdown.",
		SchedulerHints:      "This is a scheduled daily check.",
		SessionContext:      "You are in fleet wizard mode.",
		RelevantKnowledge:   "**infra/portainer.md** (53%)\nPortainer runs at 192.168.1.223:9000",
		RelevantTools:       "**browser** group:\n  - `browser_take_screenshot` — Capture a screenshot\n",
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
				Name:        "mcp:context7",
				Description: "MCP server: context7 (2 tools)",
				Toolsets:    []tool.Toolset{&staticToolset{name: "context7", tools: mockTools("get_library_docs", "resolve_library_id")}},
			},
		},
		Tools: mockTools(
			"read_file", "write_file", "shell_command",
			"process_read",
			"http_request", "delegate_tasks", "email_list",
			"browser_navigate", "browser_request_human",
			"repo_map",
			"code_definition", "code_references", "codegraph_explore",
		),
	}
	cb := NewCodeSystemPromptBuilder(base)
	cb.ProjectContext = "Use tabs."
	cb.PlanFilePersistence = true
	cb.EnforceAuthorization = true
	return cb
}

// ─── Golden Snapshot Test ────────────────────────────────────────────────────

func TestCodeSystemPromptBuilder_Golden(t *testing.T) {
	builder := maximalCodeBuilder()
	prompt := normalizePrompt(builder.Build())

	goldenPath := filepath.Join("testdata", "code_system_prompt_golden.txt")

	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(prompt), 0644); err != nil {
			t.Fatalf("failed to write golden file: %v", err)
		}
		t.Logf("updated golden file: %s (%d bytes)", goldenPath, len(prompt))
		return
	}

	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden file not found: %s\nRun with -update to generate it:\n  go test ./pkg/agent -run TestCodeSystemPromptBuilder_Golden -update", goldenPath)
	}

	goldenStr := normalizePrompt(string(golden))

	if prompt != goldenStr {
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
				firstDiff = i + 1
				t.Errorf("golden file mismatch at line %d:\n  got:    %q\n  want:   %q\n\nIf this change is intentional, run:\n  go test ./pkg/agent -run TestCodeSystemPromptBuilder_Golden -update",
					firstDiff, pLine, gLine)
				break
			}
		}

		if firstDiff == -1 {
			t.Errorf("golden file mismatch: got %d lines, want %d lines\n\nRun with -update to regenerate:\n  go test ./pkg/agent -run TestCodeSystemPromptBuilder_Golden -update",
				len(promptLines), len(goldenLines))
		}
	}
}

// ─── Code-Mode Contract Tests ────────────────────────────────────────────────

func TestCodeSystemPromptContracts_ProjectGuidance(t *testing.T) {
	cb := maximalCodeBuilder()
	prompt := cb.Build()

	assertContains(t, prompt, "## Project Guidance", "Project Guidance section present in code mode")
	assertContains(t, prompt, "Use tabs.", "project context content rendered")
	assertContains(t, prompt, "AGENTS.md files", "provenance text in project guidance section")

	// Base builder alone must NOT emit project guidance
	base := &SystemPromptBuilder{WorkspaceDir: "/home/user/project"}
	assertNotContains(t, base.Build(), "## Project Guidance", "Project Guidance must NOT appear in chat-mode (base) prompt")
}

func TestCodeSystemPromptContracts_CodeNavigationRule(t *testing.T) {
	cb := maximalCodeBuilder()
	prompt := cb.Build()

	// Code-nav rule — always present in code mode (no tool-presence gate)
	assertContains(t, prompt, "Code navigation rule (MUST)", "code-nav rule always present in code mode")
	assertContains(t, prompt, "code_definition", "code_definition referenced in rule")
	assertContains(t, prompt, "code_references", "code_references referenced in rule")

	// Codegraph-first rule — always present in code mode
	assertContains(t, prompt, "Codegraph-first rule", "codegraph-first rule always present in code mode")
	assertContains(t, prompt, "codegraph_explore", "codegraph_explore referenced in rule")

	// Stop-exploring discipline — always present in code mode
	assertContains(t, prompt, "Stop exploring when the scope is clear", "stop-exploring discipline always present in code mode")
}

func TestCodeSystemPromptContracts_PlanFilePersistence(t *testing.T) {
	cb := maximalCodeBuilder()
	prompt := cb.Build()

	assertContains(t, prompt, "Execution plan (PLAN.md):", "PLAN.md guidance present in code mode")
	assertContains(t, prompt, "announce_plan", "announce_plan referenced in PLAN.md guidance")
	assertContains(t, prompt, "MUST NOT call `announce_plan`", "Normal mode forbids announce_plan")
	assertContains(t, prompt, "update_plan", "update_plan referenced in PLAN.md guidance")

	// Without PlanFilePersistence it must not appear
	base := &SystemPromptBuilder{}
	noCode := NewCodeSystemPromptBuilder(base)
	noCode.PlanFilePersistence = false
	assertNotContains(t, noCode.Build(), "Execution plan (PLAN.md):", "PLAN.md guidance absent when PlanFilePersistence is false")
}

func TestCodeSystemPromptContracts_AuthorizationGuidance(t *testing.T) {
	cb := maximalCodeBuilder()
	prompt := cb.Build()

	assertContains(t, prompt, "Tool & folder authorization", "auth guidance present when EnforceAuthorization is true")
	assertContains(t, prompt, "user's machine", "mentions running on user's machine")

	// Without EnforceAuthorization it must not appear
	base := &SystemPromptBuilder{}
	noAuth := NewCodeSystemPromptBuilder(base)
	noAuth.EnforceAuthorization = false
	assertNotContains(t, noAuth.Build(), "Tool & folder authorization", "auth guidance absent when EnforceAuthorization is false")
}

func TestCodeSystemPromptContracts_MCPUsesFixedBridge(t *testing.T) {
	base := &SystemPromptBuilder{
		Tools: mockTools("read_file", "delegate_tasks"),
		Catalog: []*ToolGroup{
			{
				Name:        "core",
				Description: "File operations",
				Tools:       mockTools("read_file", "write_file"),
			},
			{
				Name:        "mcp:context7",
				Description: "MCP server: context7 (2 tools)",
				Toolsets:    []tool.Toolset{&staticToolset{name: "context7", tools: mockTools("get_library_docs", "resolve_library_id")}},
			},
		},
	}
	cb := NewCodeSystemPromptBuilder(base)
	prompt := cb.Build()

	assertContains(t, prompt, "**Deferred MCP tools:**", "fixed bridge guidance present")
	assertContains(t, prompt, "`describe_tools`", "schema inspection guidance present")
	assertContains(t, prompt, "`execute_tool`", "fixed execution bridge present")
	assertNotContains(t, prompt, "## MCP Tools (available directly)", "no direct MCP declarations in code mode")
	assertNotContains(t, prompt, "get_library_docs", "individual MCP tools are not baked into prompt")
}

func TestCodeSystemPromptContracts_Capabilities(t *testing.T) {
	cb := maximalCodeBuilder()
	prompt := cb.Build()

	assertContains(t, prompt, "## Capabilities", "Capabilities section present")
	// Code mode always includes code intelligence in guidance caps
	assertContains(t, prompt, "code intelligence", "code intelligence in code-mode capabilities guidance")
}

func TestCodeSystemPromptContracts_Clone(t *testing.T) {
	base := &SystemPromptBuilder{WorkspaceDir: "/original"}
	cb := NewCodeSystemPromptBuilder(base)
	cb.ProjectContext = "original context"

	clone := cb.Clone()

	// Clone should produce the same prompt output
	if cb.Build() != clone.Build() {
		t.Error("clone should produce the same prompt as original")
	}

	// Per-turn mutations on clone must not affect original
	clone.SystemPromptBuilder.WorkspaceDir = "/mutated"
	clone.ProjectContext = "mutated context"

	if strings.Contains(cb.Build(), "mutated") {
		t.Error("mutation of clone must not affect original builder")
	}

	// BuildOverride on clone must point to clone's build, not original's
	clonePrompt := clone.SystemPromptBuilder.Build()
	if !strings.Contains(clonePrompt, "mutated context") {
		t.Error("clone's BuildOverride should produce clone's project context")
	}
}

func TestCodeSystemPromptContracts_BuildOverrideWiring(t *testing.T) {
	// Verify that calling Build() on the base *SystemPromptBuilder (as ChatAgent
	// does via Clone().Build()) produces code-mode output after NewCodeSystemPromptBuilder.
	base := &SystemPromptBuilder{WorkspaceDir: "/test"}
	cb := NewCodeSystemPromptBuilder(base)
	cb.ProjectContext = "PROJ CONTEXT"

	// Calling base.Build() should invoke the code builder's build via BuildOverride
	prompt := base.Build()
	assertContains(t, prompt, "## Project Guidance", "BuildOverride wired: base.Build() emits code sections")
	assertContains(t, prompt, "PROJ CONTEXT", "BuildOverride wired: project context emitted")
}

// ─── Size Guard ───────────────────────────────────────────────────────────────

func TestCodeSystemPromptBuilder_MaximalSize(t *testing.T) {
	prompt := maximalCodeBuilder().Build()
	// Code-mode prompt includes all base sections plus: project guidance,
	// code-nav rules, codegraph-first, stop-exploring, PLAN.md, auth gates,
	// MCP tools listing. Budget ceiling is higher than chat mode.
	if len(prompt) > 16000 {
		t.Errorf("code-mode maximal prompt too large: %d bytes (limit 16000)", len(prompt))
	}
	if len(prompt) < 6000 {
		t.Errorf("code-mode maximal prompt suspiciously small: %d bytes (expected > 6000)", len(prompt))
	}
	t.Logf("code-mode maximal prompt size: %d bytes", len(prompt))
}

// ─── Regression: base builder must not emit code sections ────────────────────

func TestCodeSystemPromptContracts_NoCodeSectionsInChatMode(t *testing.T) {
	// Maximal chat builder must not have any code-mode-only sections
	prompt := maximalBuilder().Build()

	for _, forbidden := range []string{
		"## Project Guidance",
		"Code navigation rule (MUST)",
		"Codegraph-first rule",
		"Stop exploring when the scope is clear",
		"Execution plan (PLAN.md):",
		"Tool & folder authorization",
		"## MCP Tools (available directly)",
	} {
		assertNotContains(t, prompt, forbidden, fmt.Sprintf("code-only section %q must not appear in chat-mode prompt", forbidden))
	}
}

// TestCodeSystemPromptContracts_NoGenerativeUISection verifies that the code-mode
// prompt does NOT contain the "Visual Apps (Generative UI)" section. astonish-app
// fences are Studio-only; exposing this section in Astonish Code causes the
// coding agent to generate astonish-app output for plain "build an app" requests.
func TestCodeSystemPromptContracts_NoGenerativeUISection(t *testing.T) {
	prompt := maximalCodeBuilder().Build()

	assertNotContains(t, prompt, "## Visual Apps (Generative UI)", "Visual Apps section must NOT appear in code-mode prompt")
	assertNotContains(t, prompt, "astonish-app code fence", "astonish-app fence instruction must NOT appear in code-mode prompt")
	assertNotContains(t, prompt, "generative-ui", "generative-ui skill must NOT be referenced in code-mode prompt")
}

func TestCodeSystemPromptContracts_NoCredScheduleDistillInCodeMode(t *testing.T) {
	// Credentials, scheduler, and distill are platform-only services with no
	// backing in code mode. Their tools and tool names must not appear in the
	// code-mode system prompt.
	prompt := maximalCodeBuilder().Build()

	// Credential tools
	assertNotContains(t, prompt, "resolve_credential", "resolve_credential must NOT appear in code-mode prompt")
	assertNotContains(t, prompt, "save_credential", "save_credential must NOT appear in code-mode prompt")
	assertNotContains(t, prompt, "list_credentials", "list_credentials must NOT appear in code-mode prompt")
	assertNotContains(t, prompt, "credential management", "\"credential management\" capability must NOT appear in code-mode prompt")

	// Scheduler tools
	assertNotContains(t, prompt, "schedule_job", "schedule_job must NOT appear in code-mode prompt")
	assertNotContains(t, prompt, "job scheduling", "\"job scheduling\" capability must NOT appear in code-mode prompt")

	// Distill tools
	assertNotContains(t, prompt, "distill_flow", "distill_flow must NOT appear in code-mode prompt")
}

func TestCodeSystemPromptContracts_NoMemoryToolsInCodeMode(t *testing.T) {
	// Memory tools (memory_search, memory_save, memory_delete) have no backing
	// store in code mode — they must not be referenced in the code-mode prompt
	// because they are not registered as tools and calling them wastes turns.
	prompt := maximalCodeBuilder().Build()

	assertNotContains(t, prompt, "memory_search", "memory_search must NOT appear in code-mode prompt")
	assertNotContains(t, prompt, "memory_save", "memory_save must NOT appear in code-mode prompt")
	assertNotContains(t, prompt, "memory_delete", "memory_delete must NOT appear in code-mode prompt")
	assertNotContains(t, prompt, "memory usage", "\"memory usage\" capability hint must NOT appear in code-mode prompt")
}

// ─── Plan Mode Completeness Self-Check Contract Tests ────────────────────────

func TestGraphPlanModeSystemContext_CompletenessCheck(t *testing.T) {
	ctx := GraphPlanModeSystemContext

	if !strings.Contains(ctx, "COMPLETENESS SELF-CHECK") {
		t.Errorf("GraphPlanModeSystemContext must contain 'COMPLETENESS SELF-CHECK'")
	}
	if !strings.Contains(ctx, "frontend") {
		t.Errorf("GraphPlanModeSystemContext must contain 'frontend' (frontend/backend coverage check)")
	}
	if !strings.Contains(ctx, "terminal TUI") {
		t.Errorf("GraphPlanModeSystemContext must contain 'terminal TUI' (TUI parity check)")
	}
	if !strings.Contains(ctx, "docs/architecture") {
		t.Errorf("GraphPlanModeSystemContext must contain 'docs/architecture' (documentation check)")
	}
	if !strings.Contains(ctx, "tests") && !strings.Contains(ctx, "*_test.go") {
		t.Errorf("GraphPlanModeSystemContext must contain 'tests' or '*_test.go' (test coverage check)")
	}
	if !strings.Contains(ctx, "breaking changes") {
		t.Errorf("GraphPlanModeSystemContext must contain 'breaking changes' (user-facing changes check)")
	}
	if !strings.Contains(ctx, "summary") {
		t.Errorf("GraphPlanModeSystemContext must contain 'summary' (PHASE 4 summary field)")
	}
}

func TestPlanModeSystemContext_CompletenessCheck(t *testing.T) {
	ctx := PlanModeSystemContext

	if !strings.Contains(ctx, "COMPLETENESS SELF-CHECK") {
		t.Errorf("PlanModeSystemContext must contain 'COMPLETENESS SELF-CHECK'")
	}
	if !strings.Contains(ctx, "frontend") {
		t.Errorf("PlanModeSystemContext must contain 'frontend' (frontend/backend coverage check)")
	}
	if !strings.Contains(ctx, "terminal TUI") {
		t.Errorf("PlanModeSystemContext must contain 'terminal TUI' (TUI parity check)")
	}
}
