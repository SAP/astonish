package launcher

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

// These tests invoke the catalog's real run_flow -> scopedChatFlowRunner ->
// AstonishAgent/ADK -> scopedChatFlowTool -> chatRuntimeTool -> ordinary tool.
// They are local engine/dispatcher evidence, not Studio HTTP admission, scheduler
// triggering, physical sandbox isolation, or a delegated ChatAgent. Flow engine
// bookkeeping uses a child ADK session; node operations retain the parent session.
func flowChildAuthorityRuntime(t *testing.T, scope chatruntime.ResourceScope, mode string, limit int64) *servicesParityRuntime {
	t.Helper()
	r := newServicesParityRuntime(t, scope, mode)
	// Mutate the explicitly scoped repository's data, not the catalog or runner.
	fs := r.f.services.Flows.(servicesParityFlows)
	fs.data["authority-flow"] = fmt.Sprintf(`description: controlled flow authority
nodes:
  - name: first
    type: tool
    tools_selection: [shell_command]
    args:
      command: printf services-parity-ok
      working_dir: %q
  - name: second
    type: tool
    tools_selection: [list_drills]
    args: {}
flow:
  - from: START
    to: first
  - from: first
    to: second
  - from: second
    to: END
`, r.node.root)
	if mode != "ordinary-chat" {
		if mode == "autonomous-bot" {
			drillGrants(t, r.f, map[string][]map[string]any{
				"run_flow":      {{"flow_name": "authority-flow"}},
				"shell_command": {{"command": "printf services-parity-ok", "working_dir": r.node.root}},
				"list_drills":   {{}},
			})
		}
		if limit > 0 {
			meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxToolCalls: &limit}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			r.f.req.Budget = meter
		}
		// The shared drillGuard only consumes exact grants. Flow nodes also have
		// an engine-level pre-dispatch guard, whose contract requires live checks
		// before consumption (before the terminal Chat wrapper is entered).
		prepare := r.f.cfg.Prepare
		live := &flowChildLiveGuard{r: r, fence: r.f.req.Run.Lease.Fence}
		r.f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
			safe, err := prepare(ctx, req, scope)
			if err == nil {
				safe.PolicyRuntime = agent.BotPolicyRuntime{Classifier: BotCatalogueClassifier{}, Guard: live}
			}
			return safe, err
		}
		runtime, err := r.f.construct(t)
		if err != nil {
			t.Fatal(err)
		}
		r.chat = runtime.Agent
		t.Cleanup(runtime.Cleanup)
	}
	return r
}

type flowChildLiveGuard struct {
	r     *servicesParityRuntime
	fence int64
}

func (g *flowChildLiveGuard) Authorize(ctx context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if err := ctx.Err(); err != nil {
		return execution.PolicyDecision{}, err
	}
	if g.r.f.req.Run.Lease.Fence != g.fence {
		return execution.PolicyDecision{}, store.ErrBotConflict
	}
	if err := g.r.f.req.Budget.Check(ctx); err != nil {
		return execution.PolicyDecision{}, err
	}
	return g.r.guard.Authorize(ctx, action)
}

type flowChildAuthorityObservation struct {
	t      *testing.T
	r      *servicesParityRuntime
	mode   string
	nested []string
	after  func(string)
}

func (o *flowChildAuthorityObservation) Observe(ctx context.Context, name string, _ any, _ map[string]any, err error) {
	// Bot owner rebinding deliberately drops the private flow marker. These
	// two names occur only inside this test's flow, never at its parent boundary.
	if (name != "shell_command" && name != "list_drills") || err != nil {
		return
	}
	o.nested = append(o.nested, name)
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Subject != o.r.f.req.Principal.Subject || p.TeamSlug != "owner-team" || p.OrgSlug != "owner-org" {
		o.t.Error("flow step lost captured scope")
	}
	if o.mode == "ordinary-chat" {
		if _, ok := execution.BotRunIdentityFromContext(ctx); ok || execution.RunPolicyFromContext(ctx) != nil {
			o.t.Error("ordinary flow step acquired Bot authority")
		}
	}
	if o.after != nil {
		o.after(name)
	}
}

func flowChildAuthorityBinding(t *testing.T, r *servicesParityRuntime) *chatRuntimeBinding {
	t.Helper()
	entry := r.chat.ToolIndex.GetToolEntry("run_flow")
	if entry == nil {
		t.Fatal("real run_flow missing from shared catalog")
	}
	binding := entry.Tool.(*chatRuntimeTool).binding
	if _, ok := binding.flowRunner.(*scopedChatFlowRunner); !ok {
		t.Fatalf("not the real scoped flow engine: %T", binding.flowRunner)
	}
	return binding
}

func TestChatRuntimeNestedFlowFences(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot"} {
			// Positive control runs first, with the same engine/tools as every denial.
			fences := []string{"none", "cancel"}
			if mode != "ordinary-chat" {
				fences = append(fences, "lease-revoked", "budget")
			}
			for _, fence := range fences {
				t.Run(string(scope)+"/"+mode+"/"+fence, func(t *testing.T) {
					limit := int64(0)
					if fence == "budget" {
						limit = 2 // parent run_flow plus first nested shell; no fresh child allowance
					}
					r := flowChildAuthorityRuntime(t, scope, mode, limit)
					ctx, cancel := context.WithCancel(r.f.ctx)
					defer cancel()
					observation := &flowChildAuthorityObservation{t: t, r: r, mode: mode}
					observation.after = func(name string) {
						if name != "shell_command" {
							return
						}
						switch fence {
						case "cancel":
							cancel()
						case "lease-revoked":
							// Authoritative fixture repository changes AFTER step one. The
							// constructed request and next action's exact grant stay unchanged.
							r.f.req.Run.Lease.Fence++
						}
					}
					binding := flowChildAuthorityBinding(t, r)
					binding.hooks.Accounting = observation
					out, err := r.invoke(t, ctx, "run_flow", map[string]any{"flow_name": "authority-flow"})
					if len(r.node.calls) != 1 || r.node.outputs[0]["stdout"] != "services-parity-ok" {
						t.Fatalf("first actual nested tool did not execute: calls=%v out=%v err=%v", r.node.calls, out, err)
					}
					if fence == "none" {
						if err != nil || out["status"] != "completed" || !slices.Equal(observation.nested, []string{"shell_command", "list_drills"}) {
							t.Fatalf("unfenced flow failed: out=%v err=%v nested=%v", out, err, observation.nested)
						}
					} else {
						want := context.Canceled
						switch fence {
						case "lease-revoked":
							want = store.ErrBotConflict
						case "budget":
							want = bot.ErrBudgetExceeded
						}
						// The flow engine serializes pre-dispatch policy denials into
						// status:error. Terminal wrapper failures retain their Go error.
						blocked := errors.Is(err, want)
						if mode == "autonomous-bot" && err == nil && out["status"] == "error" {
							blocked = strings.Contains(fmt.Sprint(out["message"]), "live execution authorization failed")
						}
						if !blocked || !slices.Equal(observation.nested, []string{"shell_command"}) {
							t.Fatalf("next nested step escaped %s: out=%v err=%v nested=%v", fence, out, err, observation.nested)
						}
						if fence == "budget" {
							if _, err := r.invoke(t, r.f.ctx, "list_drills", map[string]any{}); !errors.Is(err, bot.ErrBudgetExceeded) {
								t.Fatalf("returning to parent reset shared budget: %v", err)
							}
						}
					}
					if mode == "autonomous-bot" {
						want := 2 // parent flow and shell only; second grant remains fresh
						if fence == "none" {
							want = 3
						}
						if len(r.guard.consumed) != want {
							t.Fatalf("wrong exact grant consumption: %d want %d", len(r.guard.consumed), want)
						}
					}
					engine := binding.flowRunner.(*scopedChatFlowRunner)
					engine.engine.sessions.Range(func(_, _ any) bool {
						t.Error("finished/aborted flow retained its child session")
						return true
					})
				})
			}
		}
	}
}

// Narrowing is a trusted internal adapter operation, not model-provided YAML.
// Apply it only at the actual flow child boundary, AFTER the parent's run_flow
// admission. Ordinary/interactive Chat have no autonomous RunPolicy to narrow.
// flowChildNarrowingAccess is a trusted child-policy adapter around (not a
// replacement for) the production engine. It narrows before the engine's own
// pre-dispatch authorization; narrowing at a terminal tool would be too late.
type flowChildNarrowingAccess struct {
	tools.FlowRunnerAccess
	policy *execution.RunPolicy
	calls  int
}

func (a *flowChildNarrowingAccess) RunFlowFromYAML(ctx context.Context, yaml, name string, params map[string]string, response, key string) (*tools.FlowRunResult, error) {
	a.calls++
	invocation := ctx.(tool.Context)
	bound := execution.WithRunPolicy(ctx, a.policy)
	return a.FlowRunnerAccess.RunFlowFromYAML(chatRuntimeToolContext{Context: invocation, bound: bound}, yaml, name, params, response, key)
}

func TestChatRuntimeFlowPreservesNarrowedChildPolicy(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r := flowChildAuthorityRuntime(t, scope, "autonomous-bot", 0)
			binding := flowChildAuthorityBinding(t, r)
			child, err := r.f.req.Policy.Narrow([]string{"shell_command", "list_drills"})
			if err != nil {
				t.Fatal(err)
			}
			access := &flowChildNarrowingAccess{FlowRunnerAccess: binding.flowRunner, policy: child}
			binding.flowRunner = access
			out, err := r.invoke(t, r.f.ctx, "run_flow", map[string]any{"flow_name": "authority-flow"})
			if (err == nil && out["status"] != "error") || !strings.Contains(fmt.Sprint(out, err), "child consequential action prohibited") || access.calls != 1 || len(r.node.calls) != 0 || len(r.guard.consumed) != 1 {
				t.Fatalf("actual flow child borrowed parent grant: out=%v err=%v nested=%d calls=%v grants=%d", out, err, access.calls, r.node.calls, len(r.guard.consumed))
			}
			// The denied child did not consume the parent's independently usable,
			// exact shell grant. This is also a positive control for the real tool.
			if _, err := r.invoke(t, r.f.ctx, "shell_command", map[string]any{"command": "printf services-parity-ok", "working_dir": r.node.root}); err != nil {
				t.Fatal(err)
			}
			if len(r.node.calls) != 1 || len(r.guard.consumed) != 2 {
				t.Fatal("parent shell grant was lost or child dispatched")
			}
		})
	}
}
