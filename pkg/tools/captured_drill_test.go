package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

func capturedFixtureClassifier(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}

func TestCapturedDrillStaticSnapshot(t *testing.T) {
	for _, mode := range []string{"static", "dynamic", "retry", "browser", "flow", "malformed", "missing-classifier"} {
		t.Run(mode, func(t *testing.T) {
			fs := newMemFlowStore()
			if err := fs.SaveFlow(t.Context(), "fixture", "name: fixture\ntype: drill_suite\nsuite_config: {}\n"); err != nil {
				t.Fatal(err)
			}
			child := "name: step\ntype: drill\nsuite: fixture\ndrill_config: {tags: [offline]}\nnodes:\n  - name: one\n    type: tool\n    args: {tool: filter_json, json_data: 'exact', fields_to_extract: [evidence]}\n"
			switch mode {
			case "dynamic":
				child = "name: step\ntype: drill\nsuite: fixture\nnodes:\n  - name: one\n    type: tool\n    args: {tool: filter_json, json_data: '{{dynamic}}'}\n"
			case "retry":
				child = "name: step\ntype: drill\nsuite: fixture\ndrill_config: {max_retries: 1}\nnodes:\n  - name: one\n    type: tool\n    args: {tool: filter_json}\n"
			case "browser":
				child = "name: step\ntype: drill\nsuite: fixture\nnodes:\n  - name: one\n    type: tool\n    args: {tool: browser_navigate, url: 'http://localhost'}\n"
			case "flow":
				child = "name: step\ntype: drill\nsuite: fixture\nflow: [{from: START, to: one}]\nnodes:\n  - name: one\n    type: tool\n    args: {tool: filter_json}\n"
			case "malformed":
				child = "name: step\ntype: drill\nsuite: fixture\nnodes: broken\n"
			}
			if err := fs.SaveFlow(t.Context(), "step", child); err != nil {
				t.Fatal(err)
			}
			describe := capturedFixtureClassifier
			if mode == "missing-classifier" {
				describe = nil
			}
			args := json.RawMessage(`{"suite_name":"fixture"}`)
			if mode == "static" {
				args = json.RawMessage(`{"suite_name":"fixture","tag":"missing, offline"}`)
			}
			snapshot, err := CaptureDrill(t.Context(), fs, args, describe)
			if mode != "static" {
				if err == nil {
					t.Fatal("nonstatic suite captured")
				}
				return
			}
			if err != nil || len(snapshot.Children) != 1 {
				t.Fatalf("capture: %+v %v", snapshot, err)
			}
			raw, _ := json.Marshal(snapshot)
			parent := execution.ActionDescriptor{Tool: "run_drill", TargetSnapshot: raw, TargetFingerprint: snapshot.Fingerprint()}
			if _, err := DecodeCapturedDrill(parent); err != nil {
				t.Fatal(err)
			}
			parent.TargetFingerprint = "altered"
			if _, err := DecodeCapturedDrill(parent); err == nil {
				t.Fatal("altered fingerprint accepted")
			}
			// Change store after capture: ordinary handler must still run the detached nodes.
			if err := fs.SaveFlow(t.Context(), "step", "name: step\ntype: drill\nsuite: fixture\nnodes:\n  - name: altered\n    type: tool\n    args: {tool: shell_command, command: changed}\n"); err != nil {
				t.Fatal(err)
			}
			calls := 0
			deps := &ScopedDrillExecution{CapturedSuite: snapshot.Suite, NewExecutor: func(tool.Context) (drill.ToolExecutor, error) {
				return scopedDrillExecutorFunc(func(_ context.Context, name string, args map[string]any) (any, error) {
					calls++
					if name != "filter_json" || args["json_data"] != "exact" {
						t.Fatalf("mutable store executed: %s %+v", name, args)
					}
					return map[string]any{"evidence": "exact"}, nil
				}), nil
			}}
			ordinary, err := NewRunDrillTool(nil, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx := &mockToolCtx{Context: WithDrillExecutionContext(store.WithTeamFlowStore(t.Context(), fs), deps)}
			got, err := ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"suite_name": "fixture"})
			if err != nil || got["status"] != "passed" || calls != 1 {
				t.Fatalf("pinned execution: %+v %v calls=%d", got, err, calls)
			}
			if snapshot.Suite.Tests[0].Config.Nodes[0].Name != "one" {
				t.Fatal("handler mutated captured evidence")
			}
		})
	}
}
