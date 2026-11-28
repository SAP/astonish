package launcher

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

type drillInvocationKey struct{}

type drillInvocationProbe struct {
	botRunnableTool
	run func(tool.Context, any) (map[string]any, error)
}

func (p drillInvocationProbe) Run(ctx tool.Context, args any) (map[string]any, error) {
	return p.run(ctx, args)
}

func TestBotDrillUsesSharedHandlerInvocation(t *testing.T) {
	ordinary, err := tools.NewRunDrillTool(nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := store.WithTeamSlug(context.Background(), "owner-team")
	base = store.WithTeamFlowStore(base, drillFlowStore{data: map[string]string{
		"suite":  "name: suite\ntype: drill_suite\nsuite_config: {}\n",
		"first":  "name: first\ntype: drill\nsuite: suite\nnodes:\n  - name: one\n    type: tool\n    args: {tool: read_file, path: fixture}\n",
		"second": "name: second\ntype: drill\nsuite: suite\nnodes:\n  - name: two\n    type: tool\n    args: {tool: read_file, path: fixture}\n",
	}})
	calls := 0
	target := drillInvocationProbe{run: func(ctx tool.Context, _ any) (map[string]any, error) {
		calls++
		if ctx.Value(drillInvocationKey{}) != "shared invocation" {
			t.Error("nested executor discarded the invocation provided by the shared drill handler")
		}
		return map[string]any{"content": "fixture"}, nil
	}}
	shared := ordinary.(botRunnableTool)
	probe := drillInvocationProbe{botRunnableTool: shared, run: func(ctx tool.Context, args any) (map[string]any, error) {
		// Model a shared invocation wrapper adding request-local context before execution.
		bound := botDrillExecutionContext{Context: ctx, scoped: context.WithValue(ctx, drillInvocationKey{}, "shared invocation")}
		return shared.Run(bound, args)
	}}
	adapter := &botDrillTool{botRunnableTool: probe, tools: map[string]botRunnableTool{"read_file": target}}
	out, err := adapter.run(botFactoryHandoffContext{base: base, session: "drill-child"}, map[string]any{"suite_name": "suite"}, credentials.NewRedactor())
	if err != nil || out["status"] != "passed" || calls != 2 {
		t.Fatalf("out=%v err=%v calls=%d", out, err, calls)
	}
}
