package agent

import (
	"context"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// TestPinnedRequestGroups_InjectsRequestScopedTools verifies that
// DynamicToolInjectionCallback injects tools from request-scoped groups
// (e.g., A2A tools) when those groups are listed in PinnedToolGroups,
// even though they are NOT in the singleton ToolIndex.
func TestPinnedRequestGroups_InjectsRequestScopedTools(t *testing.T) {
	// Create an empty ToolIndex (no groups registered).
	idx := newTestToolIndex(t, testEmbeddingFunc())

	// Create a ChatAgent with the empty ToolIndex.
	ca := &ChatAgent{
		ToolIndex: idx,
		DebugMode: true,
	}

	// Build a mock tool to place in the request-scoped "a2a" group.
	a2aTools := mockTools("a2a_test_agent_find_devices")

	// Set up request-scoped groups with the "a2a" group containing our mock tool.
	ctx := context.Background()
	ctx = WithRequestMCPGroups(ctx, map[string]*ToolGroup{
		"a2a": {
			Name:        "a2a",
			Description: "Remote A2A agent skills",
			Tools:       a2aTools,
		},
	})

	// Set PinnedToolGroups to include "a2a" via PromptOverrides.
	ctx = WithPromptOverrides(ctx, &PromptOverrides{
		PinnedToolGroups: []string{"a2a"},
	})

	// Build the DynamicToolInjectionCallback.
	cb := ca.DynamicToolInjectionCallback()

	// Create a minimal LLMRequest.
	req := &model.LLMRequest{
		Tools:  make(map[string]any),
		Config: &genai.GenerateContentConfig{},
	}

	// Create a minimal CallbackContext that wraps our context.
	cbCtx := &minimalReadonlyContext{Context: ctx}

	// Call the callback — this should inject the A2A tool on the first call.
	resp, err := cb(cbCtx, req)
	if err != nil {
		t.Fatalf("DynamicToolInjectionCallback returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("DynamicToolInjectionCallback returned non-nil response (should not short-circuit): %v", resp)
	}

	// Verify the tool was injected into req.Tools.
	if _, ok := req.Tools["a2a_test_agent_find_devices"]; !ok {
		t.Errorf("expected tool 'a2a_test_agent_find_devices' to be injected into req.Tools, got keys: %v", reqToolKeys(req.Tools))
	}
}

// TestPinnedRequestGroups_FallsBackToToolIndex verifies that PinnedToolGroups
// still works for groups that ARE in the ToolIndex (regression check).
func TestPinnedRequestGroups_FallsBackToToolIndex(t *testing.T) {
	// Create a ToolIndex with a group registered.
	idx := syncTestToolIndex(t, &ToolGroup{
		Name:  "test_group",
		Tools: mockTools("indexed_tool"),
	})

	ca := &ChatAgent{
		ToolIndex: idx,
		DebugMode: true,
	}

	// Pin "test_group" via PromptOverrides (no request-scoped groups).
	ctx := context.Background()
	ctx = WithPromptOverrides(ctx, &PromptOverrides{
		PinnedToolGroups: []string{"test_group"},
	})

	cb := ca.DynamicToolInjectionCallback()

	req := &model.LLMRequest{
		Tools:  make(map[string]any),
		Config: &genai.GenerateContentConfig{},
	}

	cbCtx := &minimalReadonlyContext{Context: ctx}

	resp, err := cb(cbCtx, req)
	if err != nil {
		t.Fatalf("DynamicToolInjectionCallback returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("DynamicToolInjectionCallback returned non-nil response: %v", resp)
	}

	// Verify the indexed tool was injected.
	if _, ok := req.Tools["indexed_tool"]; !ok {
		t.Errorf("expected tool 'indexed_tool' to be injected into req.Tools, got keys: %v", reqToolKeys(req.Tools))
	}
}

// TestPinnedRequestGroups_NoDoubleInject verifies that when a group is in
// BOTH the ToolIndex and request-scoped groups, tools are not double-injected.
func TestPinnedRequestGroups_NoDoubleInject(t *testing.T) {
	// Create a ToolIndex with the "a2a" group registered.
	idx := syncTestToolIndex(t, &ToolGroup{
		Name:  "a2a",
		Tools: mockTools("a2a_tool_from_index"),
	})

	ca := &ChatAgent{
		ToolIndex: idx,
		DebugMode: true,
	}

	// Also put a different tool in request-scoped "a2a" group.
	ctx := context.Background()
	ctx = WithRequestMCPGroups(ctx, map[string]*ToolGroup{
		"a2a": {
			Name:  "a2a",
			Tools: []tool.Tool{mockTool{name: "a2a_tool_from_request"}},
		},
	})
	ctx = WithPromptOverrides(ctx, &PromptOverrides{
		PinnedToolGroups: []string{"a2a"},
	})

	cb := ca.DynamicToolInjectionCallback()

	req := &model.LLMRequest{
		Tools:  make(map[string]any),
		Config: &genai.GenerateContentConfig{},
	}
	cbCtx := &minimalReadonlyContext{Context: ctx}

	_, err := cb(cbCtx, req)
	if err != nil {
		t.Fatalf("DynamicToolInjectionCallback returned error: %v", err)
	}

	// When ToolIndex has entries, request-scoped fallback should NOT fire
	// (len(entries) != 0). Only the index tool should be injected.
	if _, ok := req.Tools["a2a_tool_from_index"]; !ok {
		t.Errorf("expected 'a2a_tool_from_index' from ToolIndex to be injected")
	}
	if _, ok := req.Tools["a2a_tool_from_request"]; ok {
		t.Errorf("did NOT expect 'a2a_tool_from_request' from request-scoped group (ToolIndex took precedence)")
	}
}

// reqToolKeys returns the keys from a map for error messages.
func reqToolKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
