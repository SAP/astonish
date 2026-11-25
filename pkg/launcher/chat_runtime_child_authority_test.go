package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// These are local shared-runtime acceptance tests, not HTTP admission or physical
// sandbox evidence. Ordinary team Chat uses the explicit shared constructor;
// Studio admission remains personal-only. No Bot identity is added to Chat.
func childAuthorityBinding(t *testing.T, r *servicesParityRuntime) *chatRuntimeBinding {
	t.Helper()
	return r.chat.ToolIndex.GetToolEntry("run_drill").Tool.(*chatRuntimeTool).binding
}

// Reconstruct via the existing Bot factory after changing trusted test inputs,
// rather than mutating a running policy or replacing production dispatch.
func childAuthorityRebuildBot(t *testing.T, r *servicesParityRuntime, mode string, limit int64) {
	t.Helper()
	if mode == "autonomous-bot" {
		actions := map[string][]map[string]any{
			"run_drill":     {{"suite_name": "suite"}},
			"shell_command": {{"command": "printf services-parity-ok", "working_dir": r.node.root}},
			"list_drills":   {{}},
		}
		spec := execution.RunPolicySpec{Identity: r.f.req.Policy.Identity(), Level: execution.AutonomySupervised}
		for name, calls := range actions {
			spec.AllowedTools = append(spec.AllowedTools, name)
			for _, args := range calls {
				action := servicesParityAction(t, r.f, name, args)
				digest, err := action.Digest()
				if err != nil {
					t.Fatal(err)
				}
				spec.Grants = append(spec.Grants, execution.ScopedGrant{ID: uuid.New(), Identity: spec.Identity, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
			}
		}
		var err error
		r.f.req.Policy, err = execution.NewRunPolicy(spec)
		if err != nil {
			t.Fatal(err)
		}
	}
	if limit > 0 {
		meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxToolCalls: &limit}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		r.f.req.Budget = meter
	}
	runtime, err := r.f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	r.chat = runtime.Agent
	t.Cleanup(runtime.Cleanup)
}

type childAuthorityModel struct {
	t         *testing.T
	mode      string
	calls     int
	responses []string
	shell     map[string]any
}

func (*childAuthorityModel) Name() string { return "offline-child-authority" }
func (m *childAuthorityModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.mode == "ordinary-chat" {
			if _, ok := execution.BotRunIdentityFromContext(ctx); ok || execution.RunPolicyFromContext(ctx) != nil {
				m.t.Error("ordinary child acquired Bot authority")
			}
		}
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse != nil {
					raw, _ := json.Marshal(part.FunctionResponse.Response)
					m.responses = append(m.responses, string(raw))
				}
			}
		}
		m.calls++
		var fc *genai.FunctionCall
		switch m.calls {
		case 1:
			fc = &genai.FunctionCall{ID: "read", Name: "list_drills", Args: map[string]any{}}
		case 2:
			fc = &genai.FunctionCall{ID: "escape", Name: "execute_tool", Args: map[string]any{"name": "shell_command", "arguments": m.shell}}
		default:
			yield(&model.LLMResponse{Content: genai.NewContentFromText("child finished", "model"), TurnComplete: true}, nil)
			return
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: fc}}}}, nil)
	}
}

func TestChatRuntimeRestrictedChildAuthority(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				r := newServicesParityRuntime(t, scope, mode)
				shell := map[string]any{"command": "printf services-parity-ok", "working_dir": r.node.root}
				m := &childAuthorityModel{t: t, mode: mode, shell: shell}
				if mode != "ordinary-chat" {
					r.f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return m, nil }
					childAuthorityRebuildBot(t, r, mode, 0)
				}
				// The exact parent shell grant remains unused while the child runs.
				mgr := r.chat.SubAgentManager
				mgr.LLM = m
				// This fixture's pool does not implement Alias. No child node call is
				// permitted in this test; record the actual created session instead.
				var childID string
				mgr.OnChildSession = func(parent, child string) {
					if parent != r.node.session || child == parent || child == "" {
						t.Error("invalid child identity")
					}
					childID = child
				}
				bound, release, err := childAuthorityBinding(t, r).bind(r.f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				result := mgr.RunTask(bound, agent.SubAgentTask{Name: "restricted", Description: "List drills; try parent shell", ToolFilter: []string{"list_drills"}, ToolSelectionMode: agent.ChildToolSelectionStrict, ParentID: r.node.session})
				if result.Status != "success" || m.calls != 3 || childID == "" {
					t.Fatalf("child did not run complete sequence: %+v model calls=%d responses=%v", result, m.calls, m.responses)
				}
				responses := strings.Join(m.responses, "\n")
				if !strings.Contains(responses, "shell_command") || !strings.Contains(responses, "outside the child scope") {
					t.Fatalf("missing actual child denial evidence: %s", responses)
				}
				if mode == "autonomous-bot" {
					// list_drills is conservatively consequential in the production
					// classifier, so autonomous children reject it too. Do not weaken
					// the classifier or relabel this as successful child read coverage.
					if !strings.Contains(responses, "child consequential action prohibited") {
						t.Fatalf("missing autonomous policy fence: %s", responses)
					}
				} else if !strings.Contains(responses, "first") {
					t.Fatalf("missing actual child drill listing: %s", responses)
				}
				if len(r.node.calls) != 0 || len(r.guard.consumed) != 0 {
					t.Fatalf("restricted child borrowed parent authority: %v grants=%v", r.node.calls, r.guard.consumed)
				}
				// Positive control after denial proves the parent's capability and
				// exact grant were real, fresh, and not consumed by the child.
				if _, err := r.invoke(t, r.f.ctx, "shell_command", shell); err != nil {
					t.Fatal(err)
				}
				if len(r.node.calls) != 1 {
					t.Fatal("parent control did not execute")
				}
			})
		}
	}
}

type childAuthorityObservation struct {
	after func(string)
	seen  []string
}

func (o *childAuthorityObservation) Observe(_ context.Context, name string, _ any, _ map[string]any, err error) {
	// Accounting also observes refused dispatches. Only successful operations
	// attest execution; cancellation may be observed after binding but before Run.
	if err != nil {
		return
	}
	o.seen = append(o.seen, name)
	if o.after != nil {
		o.after(name)
	}
}

func TestChatRuntimeNestedDrillFences(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot"} {
			fences := []string{"none", "cancel"}
			if mode != "ordinary-chat" {
				fences = append(fences, "lease-revoked", "budget")
			}
			for _, fence := range fences {
				t.Run(string(scope)+"/"+mode+"/"+fence, func(t *testing.T) {
					r := newServicesParityRuntime(t, scope, mode)
					// Review the complete two-step suite before issuing exact grants.
					fs := r.f.services.Flows.(servicesParityFlows)
					fs.data["first"] += "  - name: second\n    type: tool\n    args: {tool: list_drills}\n"
					if mode != "ordinary-chat" {
						limit := int64(0)
						if fence == "budget" {
							limit = 2
						}
						childAuthorityRebuildBot(t, r, mode, limit)
					}
					ctx, cancel := context.WithCancel(r.f.ctx)
					defer cancel()
					observation := &childAuthorityObservation{}
					observation.after = func(name string) {
						if name != "shell_command" {
							return
						}
						if fence == "cancel" {
							cancel()
						}
						if fence == "lease-revoked" {
							r.f.req.Run.Lease.Fence++
						}
					}
					childAuthorityBinding(t, r).hooks.Accounting = observation
					out, err := r.invoke(t, ctx, "run_drill", map[string]any{"suite_name": "suite"})
					if fence == "none" {
						if err != nil || out["status"] != "passed" || len(r.node.calls) != 1 || len(r.reports.reports) != 1 || !strings.Contains(strings.Join(observation.seen, ","), "list_drills") {
							t.Fatalf("unfenced positive control failed: %v %v observed=%v", out, err, observation.seen)
						}
						return
					}
					if err == nil {
						t.Fatalf("nested fence did not abort: %v observed=%v", out, observation.seen)
					}
					if fence == "cancel" && !errors.Is(err, context.Canceled) {
						t.Fatalf("wrong cancellation: %v", err)
					}
					if fence == "budget" && !errors.Is(err, bot.ErrBudgetExceeded) {
						t.Fatalf("wrong budget failure: %v", err)
					}
					if fence == "budget" {
						// Returning from the drill cannot reset the shared allowance.
						if _, parentErr := r.invoke(t, r.f.ctx, "list_drills", map[string]any{}); !errors.Is(parentErr, bot.ErrBudgetExceeded) {
							t.Fatalf("parent escaped exhausted child budget: %v", parentErr)
						}
					}
					if fence == "lease-revoked" && !errors.Is(err, store.ErrBotConflict) {
						t.Fatalf("wrong lease failure: %v", err)
					}
					if len(r.node.calls) != 1 || r.node.outputs[0]["stdout"] != "services-parity-ok" {
						t.Fatalf("first child step did not execute: %v", r.node.calls)
					}
					for _, name := range observation.seen {
						if name == "list_drills" {
							t.Fatal("second child step escaped fence")
						}
					}
					// Invocation has synchronously returned; no detached child continues work.
					if len(r.reports.reports) != 0 {
						t.Fatalf("aborted child persisted success: %+v", r.reports.reports)
					}
					t.Logf("%s: first nested shell executed, next nested dispatch fenced (%v)", fmt.Sprint(scope), err)
				})
			}
		}
	}
}
