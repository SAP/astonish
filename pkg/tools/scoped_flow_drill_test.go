package tools

import (
	"context"
	"errors"
	"testing"

	adrill "github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

type scopedDrillFixtureContextKey struct{}

type scopedTestRunnable interface {
	Run(tool.Context, any) (map[string]any, error)
}
type scopedDrillExecutorFunc func(context.Context, string, map[string]any) (any, error)

func (f scopedDrillExecutorFunc) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	return f(ctx, name, args)
}

type scopedDrillModelFunc func(context.Context, string) (string, error)

func (f scopedDrillModelFunc) EvaluateText(ctx context.Context, prompt string) (string, error) {
	return f(ctx, prompt)
}

func TestRunDrillScopedSemanticBudgetStopsRetries(t *testing.T) {
	fs := newMemFlowStore()
	for name, y := range map[string]string{
		"fixture": "name: fixture\ntype: drill_suite\nsuite_config: {}\n",
		"step":    "name: step\ntype: drill\nsuite: fixture\ndrill_config:\n  mode: tutorial\n  on_fail: continue\n  max_retries: 3\nnodes:\n  - name: one\n    type: tool\n    args: {tool: shell_command, command: never-on-host}\n    assert: {type: semantic, expected: approved}\n  - name: two\n    type: tool\n    args: {tool: shell_command, command: never-again}\n",
	} {
		if err := fs.SaveFlow(context.Background(), name, y); err != nil {
			t.Fatal(err)
		}
	}
	ordinary, err := NewRunDrillTool(nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("model budget exhausted")
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{true: "missing", false: "budget"}[missing], func(t *testing.T) {
			calls, models := 0, 0
			deps := &ScopedDrillExecution{
				NewExecutor: func(tool.Context) (adrill.ToolExecutor, error) {
					return scopedDrillExecutorFunc(func(context.Context, string, map[string]any) (any, error) {
						calls++
						return map[string]any{"stdout": "fixture"}, nil
					}), nil
				},
			}
			if !missing {
				deps.LLM = scopedDrillModelFunc(func(context.Context, string) (string, error) { models++; return "", denied })
			}
			ctx := &mockToolCtx{Context: WithDrillExecutionContext(store.WithTeamFlowStore(context.Background(), fs), deps)}
			_, err := ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"suite_name": "fixture"})
			if err == nil || calls != 1 || (!missing && (!errors.Is(err, denied) || models != 1)) || (missing && models != 0) {
				t.Fatalf("error=%v tool=%d model=%d", err, calls, models)
			}
		})
	}
}

func TestRunFlowScopedHandler(t *testing.T) {
	old := flowRunnerAccessVar
	defer func() { flowRunnerAccessVar = old }()
	global := &mockFlowRunnerAccess{result: &FlowRunResult{Status: "completed", Output: "ambient"}}
	flowRunnerAccessVar = global
	fs := newMemFlowStore()
	if err := fs.SaveFlow(context.Background(), "fixture", "name: fixture\nnodes:\n  - name: step\n    type: tool\n    tools_selection: [read_file]\nflow:\n  - from: START\n    to: step\n  - from: step\n    to: END\n"); err != nil {
		t.Fatal(err)
	}
	base := store.WithFlowStore(context.Background(), fs)
	ordinary, err := NewRunFlowTool()
	if err != nil {
		t.Fatal(err)
	}
	scoped := &mockFlowRunnerAccess{result: &FlowRunResult{Status: "completed", Output: "scoped"}}
	ctx := &mockToolCtx{Context: WithFlowRunnerContext(base, scoped)}
	got, err := ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"flow_name": "fixture"})
	if err != nil || got["output"] != "scoped" || global.lastKey != "" || len(scoped.cleaned) != 1 {
		t.Fatalf("got=%v err=%v scoped=%+v global=%+v", got, err, scoped, global)
	}
	// Resume keeps option validation, parameter mapping and cleanup on the
	// supplied runner rather than the daemon-global state machine.
	scoped.pausedNode = "choice"
	scoped.pausedOptions = []string{"Yes", "No"}
	got, err = ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"flow_name": "fixture", "parameters": map[string]string{"choice": "yes"}})
	if err != nil || got["status"] != "completed" || scoped.lastInput != "Yes" || global.lastKey != "" {
		t.Fatalf("scoped resume: %v %v %+v", got, err, scoped)
	}
	scoped.pausedNode = ""
	denied := errors.New("flow child budget exhausted")
	scoped.err = denied
	_, err = ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"flow_name": "fixture"})
	if !errors.Is(err, denied) {
		t.Fatalf("lost scoped terminal error: %v", err)
	}
	ctx.Context = WithFlowRunnerContext(base, nil)
	got, err = ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"flow_name": "fixture"})
	if err == nil || global.lastKey != "" {
		t.Fatalf("nil scope fell back: %v %v %+v", got, err, global)
	}
	// A supplied runner with no scoped store must not resolve a host flow path.
	ctx.Context = WithFlowRunnerContext(context.Background(), scoped)
	_, err = ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"flow_name": "fixture"})
	if err == nil {
		t.Fatal("missing scoped store accepted")
	}
}

func TestRunDrillScopedHandler(t *testing.T) {
	fs := newMemFlowStore()
	for name, y := range map[string]string{
		"fixture": "name: fixture\ntype: drill_suite\nsuite_config: {}\n",
		"step":    "name: step\ntype: drill\nsuite: fixture\nnodes:\n  - name: one\n    type: tool\n    args: {tool: shell_command, command: never-on-host}\n",
	} {
		if err := fs.SaveFlow(context.Background(), name, y); err != nil {
			t.Fatal(err)
		}
	}
	base := store.WithTeamFlowStore(context.Background(), fs)
	ordinary, err := NewRunDrillTool(nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls, injects, saves := 0, 0, 0
	denied := errors.New("nested budget exhausted")
	deps := &ScopedDrillExecution{
		NewExecutor: func(ctx tool.Context) (adrill.ToolExecutor, error) {
			return scopedDrillExecutorFunc(func(step context.Context, name string, args map[string]any) (any, error) {
				calls++
				if name != "shell_command" || step.Value(scopedDrillFixtureContextKey{}) != "fixture" {
					t.Fatalf("lost invocation: %s %v", name, step)
				}
				return map[string]any{"stdout": "scoped"}, nil
			}), nil
		},
		InjectCredentials: func(tool.Context, *adrill.LoadedSuite) error { injects++; return nil },
		SaveReport:        func(tool.Context, *adrill.SuiteReport) error { saves++; return nil },
	}
	ctx := &mockToolCtx{Context: WithDrillExecutionContext(context.WithValue(base, scopedDrillFixtureContextKey{}, "fixture"), deps)}
	got, err := ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"suite_name": "fixture"})
	if err != nil || got["status"] != "passed" || calls != 1 || injects != 1 || saves != 1 {
		t.Fatalf("got=%v err=%v calls=%d inject=%d save=%d", got, err, calls, injects, saves)
	}
	ctx.Context = WithDrillExecutionContext(base, nil)
	_, err = ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"suite_name": "fixture"})
	if err == nil || calls != 1 {
		t.Fatalf("nil scope accepted: %v calls=%d", err, calls)
	}
	deps.NewExecutor = func(tool.Context) (adrill.ToolExecutor, error) {
		return scopedDrillExecutorFunc(func(context.Context, string, map[string]any) (any, error) { calls++; return nil, denied }), nil
	}
	ctx.Context = WithDrillExecutionContext(base, deps)
	_, err = ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"suite_name": "fixture"})
	if !errors.Is(err, denied) || calls != 2 || saves != 1 {
		t.Fatalf("terminal error lost/retried: %v calls=%d saves=%d", err, calls, saves)
	}
	cancelled, cancel := context.WithCancel(ctx.Context)
	cancel()
	ctx.Context = cancelled
	_, err = ordinary.(scopedTestRunnable).Run(ctx, map[string]any{"suite_name": "fixture"})
	if !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("cancelled executed: %v calls=%d", err, calls)
	}
}
