package agent

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

func TestExecuteToolBridge_DiscoveryNeverExecutes(t *testing.T) {
	for _, name := range []string{"describe_tools", "search_tools"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			inner, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "test"}, func(_ tool.Context, args map[string]any) (map[string]any, error) { calls++; return args, nil })
			if err != nil {
				t.Fatal(err)
			}
			idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: []tool.Tool{inner}})
			bridge, err := NewProgressiveToolBridge(idx)
			if err != nil {
				t.Fatal(err)
			}
			ctx := &contextToolContext{minimalReadonlyContext{Context: context.Background()}}
			_, err = bridge[1].(runnableDeferredTool).Run(ctx, map[string]any{"name": name, "arguments": map[string]any{"_caller": "chat", "name": "write_file"}})
			if err == nil || calls != 0 {
				t.Fatalf("discovery executed: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestEffectiveToolCall_MatchesExecution(t *testing.T) {
	inner, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "test"}, func(_ tool.Context, args map[string]any) (map[string]any, error) { return args, nil })
	if err != nil {
		t.Fatal(err)
	}
	idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: []tool.Tool{inner}})
	bridge, err := NewProgressiveToolBridge(idx)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &contextToolContext{minimalReadonlyContext{Context: context.Background()}}
	raw := map[string]any{"name": "execute_tool", "arguments": map[string]any{"name": "write_file", "arguments": map[string]any{"file_path": "/tmp/example", "content": "hello"}}}
	logical, args := (&ChatAgent{}).effectiveToolCall(ctx, bridge[1], raw)
	if logical.Name() != "write_file" || args["file_path"] != "/tmp/example" {
		t.Fatalf("logical=%s args=%v", logical.Name(), args)
	}
	output, err := bridge[1].(runnableDeferredTool).Run(ctx, raw)
	if err != nil || output["file_path"] != args["file_path"] {
		t.Fatalf("output=%v err=%v", output, err)
	}
}

func TestExecuteToolBridge_RejectsMalformedEnvelope(t *testing.T) {
	inner, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "test"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
		t.Error("invalid call executed")
		return args, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: []tool.Tool{inner}})
	bridge, err := NewProgressiveToolBridge(idx)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []any{nil, "{broken", "null", []any{}, 7} {
		_, err := bridge[1].(runnableDeferredTool).Run(&contextToolContext{minimalReadonlyContext{Context: context.Background()}}, map[string]any{"name": "write_file", "arguments": args})
		if err == nil || !strings.Contains(err.Error(), "arguments") {
			t.Errorf("args=%v err=%v", args, err)
		}
	}
}

func TestDecodeExecuteToolArgs_StringArgumentsShareCallbackMutations(t *testing.T) {
	raw := map[string]any{"name": "write_file", "arguments": `{"file_path":"/tmp/example","content":"placeholder"}`}
	first, err := decodeExecuteToolArgs(raw)
	if err != nil {
		t.Fatal(err)
	}
	first.Arguments["content"] = "substituted"
	second, err := decodeExecuteToolArgs(raw)
	if err != nil || second.Arguments["content"] != "substituted" {
		t.Fatalf("callback changes lost: args=%v err=%v", second.Arguments, err)
	}
}
