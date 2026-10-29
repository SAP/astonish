package agent

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

func TestDecodeExecuteToolArgsUnwrapsOneAccidentalArgumentsEnvelope(t *testing.T) {
	raw := map[string]any{
		"arguments": map[string]any{
			"name": "announce_plan",
			"arguments": map[string]any{
				"goal": "Documentary script",
			},
		},
	}

	got, err := decodeExecuteToolArgs(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "announce_plan" || got.Arguments["goal"] != "Documentary script" {
		t.Fatalf("decoded=%+v", got)
	}
}

func TestDecodeExecuteToolArgsDoesNotRecursivelyUnwrap(t *testing.T) {
	raw := map[string]any{
		"arguments": map[string]any{
			"arguments": map[string]any{
				"name":      "announce_plan",
				"arguments": map[string]any{},
			},
		},
	}

	got, err := decodeExecuteToolArgs(raw)
	if err == nil || got.Name != "" {
		t.Fatalf("recursive wrapper must be rejected: decoded=%+v err=%v", got, err)
	}
}

// Regression for session a4602e66: providers sometimes serialize the inner
// `arguments` object as a JSON *string* and omit the outer `name`, wrapping the
// real {name,arguments} one level deep. The decoder must recover the intended
// tool name and arguments rather than yield an empty name (which previously
// surfaced the misleading "tool name is required" / "execute_tool not found").
func TestDecodeExecuteToolArgsParsesStringArgumentsWrapper(t *testing.T) {
	raw := map[string]any{
		"arguments": `{"name":"edit_file","arguments":{"path":"/root/x.md","old_string":"a","new_string":"b"}}`,
	}
	got, err := decodeExecuteToolArgs(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "edit_file" {
		t.Fatalf("Name=%q, want edit_file (decoded=%+v)", got.Name, got)
	}
	if got.Arguments["path"] != "/root/x.md" || got.Arguments["old_string"] != "a" || got.Arguments["new_string"] != "b" {
		t.Fatalf("arguments not recovered: %+v", got.Arguments)
	}
}

// Well-formed envelope {name, arguments-object} must decode verbatim.
func TestDecodeExecuteToolArgsWellFormedEnvelope(t *testing.T) {
	raw := map[string]any{
		"name": "edit_file",
		"arguments": map[string]any{
			"path": "/root/x.md", "old_string": "a", "new_string": "b",
		},
	}
	got, err := decodeExecuteToolArgs(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "edit_file" || got.Arguments["path"] != "/root/x.md" {
		t.Fatalf("decoded=%+v", got)
	}
}

// Regression for session a4602e66 call #0: the provider fragmented the call —
// `arguments` was a truncated JSON string with no recoverable inner name. The
// bridge must return an actionable error that names the required envelope
// shape, never the misleading "execute_tool was not found in the available
// catalog" (which implied the bridge itself was missing).
func TestExecuteToolBridgeEmptyNameReturnsActionableError(t *testing.T) {
	bridge, err := NewProgressiveToolBridge(nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := bridge[1].(runnableDeferredTool)
	// Unterminated/fragmented arguments string, no top-level name — exactly the
	// shape observed in the failing session.
	_, runErr := runner.Run(&contextToolContext{minimalReadonlyContext{Context: context.Background()}}, map[string]any{
		"arguments":  `{"path": "/root/x.md", "old_string": "a`,
		"new_string": "b",
	})
	if runErr == nil {
		t.Fatal("expected an error for a nameless execute_tool envelope")
	}
	if strings.Contains(runErr.Error(), "execute_tool") && strings.Contains(runErr.Error(), "not found in the available catalog") {
		t.Fatalf("error must not blame execute_tool itself: %v", runErr)
	}
	if !strings.Contains(runErr.Error(), "name") || !strings.Contains(runErr.Error(), "arguments") {
		t.Fatalf("error should name the required envelope fields: %v", runErr)
	}
}

// End-to-end regression for session a4602e66: a WELL-FORMED execute_tool
// envelope ({"name":"edit_file","arguments":{...}}) must dispatch to and run the
// inner deferred tool. The failing session returned
//
//	tool "execute_tool" was not found in the available catalog
//
// for exactly this shape, which can only happen if the bridge resolves its own
// name instead of the inner tool. This drives the real executeToolBridge.Run
// against an index containing a runnable inner tool and asserts the inner tool
// actually executed.
func TestExecuteToolBridgeRunsWellFormedInnerTool(t *testing.T) {
	var ran bool
	var gotArgs map[string]any
	inner, err := functiontool.New(functiontool.Config{
		Name:        "edit_file",
		Description: "Edit a file by finding and replacing text.",
	}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
		ran = true
		gotArgs = args
		return map[string]any{"success": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: []tool.Tool{inner}})
	bridge, err := NewProgressiveToolBridge(idx)
	if err != nil {
		t.Fatal(err)
	}
	runner := bridge[1].(runnableDeferredTool)

	// Exactly the shape from the failing trace (msg[99]): top-level name plus a
	// nested arguments object.
	out, runErr := runner.Run(&contextToolContext{minimalReadonlyContext{Context: context.Background()}}, map[string]any{
		"name": "edit_file",
		"arguments": map[string]any{
			"path":       "/root/elon-musk-treatment.md",
			"old_string": "a",
			"new_string": "b",
		},
	})
	if runErr != nil {
		t.Fatalf("execute_tool(edit_file) failed: %v", runErr)
	}
	if !ran {
		t.Fatal("inner edit_file tool was never executed")
	}
	if out["success"] != true {
		t.Fatalf("unexpected output: %#v", out)
	}
	if gotArgs["path"] != "/root/elon-musk-treatment.md" || gotArgs["new_string"] != "b" {
		t.Fatalf("inner tool received wrong args: %#v", gotArgs)
	}
}

// The bridge must never surface the misleading "was not found in the available
// catalog" error for its OWN fixed names. If resolve is ever handed
// execute_tool / describe_tools / search_tools (a sign the inner name was not
// unwrapped), the message must tell the model to supply the inner tool name
// rather than implying the dispatcher itself is missing.
func TestResolverRejectsBridgeOwnNamesActionably(t *testing.T) {
	idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: mockTools("edit_file")})
	r := deferredToolResolver{index: idx}
	for _, name := range []string{"execute_tool", "describe_tools", "search_tools"} {
		_, _, err := r.resolve(&contextToolContext{minimalReadonlyContext{Context: context.Background()}}, name)
		if err == nil {
			t.Fatalf("resolve(%q) should error", name)
		}
		if strings.Contains(err.Error(), "not found in the available catalog") {
			t.Fatalf("resolve(%q) must not blame the catalog: %v", name, err)
		}
		if !strings.Contains(err.Error(), "name") || !strings.Contains(err.Error(), "arguments") {
			t.Fatalf("resolve(%q) should guide toward the inner tool name: %v", name, err)
		}
	}
}

// Defensive: a model that nests the dispatcher inside itself —
// execute_tool{name:"execute_tool", arguments:{name:"edit_file", arguments:{…}}}
// — must still run the real inner tool rather than failing with
// "execute_tool not found".
func TestExecuteToolBridgeUnwrapsSelfNestedEnvelope(t *testing.T) {
	var ran bool
	inner, err := functiontool.New(functiontool.Config{
		Name:        "edit_file",
		Description: "Edit a file by finding and replacing text.",
	}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
		ran = true
		return map[string]any{"success": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: []tool.Tool{inner}})
	bridge, err := NewProgressiveToolBridge(idx)
	if err != nil {
		t.Fatal(err)
	}
	runner := bridge[1].(runnableDeferredTool)
	_, runErr := runner.Run(&contextToolContext{minimalReadonlyContext{Context: context.Background()}}, map[string]any{
		"name": "execute_tool",
		"arguments": map[string]any{
			"name":      "edit_file",
			"arguments": map[string]any{"path": "/root/x.md", "old_string": "a", "new_string": "b"},
		},
	})
	if runErr != nil {
		t.Fatalf("self-nested execute_tool failed: %v", runErr)
	}
	if !ran {
		t.Fatal("inner edit_file tool was never executed")
	}
}
