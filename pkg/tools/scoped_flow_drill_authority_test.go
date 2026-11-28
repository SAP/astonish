package tools

import (
	"context"
	"strings"
	"testing"

	adrill "github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

func flowDrillPrincipal(t *testing.T, local bool) context.Context {
	t.Helper()
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: "owner", OrgSlug: "org", TeamSlug: "team", Authenticated: true}
	if local {
		p.Kind, p.Authentication, p.Surface = execution.PrincipalKindLocal, execution.AuthMethodLocalOS, execution.SurfaceCode
	}
	ctx, err := execution.WithPrincipal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestScopedFlowDrillAuthorityMissingBindings(t *testing.T) {
	old := flowRunnerAccessVar
	defer func() { flowRunnerAccessVar = old }()
	global := &mockFlowRunnerAccess{result: &FlowRunResult{Status: "completed", Output: "ambient"}}
	flowRunnerAccessVar = global
	fs := newMemFlowStore()
	if err := fs.SaveFlow(context.Background(), "fixture", "name: fixture\nnodes:\n  - name: step\n    type: tool\n    tools_selection: [read_file]\nflow:\n  - from: START\n    to: step\n  - from: step\n    to: END\n"); err != nil {
		t.Fatal(err)
	}
	ctx := &mockToolCtx{Context: store.WithFlowStore(flowDrillPrincipal(t, false), fs)}
	got, err := runFlow(ctx, RunFlowArgs{FlowName: "fixture"})
	if err == nil || !strings.Contains(err.Error(), "scoped flow execution") || global.lastKey != "" {
		t.Errorf("missing flow binding used ambient authority: %+v err=%v runner=%+v", got, err, global)
	}
	// Empty name ensures the pre-fix path cannot execute a host tool or write reports.
	drill, err := executeRunDrill(ctx, &runDrillDeps{}, RunDrillArgs{})
	if err == nil || !strings.Contains(err.Error(), "scoped drill execution") {
		t.Errorf("missing drill binding not rejected: %+v err=%v", drill, err)
	}
}

func TestScopedFlowDrillAuthorityTeamStoreRequired(t *testing.T) {
	fs := newMemFlowStore()
	for name, y := range map[string]string{
		"fixture": "name: fixture\ntype: drill_suite\nsuite_config: {}\n",
		"step":    "name: step\ntype: drill\nsuite: fixture\nnodes:\n  - name: one\n    type: tool\n    args: {tool: read_file, path: fixture}\n",
	} {
		if err := fs.SaveFlow(context.Background(), name, y); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	deps := &ScopedDrillExecution{NewExecutor: func(tool.Context) (adrill.ToolExecutor, error) {
		calls++
		return scopedDrillExecutorFunc(func(context.Context, string, map[string]any) (any, error) { return "fixture", nil }), nil
	}}
	ctx := &mockToolCtx{Context: WithDrillExecutionContext(store.WithFlowStore(flowDrillPrincipal(t, false), fs), deps)}
	got, err := executeRunDrill(ctx, &runDrillDeps{}, RunDrillArgs{SuiteName: "fixture"})
	if err == nil || !strings.Contains(err.Error(), "scoped drill store") || calls != 0 {
		t.Fatalf("scoped drill used general store: %+v err=%v executors=%d", got, err, calls)
	}
}

func TestScopedFlowDrillAuthorityLocalCompatibility(t *testing.T) {
	old := flowRunnerAccessVar
	defer func() { flowRunnerAccessVar = old }()
	global := &mockFlowRunnerAccess{}
	flowRunnerAccessVar = global
	for _, ctx := range []context.Context{context.Background(), flowDrillPrincipal(t, true)} {
		runner, scoped := flowRunnerFromContext(ctx)
		if runner != global || scoped {
			t.Fatal("local/global compatibility changed")
		}
		got, err := executeRunDrill(&mockToolCtx{Context: ctx}, &runDrillDeps{}, RunDrillArgs{})
		if err != nil || got.Summary != "suite_name is required" {
			t.Fatalf("legacy drill validation changed: %+v %v", got, err)
		}
	}
}
