package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/tool"
)

func TestNewSubAgentManager_Defaults(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})

	if mgr.Config.MaxDepth != 2 {
		t.Errorf("MaxDepth = %d, want 2", mgr.Config.MaxDepth)
	}
	if mgr.Config.MaxConcurrent != 5 {
		t.Errorf("MaxConcurrent = %d, want 5", mgr.Config.MaxConcurrent)
	}
	if mgr.Config.TaskTimeout != 10*time.Minute {
		t.Errorf("TaskTimeout = %v, want 10m", mgr.Config.TaskTimeout)
	}
	if mgr.Config.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", mgr.Config.MaxRetries)
	}
}

func TestNewSubAgentManager_CustomConfig(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{
		MaxDepth:      3,
		MaxConcurrent: 10,
		TaskTimeout:   10 * time.Minute,
	})

	if mgr.Config.MaxDepth != 3 {
		t.Errorf("MaxDepth = %d, want 3", mgr.Config.MaxDepth)
	}
	if mgr.Config.MaxConcurrent != 10 {
		t.Errorf("MaxConcurrent = %d, want 10", mgr.Config.MaxConcurrent)
	}
	if mgr.Config.TaskTimeout != 10*time.Minute {
		t.Errorf("TaskTimeout = %v, want 10m", mgr.Config.TaskTimeout)
	}
}

// staticToolset implements tool.Toolset with a fixed tool list (for MCP group tests).
type staticToolset struct {
	name  string
	tools []tool.Tool
}

func (s *staticToolset) Name() string { return s.name }
func (s *staticToolset) Tools(_ adkagent.ReadonlyContext) ([]tool.Tool, error) {
	return s.tools, nil
}

func TestSubAgentManager_ResolveTools(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})
	mgr.ToolGroups = map[string]*ToolGroup{
		"core": {
			Name: "core",
			Tools: mockTools(
				"read_file", "write_file", "shell_command",
				"memory_save",       // should be excluded
				"delegate_tasks",    // should be excluded
				"schedule_job",      // should be excluded
				"save_credential",   // should be excluded
				"remove_credential", // should be excluded
				"grep_search",
			),
		},
		"browser": {
			Name:  "browser",
			Tools: mockTools("browser_navigate", "browser_click"),
		},
		"mcp:email": {
			Name:     "mcp:email",
			Toolsets: []tool.Toolset{&staticToolset{name: "email", tools: mockTools("send_email")}},
		},
	}

	// Resolve with a group name — excluded tools are removed
	tools, toolsets, warnings := mgr.resolveTools(context.Background(), []string{"core"})
	if len(tools) != 4 {
		t.Errorf("resolveTools([core]) returned %d tools, want 4 (excluding 5 excluded tools)", len(tools))
	}
	if len(toolsets) != 0 {
		t.Errorf("resolveTools([core]) returned %d toolsets, want 0", len(toolsets))
	}
	if len(warnings) != 0 {
		t.Errorf("resolveTools([core]) returned warnings: %v", warnings)
	}

	// Verify excluded tools are not present
	for _, ft := range tools {
		if excludedChildTools[ft.Name()] {
			t.Errorf("resolveTools returned excluded tool %q", ft.Name())
		}
	}

	// Resolve with individual tool names
	tools, _, _ = mgr.resolveTools(context.Background(), []string{"read_file", "grep_search"})
	if len(tools) != 2 {
		t.Errorf("resolveTools([read_file, grep_search]) returned %d tools, want 2", len(tools))
	}

	// Individual tool name that is excluded — should not be returned
	tools, _, _ = mgr.resolveTools(context.Background(), []string{"read_file", "memory_save"})
	if len(tools) != 1 {
		t.Errorf("resolveTools([read_file, memory_save]) returned %d tools, want 1", len(tools))
	}

	// Resolve multiple groups
	tools, _, _ = mgr.resolveTools(context.Background(), []string{"core", "browser"})
	if len(tools) != 6 { // 4 non-excluded from core + 2 from browser
		t.Errorf("resolveTools([core, browser]) returned %d tools, want 6", len(tools))
	}

	// Mixed: group name + individual tool name
	tools, _, _ = mgr.resolveTools(context.Background(), []string{"browser", "grep_search"})
	if len(tools) != 3 { // 2 browser + 1 grep_search
		t.Errorf("resolveTools([browser, grep_search]) returned %d tools, want 3", len(tools))
	}

	// Unknown group name — should produce a warning
	tools, _, warnings = mgr.resolveTools(context.Background(), []string{"drills"})
	if len(tools) != 0 {
		t.Errorf("resolveTools([drills]) returned %d tools, want 0", len(tools))
	}
	if len(warnings) != 1 {
		t.Errorf("resolveTools([drills]) returned %d warnings, want 1", len(warnings))
	} else if !strings.Contains(warnings[0], "drills") {
		t.Errorf("warning should mention 'drills', got: %s", warnings[0])
	}

	// Mixed known group + unknown name — should resolve known group and warn about unknown
	tools, _, warnings = mgr.resolveTools(context.Background(), []string{"browser", "nonexistent"})
	if len(tools) != 2 {
		t.Errorf("resolveTools([browser, nonexistent]) returned %d tools, want 2", len(tools))
	}
	if len(warnings) != 1 {
		t.Errorf("resolveTools([browser, nonexistent]) returned %d warnings, want 1", len(warnings))
	}

	// MCP group by name loads toolset
	_, toolsets, warnings = mgr.resolveTools(context.Background(), []string{"mcp:email"})
	if len(toolsets) != 1 {
		t.Errorf("resolveTools([mcp:email]) returned %d toolsets, want 1", len(toolsets))
	}
	if len(warnings) != 0 {
		t.Errorf("resolveTools([mcp:email]) warnings: %v", warnings)
	}

	// Bare MCP tool name from toolset (was broken — only searched g.Tools)
	tools, toolsets, warnings = mgr.resolveTools(context.Background(), []string{"send_email"})
	if len(tools) != 1 || tools[0].Name() != "send_email" {
		t.Errorf("resolveTools([send_email]) tools=%v, want [send_email]", tools)
	}
	if len(toolsets) != 1 {
		t.Errorf("resolveTools([send_email]) toolsets=%d, want 1", len(toolsets))
	}
	if len(warnings) != 0 {
		t.Errorf("resolveTools([send_email]) warnings: %v", warnings)
	}

	// App-style mcp:server/tool alias
	tools, toolsets, warnings = mgr.resolveTools(context.Background(), []string{"mcp:email/send_email"})
	if len(tools) != 1 || tools[0].Name() != "send_email" {
		t.Errorf("resolveTools([mcp:email/send_email]) tools=%v, want [send_email]", tools)
	}
	if len(toolsets) < 1 {
		t.Errorf("resolveTools([mcp:email/send_email]) toolsets=%d, want >=1", len(toolsets))
	}
	if len(warnings) != 0 {
		t.Errorf("resolveTools([mcp:email/send_email]) warnings: %v", warnings)
	}
}

func TestSubAgentManager_ResolveToolsEmpty(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})
	mgr.ToolGroups = map[string]*ToolGroup{
		"core": {
			Name:  "core",
			Tools: mockTools("read_file", "grep_search"),
		},
	}

	// Empty request → zero tools
	tools, toolsets, warnings := mgr.resolveTools(context.Background(), nil)
	if len(tools) != 0 {
		t.Errorf("resolveTools(nil) returned %d tools, want 0", len(tools))
	}
	if len(toolsets) != 0 {
		t.Errorf("resolveTools(nil) returned %d toolsets, want 0", len(toolsets))
	}
	if len(warnings) != 0 {
		t.Errorf("resolveTools(nil) returned warnings: %v", warnings)
	}

	tools, _, _ = mgr.resolveTools(context.Background(), []string{})
	if len(tools) != 0 {
		t.Errorf("resolveTools([]) returned %d tools, want 0", len(tools))
	}
}

func TestSubAgentManager_BuildChildPrompt(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})

	task := SubAgentTask{
		Name:         "researcher",
		Description:  "Find all references to function X",
		Instructions: "Check only .go files",
	}

	prompt := mgr.buildChildPrompt(context.Background(), task)

	// Check key sections are present
	if !contains(prompt, "researcher") {
		t.Error("prompt missing agent name")
	}
	if !contains(prompt, "Find all references to function X") {
		t.Error("prompt missing task description")
	}
	if !contains(prompt, "Check only .go files") {
		t.Error("prompt missing instructions")
	}
	if !contains(prompt, "Behavior Rules") {
		t.Error("prompt missing behavior rules")
	}
}

func TestSubAgentManager_BuildChildPromptNoInstructions(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})

	task := SubAgentTask{
		Name:        "worker",
		Description: "Do something",
	}

	prompt := mgr.buildChildPrompt(context.Background(), task)

	if contains(prompt, "## Instructions") {
		t.Error("prompt should NOT contain Instructions section when no instructions provided")
	}
}

func TestSubAgentManager_BuildChildPromptPrefersRequestSkillIndex(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})
	mgr.SkillIndex = "STATIC SKILL INDEX"
	task := SubAgentTask{Name: "worker", Description: "Use a skill"}

	ctx := WithPromptOverrides(context.Background(), &PromptOverrides{SkillIndex: "REQUEST SKILL INDEX"})
	prompt := mgr.buildChildPrompt(ctx, task)
	if !contains(prompt, "REQUEST SKILL INDEX") {
		t.Fatal("prompt missing request-scoped skill index")
	}
	if contains(prompt, "STATIC SKILL INDEX") {
		t.Fatal("prompt included static skill index despite request-scoped override")
	}

	fallback := mgr.buildChildPrompt(context.Background(), task)
	if !contains(fallback, "STATIC SKILL INDEX") {
		t.Fatal("prompt missing static skill index fallback")
	}
}

func TestSubAgentManager_BuildChildPromptWithHTTPTools(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})
	mgr.ToolGroups = map[string]*ToolGroup{
		"core": {
			Name:  "core",
			Tools: mockTools("http_request", "list_credentials", "resolve_credential", "read_file"),
		},
	}

	task := SubAgentTask{
		Name:        "api-caller",
		Description: "Call an API",
		ToolFilter:  []string{"core"},
	}

	prompt := mgr.buildChildPrompt(context.Background(), task)

	if !contains(prompt, "## HTTP Requests") {
		t.Error("prompt missing HTTP Requests section when http_request tool is available")
	}
	if !contains(prompt, "## Credentials") {
		t.Error("prompt missing Credentials section when resolve_credential tool is available")
	}
	if !contains(prompt, "Do NOT write scripts") {
		t.Error("prompt missing anti-script guidance")
	}
}

func TestSubAgentManager_BuildChildPromptWithoutHTTPTools(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})
	mgr.ToolGroups = map[string]*ToolGroup{
		"core": {
			Name:  "core",
			Tools: mockTools("read_file", "grep_search"),
		},
	}

	task := SubAgentTask{
		Name:        "searcher",
		Description: "Search files",
		ToolFilter:  []string{"core"},
	}

	prompt := mgr.buildChildPrompt(context.Background(), task)

	if contains(prompt, "## HTTP Requests") {
		t.Error("prompt should NOT contain HTTP Requests section when http_request tool is not available")
	}
	if contains(prompt, "## Credentials") {
		t.Error("prompt should NOT contain Credentials section when resolve_credential tool is not available")
	}
}

func TestSubAgentManager_BuildChildPromptPrimaryToolsGuidance(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})
	mgr.ToolGroups = map[string]*ToolGroup{
		"browser": {
			Name:  "browser",
			Tools: mockTools("browser_navigate", "browser_snapshot"),
		},
	}

	// With ToolFilter set, should include primary tools guidance
	task := SubAgentTask{
		Name:        "researcher",
		Description: "Get current prices from Amazon",
		ToolFilter:  []string{"browser"},
	}
	prompt := mgr.buildChildPrompt(context.Background(), task)

	if !contains(prompt, "PRIMARY tools") {
		t.Error("prompt missing primary tools guidance when ToolFilter is set")
	}
	if !contains(prompt, "browser") {
		t.Error("prompt missing tool filter names in primary tools guidance")
	}

	// Without ToolFilter, should NOT include primary tools guidance
	taskNoFilter := SubAgentTask{
		Name:        "worker",
		Description: "Do something",
	}
	promptNoFilter := mgr.buildChildPrompt(context.Background(), taskNoFilter)

	if contains(promptNoFilter, "PRIMARY tools") {
		t.Error("prompt should NOT contain primary tools guidance when ToolFilter is empty")
	}
}

func TestSubAgentManager_BuildChildPromptWithWebTools(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})
	mgr.WebSearchToolName = "tavily_search"
	mgr.WebExtractToolName = "tavily_extract"
	mgr.ToolGroups = map[string]*ToolGroup{
		"web": {
			Name:  "web",
			Tools: mockTools("web_fetch", "tavily_search", "tavily_extract"),
		},
	}

	task := SubAgentTask{
		Name:        "researcher",
		Description: "Research a topic",
		ToolFilter:  []string{"web"},
	}

	prompt := mgr.buildChildPrompt(context.Background(), task)

	// Should have web tools section
	if !contains(prompt, "## Web Tools") {
		t.Error("prompt missing Web Tools section when web_fetch and search tools are available")
	}
	// Should mention tavily_search with staleness caveat
	if !contains(prompt, "tavily_search") {
		t.Error("prompt missing tavily_search tool name")
	}
	if !contains(prompt, "stale") {
		t.Error("prompt missing staleness caveat for search results")
	}
	// Should mention tavily_extract as fallback
	if !contains(prompt, "tavily_extract") {
		t.Error("prompt missing tavily_extract fallback")
	}
	// Should NOT mention browser when browser tools are absent
	if contains(prompt, "browser_navigate") {
		t.Error("prompt should NOT mention browser tools when they are not in the toolset")
	}
}

func TestSubAgentManager_BuildChildPromptWithWebAndBrowserTools(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})
	mgr.WebSearchToolName = "tavily_search"
	mgr.WebExtractToolName = "tavily_extract"
	mgr.ToolGroups = map[string]*ToolGroup{
		"web": {
			Name:  "web",
			Tools: mockTools("web_fetch", "tavily_search", "tavily_extract"),
		},
		"browser": {
			Name:  "browser",
			Tools: mockTools("browser_navigate", "browser_snapshot", "browser_click", "browser_type"),
		},
	}

	task := SubAgentTask{
		Name:        "researcher",
		Description: "Get current prices from a website",
		ToolFilter:  []string{"web", "browser"},
	}

	prompt := mgr.buildChildPrompt(context.Background(), task)

	// Should have web tools section
	if !contains(prompt, "## Web Tools") {
		t.Error("prompt missing Web Tools section")
	}
	// Should mention browser tools with live data guidance
	if !contains(prompt, "Browser tools") {
		t.Error("prompt missing browser tools guidance when browser_navigate is available")
	}
	if !contains(prompt, "live/current data") {
		t.Error("prompt missing live/current data guidance for browser tools")
	}
	// Should NOT contain the old prescriptive "Do NOT use web_fetch for search"
	if contains(prompt, "Do NOT use") {
		t.Error("prompt still contains prescriptive 'Do NOT use' language (should be behavior-based)")
	}
}

func TestSubAgentManager_DepthCheck(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{MaxDepth: 2})

	// At depth 2 (equal to max), should be blocked
	result := mgr.RunTask(context.Background(), SubAgentTask{
		Name:        "blocked",
		Description: "should fail",
		ParentDepth: 2,
	})

	if result.Status != "error" {
		t.Errorf("Status = %q, want 'error'", result.Status)
	}
	if !contains(result.Error, "max delegation depth") {
		t.Errorf("Error = %q, want to contain 'max delegation depth'", result.Error)
	}
}

func TestExcludedChildTools(t *testing.T) {
	expected := map[string]bool{
		"memory_save":       true,
		"memory_delete":     true,
		"delegate_tasks":    true,
		"schedule_job":      true,
		"save_credential":   true,
		"remove_credential": true,
	}

	for name := range expected {
		if !excludedChildTools[name] {
			t.Errorf("excludedChildTools missing %q", name)
		}
	}

	if len(excludedChildTools) != len(expected) {
		t.Errorf("excludedChildTools has %d entries, want %d", len(excludedChildTools), len(expected))
	}
}

// --- helpers ---

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && containsString(s, substr)
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// mockTool implements tool.Tool for testing filterTools.
type mockTool struct {
	name string
}

func (m mockTool) Name() string        { return m.name }
func (m mockTool) Description() string { return "mock " + m.name }
func (m mockTool) IsLongRunning() bool { return false }

// mockTools creates a []tool.Tool from a list of names.
func mockTools(names ...string) []tool.Tool {
	var result []tool.Tool
	for _, name := range names {
		result = append(result, mockTool{name: name})
	}
	return result
}

// --- flattenTraces tests ---

func TestFlattenTraces_Nil(t *testing.T) {
	result := flattenTraces(nil)
	if result != nil {
		t.Error("flattenTraces(nil) should return nil")
	}
}

func TestFlattenTraces_NoSubAgentSteps(t *testing.T) {
	trace := &ExecutionTrace{
		Steps: []TraceStep{
			{ToolName: "read_file", Success: true},
			{ToolName: "grep_search", Success: true},
		},
	}

	flattenTraces(trace)

	if len(trace.Steps) != 2 {
		t.Errorf("Steps len = %d, want 2 (no change)", len(trace.Steps))
	}
}

func TestFlattenTraces_ReplaceDelegateTasks(t *testing.T) {
	childTrace1 := &ExecutionTrace{
		Steps: []TraceStep{
			{ToolName: "read_file", Success: true, ToolArgs: map[string]any{"path": "a.go"}},
			{ToolName: "grep_search", Success: true},
		},
	}
	childTrace2 := &ExecutionTrace{
		Steps: []TraceStep{
			{ToolName: "shell_command", Success: true},
		},
	}

	trace := &ExecutionTrace{
		Steps: []TraceStep{
			{ToolName: "read_file", Success: true},
			{
				ToolName:       "delegate_tasks",
				Success:        true,
				SubAgentName:   "test-delegation",
				SubAgentTraces: []*ExecutionTrace{childTrace1, childTrace2},
			},
			{ToolName: "write_file", Success: true},
		},
	}

	flattenTraces(trace)

	// Should be: read_file, read_file(from child1), grep_search(from child1), shell_command(from child2), write_file
	if len(trace.Steps) != 5 {
		t.Errorf("Steps len = %d, want 5", len(trace.Steps))
		for i, s := range trace.Steps {
			t.Logf("  step %d: %s", i, s.ToolName)
		}
		return
	}

	expected := []string{"read_file", "read_file", "grep_search", "shell_command", "write_file"}
	for i, exp := range expected {
		if trace.Steps[i].ToolName != exp {
			t.Errorf("Step[%d].ToolName = %q, want %q", i, trace.Steps[i].ToolName, exp)
		}
	}
}

func TestFlattenTraces_SkipsInternalToolsFromChildren(t *testing.T) {
	childTrace := &ExecutionTrace{
		Steps: []TraceStep{
			{ToolName: "read_file", Success: true},
			{ToolName: "memory_save", Success: true},    // should be filtered
			{ToolName: "delegate_tasks", Success: true}, // should be filtered
		},
	}

	trace := &ExecutionTrace{
		Steps: []TraceStep{
			{
				ToolName:       "delegate_tasks",
				Success:        true,
				SubAgentTraces: []*ExecutionTrace{childTrace},
			},
		},
	}

	flattenTraces(trace)

	if len(trace.Steps) != 1 {
		t.Errorf("Steps len = %d, want 1 (only read_file)", len(trace.Steps))
	}
	if len(trace.Steps) > 0 && trace.Steps[0].ToolName != "read_file" {
		t.Errorf("Step[0].ToolName = %q, want 'read_file'", trace.Steps[0].ToolName)
	}
}

func TestFlattenTraces_DelegateWithoutTraces(t *testing.T) {
	trace := &ExecutionTrace{
		Steps: []TraceStep{
			{ToolName: "read_file", Success: true},
			{
				ToolName:       "delegate_tasks",
				Success:        true,
				SubAgentTraces: nil, // no child traces
			},
		},
	}

	flattenTraces(trace)

	// delegate_tasks with no traces is kept as-is
	if len(trace.Steps) != 2 {
		t.Errorf("Steps len = %d, want 2", len(trace.Steps))
	}
}

func TestIsRawContextDeadlineExceeded(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated error", fmt.Errorf("something else"), false},
		{"context deadline exceeded", fmt.Errorf("Post \"https://api.example.com/invoke\": context deadline exceeded"), true},
		{"timeout awaiting response headers", fmt.Errorf("http2: timeout awaiting response headers"), true},
		{"wrapped context deadline", fmt.Errorf("agent run: %w", fmt.Errorf("context deadline exceeded")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRawContextDeadlineExceeded(tt.err)
			if got != tt.want {
				t.Errorf("isRawContextDeadlineExceeded(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
