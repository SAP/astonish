package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

func TestProgressiveToolBridge_RequestScopedMCPAndFirstPartyPrecedence(t *testing.T) {
	firstParty, err := functiontool.New(functiontool.Config{Name: "shared", Description: "first party"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"source": "first-party"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mcpTool, err := functiontool.New(functiontool.Config{Name: "shared", Description: "mcp"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"source": "mcp"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: []tool.Tool{firstParty}})
	ctx := WithRequestMCPGroups(context.Background(), map[string]*ToolGroup{
		"mcp:test": {Name: "mcp:test", Tools: []tool.Tool{mcpTool}},
	})
	resolved, group, err := (deferredToolResolver{index: idx}).resolve(&minimalReadonlyContext{Context: ctx}, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != firstParty || group != "core" {
		t.Fatalf("resolved (%v, %q), want first-party core tool", resolved, group)
	}
}

func TestProgressiveToolBridge_RequestScopedExecutionAndDisabledCheck(t *testing.T) {
	mcpTool, err := functiontool.New(functiontool.Config{Name: "remote_action", Description: "remote"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
		return map[string]any{"value": args["value"]}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestMCPGroups(context.Background(), map[string]*ToolGroup{
		"mcp:test": {Name: "mcp:test", Tools: []tool.Tool{mcpTool}},
	})
	bridge, err := NewProgressiveToolBridge(nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := bridge[1].(runnableDeferredTool)
	result, err := runner.Run(&contextToolContext{minimalReadonlyContext{Context: ctx}}, map[string]any{
		"name": "remote_action", "arguments": map[string]any{"value": "ok"},
	})
	if err != nil || result["value"] != "ok" {
		t.Fatalf("execute request MCP tool = %#v, %v", result, err)
	}

	disabledCtx := store.WithDisabledTools(ctx, []string{"remote_action"})
	_, err = runner.Run(&contextToolContext{minimalReadonlyContext{Context: disabledCtx}}, map[string]any{
		"name": "remote_action", "arguments": map[string]any{},
	})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled execute error = %v, want disabled error", err)
	}
}

func TestRequestGroupsMergeAndRejectAmbiguousBareNames(t *testing.T) {
	mcpTool, err := functiontool.New(functiontool.Config{Name: "shared", Description: "mcp"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"source": "mcp"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a2aTool, err := functiontool.New(functiontool.Config{Name: "shared", Description: "a2a"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"source": "a2a"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestMCPGroups(context.Background(), map[string]*ToolGroup{
		"mcp:test": {Name: "mcp:test", Tools: []tool.Tool{mcpTool}},
	})
	ctx = WithRequestMCPGroups(ctx, map[string]*ToolGroup{
		"a2a": {Name: "a2a", Tools: []tool.Tool{a2aTool}},
	})
	if len(RequestMCPGroupsFromContext(ctx)) != 2 {
		t.Fatalf("request groups = %#v, want merged MCP and A2A groups", RequestMCPGroupsFromContext(ctx))
	}

	resolver := deferredToolResolver{}
	if _, _, err := resolver.resolve(&minimalReadonlyContext{Context: ctx}, "shared"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("bare collision error = %v, want ambiguity", err)
	}
	resolved, group, err := resolver.resolve(&minimalReadonlyContext{Context: ctx}, "mcp:test/shared")
	if err != nil || resolved != mcpTool || group != "mcp:test" {
		t.Fatalf("qualified resolve = (%v, %q, %v), want MCP tool", resolved, group, err)
	}

	bridge, err := NewProgressiveToolBridge(nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := bridge[1].(runnableDeferredTool)
	result, err := runner.Run(&contextToolContext{minimalReadonlyContext{Context: ctx}}, map[string]any{
		"name": "mcp:test/shared", "arguments": map[string]any{},
	})
	if err != nil || result["source"] != "mcp" {
		t.Fatalf("qualified execute = %#v, %v", result, err)
	}
}

type testMCPServerStore struct {
	servers map[string]*store.MCPServer
}

func (s *testMCPServerStore) List(context.Context) ([]store.MCPServer, error) { return nil, nil }
func (s *testMCPServerStore) Get(_ context.Context, name string) (*store.MCPServer, error) {
	return s.servers[name], nil
}
func (s *testMCPServerStore) Save(context.Context, *store.MCPServer) error { return nil }
func (s *testMCPServerStore) Delete(context.Context, string) error         { return nil }
func (s *testMCPServerStore) UpdateCachedTools(context.Context, string, json.RawMessage) error {
	return nil
}

func TestMCPServerAuthorizationUsesMostSpecificOverride(t *testing.T) {
	enabled, disabled := true, false
	server := func(value *bool) *testMCPServerStore {
		return &testMCPServerStore{servers: map[string]*store.MCPServer{
			"perplexity": {Name: "perplexity", Enabled: value},
		}}
	}
	tests := []struct {
		name   string
		stores *store.MCPServerStores
		want   bool
	}{
		{"team disable overrides enabled parents", &store.MCPServerStores{Platform: server(&enabled), Org: server(&enabled), Team: server(&disabled)}, false},
		{"org disable overrides enabled platform", &store.MCPServerStores{Platform: server(&enabled), Org: server(&disabled)}, false},
		{"team enable overrides disabled parents", &store.MCPServerStores{Platform: server(&disabled), Org: server(&disabled), Team: server(&enabled)}, true},
		{"scoped disable overrides installed standard server", &store.MCPServerStores{Team: server(&disabled)}, false},
		{"installed standard server allowed without scoped declaration", &store.MCPServerStores{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := store.WithMCPServerStores(context.Background(), tt.stores)
			if got := isMCPServerAccessible(ctx, "perplexity"); got != tt.want {
				t.Fatalf("isMCPServerAccessible = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProgressiveToolBridge_FixedDeclarations(t *testing.T) {
	idx := syncTestToolIndex(t, &ToolGroup{Name: "browser", Tools: mockTools("browser_click")})
	bridge, err := NewProgressiveToolBridge(idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bridge) != 2 || bridge[0].Name() != "describe_tools" || bridge[1].Name() != "execute_tool" {
		t.Fatalf("bridge declarations = %v, want [describe_tools execute_tool]", []string{bridge[0].Name(), bridge[1].Name()})
	}
	ca := &ChatAgent{ToolIndex: idx}
	resolved, args := ca.effectiveToolCall(&contextToolContext{minimalReadonlyContext{Context: context.Background()}}, bridge[1], map[string]any{
		"name": "browser_click", "arguments": map[string]any{"selector": "button"},
	})
	if resolved.Name() != "browser_click" || args["selector"] != "button" {
		t.Fatalf("effective call = %q %#v", resolved.Name(), args)
	}
}

type contextToolContext struct{ minimalReadonlyContext }
