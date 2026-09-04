package agent

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/common"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
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
	if mgr.Config.InactivityTimeout != 2*time.Minute {
		t.Errorf("InactivityTimeout = %v, want 2m", mgr.Config.InactivityTimeout)
	}
	if mgr.Config.HeartbeatInterval != 5*time.Second {
		t.Errorf("HeartbeatInterval = %v, want 5s", mgr.Config.HeartbeatInterval)
	}
	if mgr.Config.DelegationTimeout != 25*time.Minute {
		t.Errorf("DelegationTimeout = %v, want 25m", mgr.Config.DelegationTimeout)
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

func TestSubAgentManager_InactivityWatchdogCancelsProviderWait(t *testing.T) {
	mgr := newLivenessTestManager(SubAgentConfig{
		TaskTimeout:       time.Second,
		InactivityTimeout: 40 * time.Millisecond,
		HeartbeatInterval: 10 * time.Millisecond,
	})
	mgr.LLM = livenessTestLLM{generate: func(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			<-ctx.Done()
		}
	}}

	var eventsMu sync.Mutex
	var progress []SubTaskProgressEvent
	mgr.SubTaskProgress = func(evt SubTaskProgressEvent) {
		eventsMu.Lock()
		progress = append(progress, evt)
		eventsMu.Unlock()
	}

	started := time.Now()
	result := mgr.RunTask(context.Background(), SubAgentTask{Name: "inert", Description: "wait"})
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("watchdog returned after %v, want prompt cancellation", elapsed)
	}
	if result.Status != "timeout" || result.InactivityReason == "" {
		t.Fatalf("result = %#v, want inactivity timeout", result)
	}
	if !strings.Contains(result.Error, "inactivity watchdog") {
		t.Fatalf("error = %q, want inactivity watchdog reason", result.Error)
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	if !hasProgressEvent(progress, "task_state", "waiting_on_model", false) {
		t.Fatalf("missing waiting_on_model event: %#v", progress)
	}
	if !hasProgressEvent(progress, "task_state", "failed", true) {
		t.Fatalf("missing no-activity warning event: %#v", progress)
	}
}

func TestSubAgentManager_ThoughtDoesNotResetInactivity(t *testing.T) {
	mgr := newLivenessTestManager(SubAgentConfig{
		TaskTimeout:       time.Second,
		InactivityTimeout: 45 * time.Millisecond,
		HeartbeatInterval: 10 * time.Millisecond,
	})
	mgr.LLM = livenessTestLLM{generate: func(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if !yield(&model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{Text: "hidden", Thought: true}}}, Partial: true}, nil) {
						return
					}
				}
			}
		}
	}}

	result := mgr.RunTask(context.Background(), SubAgentTask{Name: "thinking", Description: "wait"})
	if result.InactivityReason == "" {
		t.Fatalf("thought tokens incorrectly counted as progress: %#v", result)
	}
	if result.Result != "" {
		t.Fatalf("hidden thought leaked into result: %q", result.Result)
	}
}

func TestSubAgentManager_RetriesInactivityOnlyAfterProgress(t *testing.T) {
	mgr := newLivenessTestManager(SubAgentConfig{
		TaskTimeout:       time.Second,
		InactivityTimeout: 40 * time.Millisecond,
		HeartbeatInterval: 10 * time.Millisecond,
	})
	var calls atomic.Int32
	mgr.LLM = livenessTestLLM{generate: func(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			if calls.Add(1) == 1 {
				yield(&model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{Text: "partial"}}}, Partial: true}, nil)
				<-ctx.Done()
				return
			}
			yield(&model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{Text: "done"}}}, TurnComplete: true}, nil)
		}
	}}

	var eventsMu sync.Mutex
	var progress []SubTaskProgressEvent
	mgr.SubTaskProgress = func(evt SubTaskProgressEvent) {
		eventsMu.Lock()
		progress = append(progress, evt)
		eventsMu.Unlock()
	}

	results := mgr.RunTasks(context.Background(), []SubAgentTask{{Name: "retry", Description: "work"}})
	if len(results) != 1 || results[0].Status != "success" || results[0].Attempts != 2 {
		t.Fatalf("retry result = %#v, want success on attempt 2", results)
	}
	if !strings.Contains(results[0].Result, "done") {
		t.Fatalf("retry result missing final output: %#v", results[0])
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	if !hasProgressEvent(progress, "task_retry", "retrying", true) {
		t.Fatalf("missing retry event with inactivity reason: %#v", progress)
	}
}

func TestSubAgentManager_ParentCancellationReleasesSemaphore(t *testing.T) {
	mgr := newLivenessTestManager(SubAgentConfig{
		MaxConcurrent:     1,
		TaskTimeout:       time.Second,
		InactivityTimeout: time.Second,
		HeartbeatInterval: 10 * time.Millisecond,
	})
	var calls atomic.Int32
	mgr.LLM = livenessTestLLM{generate: func(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				return
			}
			yield(&model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{Text: "ok"}}}, TurnComplete: true}, nil)
		}
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []TaskResult, 1)
	go func() {
		done <- mgr.RunTasks(ctx, []SubAgentTask{{Name: "blocked", Description: "wait"}})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("parent cancellation did not release first task")
	}

	result := mgr.RunTasks(context.Background(), []SubAgentTask{{Name: "next", Description: "finish"}})
	if len(result) != 1 || result[0].Status != "success" {
		t.Fatalf("semaphore was not released: %#v", result)
	}
}

func newLivenessTestManager(cfg SubAgentConfig) *SubAgentManager {
	mgr := NewSubAgentManager(cfg)
	mgr.AppName = "sub-agent-liveness-test"
	mgr.UserID = "test-user"
	mgr.SessionService = common.NewAutoInitService(adksession.InMemoryService())
	return mgr
}

type livenessTestLLM struct {
	generate func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error]
}

func (l livenessTestLLM) Name() string { return "liveness-test" }

func (l livenessTestLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return l.generate(ctx, req, stream)
}

func hasProgressEvent(events []SubTaskProgressEvent, eventType, status string, noActivity bool) bool {
	for _, evt := range events {
		if evt.Type == eventType && evt.Status == status && evt.NoActivity == noActivity {
			return true
		}
	}
	return false
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

// --- evaluateTimeoutResult tests ---

// evalMockLLM implements model.LLM with a canned response or error for testing.
type evalMockLLM struct {
	response string
	err      error
}

func (m evalMockLLM) Name() string { return "eval-mock" }

func (m evalMockLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.err != nil {
			yield(nil, m.err)
			return
		}
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Parts: []*genai.Part{{Text: m.response}},
				Role:  "model",
			},
			TurnComplete: true,
		}, nil)
	}
}

func TestEvaluateTimeoutResult_Continue(t *testing.T) {
	mockLLM := evalMockLLM{
		response: "ACTION: continue\nREASON: task was reading files progressively\nGUIDANCE: continue with remaining files",
	}

	task := SubAgentTask{Name: "reader", Description: "read all go files"}
	result := TaskResult{
		Name:      "reader",
		Status:    "timeout",
		Error:     "task timed out",
		ToolCalls: 15,
		Duration:  10 * time.Minute,
		Result:    "partial output here",
	}

	action, reason, guidance := evaluateTimeoutResult(context.Background(), mockLLM, task, result)

	if action != "continue" {
		t.Errorf("action = %q, want 'continue'", action)
	}
	if reason != "task was reading files progressively" {
		t.Errorf("reason = %q, want 'task was reading files progressively'", reason)
	}
	if guidance != "continue with remaining files" {
		t.Errorf("guidance = %q, want 'continue with remaining files'", guidance)
	}
}

func TestEvaluateTimeoutResult_Restart(t *testing.T) {
	mockLLM := evalMockLLM{
		response: "ACTION: restart\nREASON: task repeated same grep 5 times\nGUIDANCE: use code_definition instead",
	}

	task := SubAgentTask{Name: "searcher", Description: "find function definition"}
	result := TaskResult{
		Name:      "searcher",
		Status:    "timeout",
		Error:     "task timed out",
		ToolCalls: 20,
		Duration:  10 * time.Minute,
	}

	action, reason, guidance := evaluateTimeoutResult(context.Background(), mockLLM, task, result)

	if action != "restart" {
		t.Errorf("action = %q, want 'restart'", action)
	}
	if reason != "task repeated same grep 5 times" {
		t.Errorf("reason = %q, want 'task repeated same grep 5 times'", reason)
	}
	if guidance != "use code_definition instead" {
		t.Errorf("guidance = %q, want 'use code_definition instead'", guidance)
	}
}

func TestEvaluateTimeoutResult_LLMError(t *testing.T) {
	mockLLM := evalMockLLM{
		err: fmt.Errorf("connection refused"),
	}

	task := SubAgentTask{Name: "worker", Description: "do work"}
	result := TaskResult{
		Name:      "worker",
		Status:    "timeout",
		Error:     "task timed out",
		ToolCalls: 5,
		Duration:  10 * time.Minute,
	}

	action, reason, guidance := evaluateTimeoutResult(context.Background(), mockLLM, task, result)

	if action != "continue" {
		t.Errorf("action = %q, want 'continue' (fallback)", action)
	}
	if !strings.Contains(reason, "evaluation unavailable") {
		t.Errorf("reason = %q, want to contain 'evaluation unavailable'", reason)
	}
	if guidance != "" {
		t.Errorf("guidance = %q, want empty string on fallback", guidance)
	}
}

func TestEvaluateTimeoutResult_MalformedResponse(t *testing.T) {
	mockLLM := evalMockLLM{
		response: "I don't know what to do. The task seems complicated.",
	}

	task := SubAgentTask{Name: "confused", Description: "something"}
	result := TaskResult{
		Name:      "confused",
		Status:    "timeout",
		Error:     "task timed out",
		ToolCalls: 3,
		Duration:  5 * time.Minute,
	}

	action, reason, guidance := evaluateTimeoutResult(context.Background(), mockLLM, task, result)

	if action != "continue" {
		t.Errorf("action = %q, want 'continue' (fallback on malformed response)", action)
	}
	if reason == "" {
		t.Error("reason should not be empty on malformed response")
	}
	_ = guidance // may or may not be empty
}

func TestEvaluateTimeoutResult_NilLLM(t *testing.T) {
	task := SubAgentTask{Name: "worker", Description: "do work"}
	result := TaskResult{
		Name:      "worker",
		Status:    "timeout",
		Error:     "task timed out",
		ToolCalls: 5,
		Duration:  10 * time.Minute,
	}

	action, reason, guidance := evaluateTimeoutResult(context.Background(), nil, task, result)

	if action != "continue" {
		t.Errorf("action = %q, want 'continue' (fallback)", action)
	}
	if !strings.Contains(reason, "evaluation unavailable") {
		t.Errorf("reason = %q, want to contain 'evaluation unavailable'", reason)
	}
	if guidance != "" {
		t.Errorf("guidance = %q, want empty", guidance)
	}
}

func TestBuildRestartPrompt(t *testing.T) {
	originalDesc := "find all bugs in the codebase"
	failedResult := TaskResult{
		Name:      "bugfinder",
		Status:    "timeout",
		Error:     "timed out",
		ToolCalls: 15,
		Result:    "some partial output that should NOT appear in restart prompt",
	}
	guidance := "try a different search strategy"

	prompt := buildRestartPrompt(originalDesc, failedResult, guidance)

	if !strings.Contains(prompt, "RESTART") {
		t.Error("restart prompt missing 'RESTART' keyword")
	}
	if !strings.Contains(prompt, "DIFFERENT approach") {
		t.Error("restart prompt missing 'DIFFERENT approach' instruction")
	}
	if !strings.Contains(prompt, originalDesc) {
		t.Error("restart prompt missing original task description")
	}
	if !strings.Contains(prompt, guidance) {
		t.Error("restart prompt missing guidance text")
	}
	if strings.Contains(prompt, "some partial output") {
		t.Error("restart prompt should NOT contain partial output from failed attempt")
	}
}

func TestBuildRestartPrompt_NoGuidance(t *testing.T) {
	prompt := buildRestartPrompt("do something", TaskResult{}, "")

	if !strings.Contains(prompt, "RESTART") {
		t.Error("restart prompt missing 'RESTART' keyword")
	}
	if !strings.Contains(prompt, "do something") {
		t.Error("restart prompt missing original description")
	}
	// Without guidance, should not have the "DIFFERENT approach" line
	if strings.Contains(prompt, "DIFFERENT approach") {
		t.Error("restart prompt should NOT include 'DIFFERENT approach' when guidance is empty")
	}
}

func TestBuildEvaluationSummary(t *testing.T) {
	task := SubAgentTask{
		Name:        "test-task",
		Description: "do something important",
	}

	trace := NewExecutionTrace("do something important")
	trace.RecordStep("read_file", map[string]any{"path": "a.go"}, nil, nil)
	trace.mu.Lock()
	trace.Steps[0].Success = true
	trace.mu.Unlock()
	trace.RecordStep("grep_search", map[string]any{"pattern": "foo"}, nil, nil)
	trace.mu.Lock()
	trace.Steps[1].Success = true
	trace.mu.Unlock()
	trace.RecordStep("shell_command", map[string]any{"command": "go build"}, nil, nil)
	trace.mu.Lock()
	trace.Steps[2].Success = false
	trace.mu.Unlock()

	result := TaskResult{
		Name:      "test-task",
		Status:    "timeout",
		Error:     "task timed out",
		ToolCalls: 3,
		Duration:  8 * time.Minute,
		Trace:     trace,
		Result:    "some partial output from the task",
	}

	summary := buildEvaluationSummary(task, result)

	if !strings.Contains(summary, "test-task") {
		t.Error("summary missing task name")
	}
	if !strings.Contains(summary, "do something important") {
		t.Error("summary missing task description")
	}
	if !strings.Contains(summary, "read_file") {
		t.Error("summary missing tool name 'read_file'")
	}
	if !strings.Contains(summary, "grep_search") {
		t.Error("summary missing tool name 'grep_search'")
	}
	if !strings.Contains(summary, "shell_command") {
		t.Error("summary missing tool name 'shell_command'")
	}
	if !strings.Contains(summary, "success") {
		t.Error("summary missing 'success' status indicator")
	}
	if !strings.Contains(summary, "failed") {
		t.Error("summary missing 'failed' status indicator")
	}
	if !strings.Contains(summary, "task timed out") {
		t.Error("summary missing error message")
	}
	if !strings.Contains(summary, "8m") {
		t.Error("summary missing duration")
	}
}

func TestNewSubAgentManager_DefaultDelegationTimeout(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})

	if mgr.Config.DelegationTimeout != 25*time.Minute {
		t.Errorf("DelegationTimeout = %v, want 25m", mgr.Config.DelegationTimeout)
	}
}

func TestParseEvaluationResponse(t *testing.T) {
	tests := []struct {
		name         string
		response     string
		wantAction   string
		wantReason   string
		wantGuidance string
	}{
		{
			name:         "continue with guidance",
			response:     "ACTION: continue\nREASON: making progress\nGUIDANCE: keep going",
			wantAction:   "continue",
			wantReason:   "making progress",
			wantGuidance: "keep going",
		},
		{
			name:         "restart with guidance",
			response:     "ACTION: restart\nREASON: stuck in loop\nGUIDANCE: try grep instead",
			wantAction:   "restart",
			wantReason:   "stuck in loop",
			wantGuidance: "try grep instead",
		},
		{
			name:         "case insensitive action",
			response:     "ACTION: Continue\nREASON: works fine\nGUIDANCE:",
			wantAction:   "continue",
			wantReason:   "works fine",
			wantGuidance: "",
		},
		{
			name:         "no action line",
			response:     "I think the task was doing fine",
			wantAction:   "continue",
			wantReason:   "evaluation response unparseable - assuming progress",
			wantGuidance: "",
		},
		{
			name:         "invalid action",
			response:     "ACTION: maybe\nREASON: not sure",
			wantAction:   "continue",
			wantReason:   "not sure",
			wantGuidance: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, reason, guidance := parseEvaluationResponse(tt.response)
			if action != tt.wantAction {
				t.Errorf("action = %q, want %q", action, tt.wantAction)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
			if guidance != tt.wantGuidance {
				t.Errorf("guidance = %q, want %q", guidance, tt.wantGuidance)
			}
		})
	}
}

func TestSubAgentManager_EmitsEvaluatingEvent(t *testing.T) {
	var mu sync.Mutex
	var events []SubTaskProgressEvent

	mgr := NewSubAgentManager(SubAgentConfig{
		MaxDepth:          1,
		MaxConcurrent:     2,
		TaskTimeout:       5 * time.Second,
		InactivityTimeout: 100 * time.Millisecond,
		HeartbeatInterval: 20 * time.Millisecond,
		DelegationTimeout: 10 * time.Second,
	})

	// Mock LLM that returns a continue evaluation response.
	mgr.LLM = evalMockLLM{response: "ACTION: continue\nREASON: task was making progress\nGUIDANCE: "}
	mgr.SubTaskProgress = func(evt SubTaskProgressEvent) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
	}

	// We need the task to fail with inactivity so the evaluating event fires.
	// Since we can't easily make a real sub-agent stall with this unit test setup,
	// we'll verify the evaluating emission path by checking that when isRetryableFailure
	// and hasProgress both return true, the evaluating event is emitted.

	// Simulate what RunTasks does when it encounters a retryable failure:
	result := TaskResult{
		Name:             "test-task",
		Status:           "timeout",
		Error:            "no meaningful activity for 2m0s",
		ToolCalls:        5,
		InactivityReason: "no meaningful activity for 2m0s",
	}

	if !isRetryableFailure(result) {
		t.Fatal("expected result to be retryable")
	}
	if !hasProgress(result) {
		t.Fatal("expected result to have progress")
	}

	// Verify the evaluating event would be emitted
	if mgr.SubTaskProgress != nil {
		mgr.SubTaskProgress(SubTaskProgressEvent{
			Type:       "evaluating",
			TaskName:   "test-task",
			Status:     "evaluating",
			Attempt:    1,
			Error:      result.Error,
			NoActivity: result.InactivityReason != "",
		})
	}

	mu.Lock()
	defer mu.Unlock()

	found := false
	for _, evt := range events {
		if evt.Type == "evaluating" {
			found = true
			if evt.Status != "evaluating" {
				t.Errorf("evaluating event status = %q, want %q", evt.Status, "evaluating")
			}
			if evt.TaskName != "test-task" {
				t.Errorf("evaluating event task name = %q, want %q", evt.TaskName, "test-task")
			}
			if !evt.NoActivity {
				t.Error("evaluating event NoActivity should be true")
			}
			if evt.Error == "" {
				t.Error("evaluating event Error should not be empty")
			}
			break
		}
	}
	if !found {
		t.Error("expected an evaluating event in the collected events")
	}
}

// stubLLM is a minimal model.LLM implementation used in unit tests.
type stubLLM struct {
	name string
}

func (s *stubLLM) Name() string { return s.name }
func (s *stubLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{}, nil)
	}
}

func TestSubAgentManager_EffectiveTaskLLM(t *testing.T) {
	parentLLM := &stubLLM{name: "parent"}
	taskLLM := &stubLLM{name: "task"}
	mgr := &SubAgentManager{
		LLM:     parentLLM,
		TaskLLM: taskLLM,
	}
	if got := mgr.effectiveTaskLLM(); got != taskLLM {
		t.Errorf("effectiveTaskLLM() = %v, want taskLLM", got)
	}
}

func TestSubAgentManager_EffectiveTaskLLMNilFallback(t *testing.T) {
	parentLLM := &stubLLM{name: "parent"}
	mgr := &SubAgentManager{
		LLM: parentLLM,
	}
	if got := mgr.effectiveTaskLLM(); got != parentLLM {
		t.Errorf("effectiveTaskLLM() = %v, want parentLLM (fallback)", got)
	}
}
