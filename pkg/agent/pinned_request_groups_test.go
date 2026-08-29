package agent

import (
	"context"
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
