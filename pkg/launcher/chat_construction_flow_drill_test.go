package launcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	adrill "github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

type runtimeFlowDrillContext struct{ runtimeTestToolContext }

func (runtimeFlowDrillContext) SessionID() string { return "same-session" }

type runtimeFlowAccess struct {
	run func(context.Context) (*tools.FlowRunResult, error)
}

func (r *runtimeFlowAccess) RunFlow(context.Context, string, map[string]string, string, string) (*tools.FlowRunResult, error) {
	return nil, errors.New("filesystem flow execution forbidden")
}
func (r *runtimeFlowAccess) RunFlowFromYAML(ctx context.Context, _ string, _ string, _ map[string]string, _ string, _ string) (*tools.FlowRunResult, error) {
	return r.run(ctx)
}
func (*runtimeFlowAccess) GetPausedNode(string) string      { return "" }
func (*runtimeFlowAccess) GetPausedOptions(string) []string { return nil }
func (*runtimeFlowAccess) CleanupSession(string)            {}

type runtimeDrillExecutorFunc func(context.Context, string, map[string]any) (any, error)

func (f runtimeDrillExecutorFunc) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	return f(ctx, name, args)
}

type runtimeFlowDrillStore struct{ store.FlowStore }

func (*runtimeFlowDrillStore) GetFlow(_ context.Context, name string) (string, error) {
	switch name {
	case "flow":
		return "name: flow\nnodes:\n  - name: step\n    type: tool\n    tools_selection: [nested_fixture]\nflow:\n  - from: START\n    to: step\n  - from: step\n    to: END\n", nil
	case "suite":
		return "name: suite\ntype: drill_suite\nsuite_config: {}\n", nil
	case "step":
		return "name: step\ntype: drill\nsuite: suite\ndrill_config:\n  mode: tutorial\n  on_fail: continue\n  max_retries: 3\nnodes:\n  - name: one\n    type: tool\n    args: {tool: nested_fixture}\n  - name: two\n    type: tool\n    args: {tool: nested_fixture}\n", nil
	}
	return "", fmt.Errorf("unknown fixture %q", name)
}
func (*runtimeFlowDrillStore) ListFlowsByType(context.Context, []string) []store.FlowSummary {
	return []store.FlowSummary{{Name: "step", Type: "drill", Suite: "suite"}}
}

type runtimeFlowDrillBinding func(context.Context) (context.Context, error)

func (f runtimeFlowDrillBinding) Bind(ctx context.Context) (context.Context, error) { return f(ctx) }

func runtimeFlowDrillDeps(t *testing.T) ChatRuntimeDependencies {
	t.Helper()
	flow, err := tools.NewRunFlowTool()
	if err != nil {
		t.Fatal(err)
	}
	drill, err := tools.NewRunDrillTool(nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ChatRuntimeDependencies{
		Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(),
		Catalog: &ChatCatalog{MainThreadTools: []tool.Tool{flow, drill}, Groups: map[string]*agent.ToolGroup{"flows": {Name: "flows", Tools: []tool.Tool{flow, drill}}}},
	}
}
func runtimeFlowDrillInvoke(runtime *ChatFactoryResult, ctx context.Context, name string) (map[string]any, error) {
	args := map[string]any{"flow_name": "flow"}
	if name == "run_drill" {
		args = map[string]any{"suite_name": "suite"}
	}
	for _, candidate := range runtime.InternalTools {
		if candidate.Name() == name {
			return candidate.(chatRuntimeRunnable).Run(runtimeFlowDrillContext{runtimeTestToolContext{lifetime: ctx}}, args)
		}
	}
	return nil, fmt.Errorf("missing tool %s", name)
}

func TestChatRuntimeFlowDrillMissingDependenciesShadowAmbient(t *testing.T) {
	calls := 0
	ambientFlow := &runtimeFlowAccess{run: func(context.Context) (*tools.FlowRunResult, error) {
		calls++
		return &tools.FlowRunResult{Status: "completed"}, nil
	}}
	ambientDrill := &tools.ScopedDrillExecution{NewExecutor: func(tool.Context) (adrill.ToolExecutor, error) {
		calls++
		return runtimeDrillExecutorFunc(func(context.Context, string, map[string]any) (any, error) { return map[string]any{}, nil }), nil
	}}
	bind := runtimeFlowDrillBinding(func(ctx context.Context) (context.Context, error) {
		ctx = store.WithFlowStore(ctx, &runtimeFlowDrillStore{})
		ctx = store.WithTeamFlowStore(ctx, &runtimeFlowDrillStore{})
		return tools.WithDrillExecutionContext(tools.WithFlowRunnerContext(ctx, ambientFlow), ambientDrill), nil
	})
	for _, hooks := range []ChatRuntimeHooks{{Context: bind}, {Authorization: &runtimeTestPolicy{}}, {Accounting: &runtimeTestPolicy{}}} {
		runtime, err := NewScopedChatRuntime(t.Context(), runtimeFlowDrillDeps(t), hooks)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(runtime.Cleanup)
		ctx, err := bind.Bind(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"run_flow", "run_drill"} {
			_, err := runtimeFlowDrillInvoke(runtime, ctx, name)
			if err == nil || !strings.Contains(err.Error(), "scoped") || !strings.Contains(err.Error(), "unavailable") {
				t.Errorf("%s accepted ambient dependency: %v", name, err)
			}
		}
	}
	if calls != 0 {
		t.Fatalf("ambient executors called %d times", calls)
	}
}

// Use both hook-only and resolved-Inputs runtimes. The former isolates the
// binding contract; the latter proves actual catalog-built shared handlers.
func TestChatRuntimeFlowDrillExplicitExecutionIsolation(t *testing.T) {
	for _, scopedInputs := range []bool{false, true} {
		t.Run(fmt.Sprintf("inputs=%t", scopedInputs), func(t *testing.T) {
			type requestKey struct{}
			fs := &runtimeFlowDrillStore{}
			var runtimes []*ChatFactoryResult
			policies := []*runtimeFlowDrillPolicy{{}, {}}
			calls := []int{0, 0}
			saved := []int{0, 0}
			for i := range 2 {
				deps := runtimeFlowDrillDeps(t)
				fixture, err := functiontool.New(functiontool.Config{Name: "nested_fixture", Description: "scoped nested fixture"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
					if ctx.Value(requestKey{}) != "current" {
						t.Fatal("nested execution lost current context")
					}
					calls[i]++
					return map[string]any{"stdout": fmt.Sprintf("runtime-%d", i)}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				hooks := ChatRuntimeHooks{Authorization: policies[i], AdditionalTools: []tool.Tool{fixture}}
				hooks.Context = runtimeFlowDrillBinding(func(ctx context.Context) (context.Context, error) {
					return store.WithTeamFlowStore(store.WithFlowStore(ctx, fs), fs), nil
				})
				if scopedInputs {
					f := newBotFactoryFixture(t)
					inputs, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopeTeam, AppConfig: deps.Config.AppConfig})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = inputs.Close() })
					deps.Inputs, deps.Catalog, deps.Resources = inputs, nil, &ChatCatalogResources{Pool: &catalogTestPool{}}
				}
				dispatch := func(ctx context.Context) (map[string]any, error) {
					groups := agent.RequestMCPGroupsFromContext(ctx)
					g := groups["runtime_extensions"]
					if g == nil || len(g.Tools) != 1 {
						t.Fatal("executor lost final catalog")
					}
					guarded, ok := g.Tools[0].(*chatRuntimeTool)
					if !ok {
						t.Fatal("executor received unguarded tool")
					}
					return guarded.Run(runtimeFlowDrillContext{runtimeTestToolContext{lifetime: ctx}}, map[string]any{})
				}
				deps.FlowRunnerAccess = &runtimeFlowAccess{run: func(ctx context.Context) (*tools.FlowRunResult, error) {
					got, err := dispatch(ctx)
					if err != nil {
						return nil, err
					}
					return &tools.FlowRunResult{Status: "completed", Output: got["stdout"].(string)}, nil
				}}
				deps.DrillExecution = &tools.ScopedDrillExecution{
					NewExecutor: func(ctx tool.Context) (adrill.ToolExecutor, error) {
						if ctx.Value(requestKey{}) != "current" || agent.RequestMCPGroupsFromContext(ctx)["runtime_extensions"] == nil {
							t.Fatal("drill factory lost invocation catalog/context")
						}
						return runtimeDrillExecutorFunc(func(step context.Context, name string, _ map[string]any) (any, error) {
							if name != "nested_fixture" {
								t.Fatalf("unexpected nested tool %s", name)
							}
							return dispatch(step)
						}), nil
					},
					SaveReport: func(ctx tool.Context, _ *adrill.SuiteReport) error {
						if ctx.Value(requestKey{}) != "current" {
							t.Fatal("report lost scope")
						}
						saved[i]++
						return nil
					},
				}
				runtime, err := NewScopedChatRuntime(t.Context(), deps, hooks)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(runtime.Cleanup)
				runtimes = append(runtimes, runtime)
				// A caller mutating its dependency container after construction cannot
				// replace this runtime's executor on a later bind.
				deps.DrillExecution.NewExecutor = nil
			}
			ctx := context.WithValue(t.Context(), requestKey{}, "current")
			for _, i := range []int{0, 1, 0} {
				got, err := runtimeFlowDrillInvoke(runtimes[i], ctx, "run_flow")
				if err != nil || got["output"] != fmt.Sprintf("runtime-%d", i) {
					t.Fatalf("flow %d: %v %v", i, got, err)
				}
				got, err = runtimeFlowDrillInvoke(runtimes[i], ctx, "run_drill")
				if err != nil || got["status"] != "passed" {
					t.Fatalf("drill %d: %v %v", i, got, err)
				}
			}
			if calls[0] != 6 || calls[1] != 3 || saved[0] != 2 || saved[1] != 1 {
				t.Fatalf("cross-runtime execution: calls=%v reports=%v", calls, saved)
			}
			policies[0].denyNested = true
			for _, name := range []string{"run_flow", "run_drill"} {
				_, err := runtimeFlowDrillInvoke(runtimes[0], ctx, name)
				if !errors.Is(err, errRuntimeTestDenied) {
					t.Fatalf("%s lost current nested policy: %v", name, err)
				}
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				_, err = runtimeFlowDrillInvoke(runtimes[1], cancelled, name)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("%s lost cancellation: %v", name, err)
				}
			}
			if calls[0] != 6 || calls[1] != 3 || saved[0] != 2 || saved[1] != 1 {
				t.Fatalf("denial/cancellation executed: calls=%v reports=%v", calls, saved)
			}
		})
	}
}

type runtimeFlowDrillPolicy struct{ denyNested bool }

func (p *runtimeFlowDrillPolicy) Authorize(_ context.Context, name string, _ any) error {
	if p.denyNested && name == "nested_fixture" {
		return errRuntimeTestDenied
	}
	return nil
}

func TestChatRuntimeFlowDrillMissingInputsDependencies(t *testing.T) {
	f := newBotFactoryFixture(t)
	deps := runtimeFlowDrillDeps(t)
	inputs, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopeTeam, AppConfig: deps.Config.AppConfig})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inputs.Close() })
	deps.Inputs, deps.Catalog, deps.Resources = inputs, nil, &ChatCatalogResources{Pool: &catalogTestPool{}}
	runtime, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Cleanup)
	for _, name := range []string{"run_flow", "run_drill"} {
		_, err := runtimeFlowDrillInvoke(runtime, t.Context(), name)
		if err == nil || !strings.Contains(err.Error(), "scoped") || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("%s did not fail closed without explicit dependencies: %v", name, err)
		}
	}
}

func TestChatRuntimeFlowDrillUnscopedPreservesContext(t *testing.T) {
	calls := 0
	deps := runtimeFlowDrillDeps(t)
	runtime, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Cleanup)
	ctx := store.WithFlowStore(t.Context(), &runtimeFlowDrillStore{})
	ctx = store.WithTeamFlowStore(ctx, &runtimeFlowDrillStore{})
	ctx = tools.WithFlowRunnerContext(ctx, &runtimeFlowAccess{run: func(context.Context) (*tools.FlowRunResult, error) {
		calls++
		return &tools.FlowRunResult{Status: "completed"}, nil
	}})
	ctx = tools.WithDrillExecutionContext(ctx, &tools.ScopedDrillExecution{NewExecutor: func(tool.Context) (adrill.ToolExecutor, error) {
		calls++
		return runtimeDrillExecutorFunc(func(context.Context, string, map[string]any) (any, error) { return map[string]any{}, nil }), nil
	}})
	for _, name := range []string{"run_flow", "run_drill"} {
		_, err := runtimeFlowDrillInvoke(runtime, ctx, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if calls != 2 {
		t.Fatalf("unscoped context overwritten: %d", calls)
	}
}
