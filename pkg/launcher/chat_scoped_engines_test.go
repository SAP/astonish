package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

const scopedInjectionSuite = "name: suite\ntype: drill_suite\nsuite_config:\n  credentials: {app: owner}\n  credential_injection:\n    files:\n      - {credential: app, path: /workspace/token, field: token, mode: '0600'}\n"

func TestChatRuntimeScopedBotInjectionExactApproval(t *testing.T) {
	for _, mode := range []string{"success", "denied", "payload", "budget", "revoked", "hook-denied"} {
		t.Run(mode, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			cs := sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: "shared-bot-injection-123456"}})
			resolved, err := fleet.ResolveCredentialsPlatformMap(f.ctx, map[string]string{"app": "owner"}, cs)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := botDrillPayloadReference(resolved)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "payload" {
				ref = "stale-payload"
			}
			mapping := map[string]any{"suite_name": "suite", "credentials": map[string]string{"app": "owner"}, "credential_injection": map[string]any{"files": []any{map[string]any{"credential": "app", "path": "/workspace/token", "field": "token", "mode": "0600"}}}, "sandbox_id": "same-session", "payload_ref": ref}
			actions := map[string][]map[string]any{"inject_drill_credentials": {mapping}}
			if mode == "denied" {
				actions["inject_drill_credentials"] = nil
			}
			drillGrants(t, f, actions)
			guard := &drillGuard{consumed: map[uuid.UUID]bool{}}
			base := execution.WithRunPolicy(execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity()), f.req.Policy)
			base = agent.WithBotPolicyRuntime(base, agent.BotPolicyRuntime{Guard: guard, Classifier: BotCatalogueClassifier{}})
			base = store.WithCredentialStore(store.WithTeamFlowStore(base, &scopedInjectionStore{data: map[string]string{"suite": scopedInjectionSuite}}), cs)
			deps := runtimeFlowDrillDeps(t)
			deps.UseScopedEngines, deps.SessionID = true, "same-session"
			budget := &scopedInjectionBudget{}
			if mode == "budget" {
				budget.err = errRuntimeTestDenied
			}
			deps.DrillBudget = budget
			backend := &drillBackend{}
			deps.Resources = &ChatCatalogResources{Backend: backend}
			revoked := false
			runtime, err := NewScopedChatRuntime(base, deps, ChatRuntimeHooks{Authorization: scopedInjectionAuthorization(func(_ context.Context, name string, _ any) error {
				if mode == "hook-denied" && name == "inject_drill_credentials" {
					return errRuntimeTestDenied
				}
				return nil
			}), Context: runtimeFlowDrillBinding(func(ctx context.Context) (context.Context, error) {
				if revoked {
					return nil, errRuntimeTestDenied
				}
				return ctx, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Cleanup()
			if mode == "revoked" {
				revoked = true
			}
			_, err = runtimeFlowDrillInvoke(runtime, base, "run_drill")
			if mode != "success" {
				if err == nil || len(backend.ids) != 0 {
					t.Fatalf("denial wrote bytes: %v %v", err, backend.ids)
				}
				return
			}
			if err != nil || len(backend.contents) != 1 || backend.contents[0] != "shared-bot-injection-123456" || budget.reserved != 1 || len(guard.consumed) != 1 {
				t.Fatalf("exact approval/budget/materialization: err=%v writes=%v reservations=%d grants=%d", err, backend.ids, budget.reserved, len(guard.consumed))
			}
		})
	}
}

type scopedInjectionBudget struct {
	execution.RunBudget
	err      error
	reserved int
}

func (b *scopedInjectionBudget) Check(context.Context) error { return b.err }
func (b *scopedInjectionBudget) ReserveTool(context.Context, string) error {
	b.reserved++
	return b.err
}

func TestChatRuntimeScopedCredentialInjection(t *testing.T) {
	for _, name := range []string{"run_drill", "inject_drill_credentials"} {
		for _, mode := range []string{"success", "denied", "payload", "scope", "session", "cancelled", "missing", "missing-credentials", "empty"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				backend := &drillBackend{}
				cs := sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: "shared-injection-fixture-123456"}})
				data := drillData("test")
				data["suite"] = scopedInjectionSuite
				if mode == "empty" {
					data["suite"] = "name: suite\ntype: drill_suite\nsuite_config: {}\n"
				}
				data["step"] = "name: step\ntype: drill\nsuite: suite\nnodes:\n  - name: inspect\n    type: tool\n    args: {tool: nested_fixture}\n"
				fs := &scopedInjectionStore{data: data}
				resolved, err := fleet.ResolveCredentialsPlatformMap(t.Context(), map[string]string{"app": "owner"}, cs)
				if err != nil {
					t.Fatal(err)
				}
				ref, err := botDrillPayloadReference(resolved)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "payload" {
					ref = "wrong-payload"
				}
				deps := runtimeFlowDrillDeps(t)
				deps.UseScopedEngines, deps.SessionID = true, "same-session"
				deps.Resources = &ChatCatalogResources{Backend: backend}
				if mode == "empty" {
					// An unimplemented pool panics on lookup; empty suites must not
					// touch transport, even when the runtime has a pool configured.
					deps.Resources = &ChatCatalogResources{Pool: &catalogTestPool{}}
				}
				if mode == "missing" {
					deps.Resources = nil
				}
				inject, err := tools.NewInjectDrillCredentialsTool(nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				deps.Catalog.MainThreadTools = append(deps.Catalog.MainThreadTools, inject)
				principal := f.req.Principal
				base, err := execution.WithPrincipal(t.Context(), principal)
				if err != nil {
					t.Fatal(err)
				}
				policy := scopedInjectionAuthorization(func(ctx context.Context, toolName string, args any) error {
					if toolName != "inject_drill_credentials" {
						return nil
					}
					m, _ := args.(map[string]any)
					if _, full := m["payload_ref"]; !full {
						return nil
					}
					if mode == "denied" || m["payload_ref"] != ref {
						return errRuntimeTestDenied
					}
					if m["sandbox_id"] != "same-session" {
						t.Fatal("unpinned target")
					}
					return nil
				})
				steps := 0
				fixture, err := functiontool.New(functiontool.Config{Name: "nested_fixture", Description: "inspect injected fixture"}, func(tool.Context, struct{}) (map[string]any, error) {
					steps++
					if mode != "empty" && (len(backend.contents) != 1 || backend.contents[0] != "shared-injection-fixture-123456") {
						t.Fatal("step ran before credential materialization")
					}
					return map[string]any{"stdout": "verified"}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "session" {
					deps.SessionID = "foreign-session"
				}
				runtime, err := NewScopedChatRuntime(base, deps, ChatRuntimeHooks{AdditionalTools: []tool.Tool{fixture}, Authorization: policy, Context: runtimeFlowDrillBinding(func(ctx context.Context) (context.Context, error) {
					if mode == "missing-credentials" {
						return store.WithCredentialStore(store.WithTeamFlowStore(ctx, fs), nil), nil
					}
					return store.WithCredentialStore(store.WithTeamFlowStore(ctx, fs), cs), nil
				})})
				if err != nil {
					t.Fatal(err)
				}
				defer runtime.Cleanup()
				if mode == "scope" {
					principal.TeamSlug = "foreign"
					base, _ = execution.WithPrincipal(base, principal)
				}
				if mode == "cancelled" {
					cancelled, cancel := context.WithCancel(base)
					cancel()
					base = cancelled
				}
				var got map[string]any
				for _, candidate := range runtime.InternalTools {
					if candidate.Name() == name {
						got, err = candidate.(chatRuntimeRunnable).Run(runtimeFlowDrillContext{runtimeTestToolContext{lifetime: base}}, map[string]any{"suite_name": "suite"})
					}
				}
				if mode != "success" && mode != "empty" {
					if err == nil || len(backend.ids) != 0 {
						t.Fatalf("refusal: result=%v err=%v writes=%v", got, err, backend.ids)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if name == "run_drill" && steps != 1 {
					t.Fatalf("drill did not execute: %v", got)
				}
				if mode == "empty" {
					if len(backend.ids) != 0 {
						t.Fatal("empty suite wrote credentials")
					}
					return
				}
				if len(backend.ids) != 1 || backend.ids[0] != "same-session" || backend.contents[0] != "shared-injection-fixture-123456" {
					t.Fatalf("materialization missing: %v %v", got, backend.ids)
				}
				raw, _ := json.Marshal(got)
				if strings.Contains(string(raw), backend.contents[0]) {
					t.Fatal("secret returned")
				}
			})
		}
	}
}

type scopedInjectionStore struct {
	store.FlowStore
	data map[string]string
}

func (s *scopedInjectionStore) GetFlow(_ context.Context, name string) (string, error) {
	return s.data[name], nil
}
func (s *scopedInjectionStore) ListFlowsByType(context.Context, []string) []store.FlowSummary {
	if s.data["step"] != "" {
		return []store.FlowSummary{{Name: "step", Type: "drill", Suite: "suite"}}
	}
	return nil
}

type scopedInjectionAuthorization func(context.Context, string, any) error

func (f scopedInjectionAuthorization) Authorize(ctx context.Context, name string, args any) error {
	return f(ctx, name, args)
}

const scopedEngineFixture = `description: scoped engine fixture
nodes:
  - name: first
    type: tool
    tools_selection: [nested_fixture]
    args: {}
  - name: second
    type: tool
    tools_selection: [nested_fixture]
    args: {}
  - name: done
    type: output
    user_message: [engine finished]
flow:
  - from: START
    to: first
  - from: first
    to: second
  - from: second
    to: done
  - from: done
    to: END
`

func TestChatRuntimeScopedEnginesExecuteAndDeny(t *testing.T) {
	for _, name := range []string{"run_flow", "run_drill"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			deps := runtimeFlowDrillDeps(t)
			deps.UseScopedEngines = true
			fixture, err := functiontool.New(functiontool.Config{Name: "nested_fixture", Description: "bound fixture"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
				if ctx.SessionID() != "same-session" {
					t.Errorf("child changed sandbox session identity: %s", ctx.SessionID())
				}
				calls++
				return map[string]any{"stdout": "scoped bytes"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			policy := &runtimeFlowDrillPolicy{}
			hooks := ChatRuntimeHooks{Authorization: policy, AdditionalTools: []tool.Tool{fixture}, Context: runtimeFlowDrillBinding(func(ctx context.Context) (context.Context, error) {
				return store.WithTeamFlowStore(store.WithFlowStore(store.WithUserID(ctx, "fixture-owner"), &productionFlowStore{yaml: scopedEngineFixture}), &runtimeFlowDrillStore{}), nil
			})}
			runtime, err := NewScopedChatRuntime(t.Context(), deps, hooks)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Cleanup()
			got, err := runtimeFlowDrillInvoke(runtime, t.Context(), name)
			if err != nil || calls != 2 {
				t.Fatalf("real engine: calls=%d result=%v err=%v", calls, got, err)
			}
			status := "completed"
			if name == "run_drill" {
				status = "passed"
			}
			if got["status"] != status {
				t.Fatalf("result: %v", got)
			}
			if name == "run_flow" && got["output"] != "engine finished" {
				t.Fatalf("missing real output node: %v", got)
			}
			policy.denyNested = true
			_, err = runtimeFlowDrillInvoke(runtime, t.Context(), name)
			if !errors.Is(err, errRuntimeTestDenied) || calls != 2 {
				t.Fatalf("denied nested execution: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestChatRuntimeScopedDryRunExecuteDenyAndCancel(t *testing.T) {
	for _, mode := range []string{"success", "denied", "cancelled", "paused"} {
		t.Run(mode, func(t *testing.T) {
			deps := runtimeFlowDrillDeps(t)
			deps.UseScopedEngines, deps.SessionID = true, "same-session"
			calls := 0
			entered := make(chan struct{})
			fixture, err := functiontool.New(functiontool.Config{Name: "nested_fixture", Description: "dry-run fixture"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
				if ctx.SessionID() != "same-session" || store.UserIDFromContext(ctx) != "fixture-owner" || ctx.UserID() != "fixture-owner" {
					t.Errorf("lost request identity: session=%s owner=%s ADK owner=%s", ctx.SessionID(), store.UserIDFromContext(ctx), ctx.UserID())
				}
				calls++
				if mode == "cancelled" {
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return map[string]any{"stdout": "dry-run bytes"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			policy := &runtimeFlowDrillPolicy{denyNested: mode == "denied"}
			runtime, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{Authorization: policy, AdditionalTools: []tool.Tool{fixture}, Context: runtimeFlowDrillBinding(func(ctx context.Context) (context.Context, error) {
				return store.WithUserID(ctx, "fixture-owner"), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Cleanup()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if mode == "cancelled" {
				go func() {
					select {
					case <-entered:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			yaml := scopedEngineFixture
			if mode == "paused" {
				yaml = strings.Replace(yaml, "    type: tool\n    tools_selection: [nested_fixture]\n    args: {}", "    type: input\n    prompt: Choose\n    options: [yes, no]", 1)
			}
			// The distillation callback is invoked with plain HTTP-style context,
			// not a fabricated test tool.Context.
			result, err := runtime.ChatAgent.FlowRunner(ctx, yaml, nil)
			switch mode {
			case "success":
				if err != nil || result == nil || !result.Success || result.Output != "engine finished" || calls != 2 {
					t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
				}
			case "denied":
				if !errors.Is(err, errRuntimeTestDenied) || calls != 0 {
					t.Fatalf("denied calls=%d err=%v", calls, err)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) || calls != 1 {
					t.Fatalf("cancelled calls=%d err=%v", calls, err)
				}
			case "paused":
				if err != nil || result == nil || result.Success || !strings.Contains(result.Error, "first") || calls != 0 {
					t.Fatalf("paused result=%+v calls=%d err=%v", result, calls, err)
				}
			}
			for _, candidate := range runtime.InternalTools {
				if candidate.Name() == "run_flow" {
					f := candidate.(*chatRuntimeTool).binding.flowRunner.(*scopedChatFlowRunner)
					f.engine.sessions.Range(func(_, _ any) bool { t.Error("dry-run leaked session"); return true })
				}
			}
		})
	}
}

func TestChatRuntimeScopedDryRunRequiresPinnedSession(t *testing.T) {
	deps := runtimeFlowDrillDeps(t)
	deps.UseScopedEngines = true
	runtime, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Cleanup()
	if _, err := runtime.ChatAgent.FlowRunner(t.Context(), scopedEngineFixture, nil); err == nil || !strings.Contains(err.Error(), "session identity") {
		t.Fatalf("missing identity accepted: %v", err)
	}
	deps.SessionID = "pinned-session"
	pinned, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Cleanup()
	ctx := runtimeFlowDrillContext{runtimeTestToolContext{lifetime: t.Context()}}
	if _, err := pinned.ChatAgent.FlowRunner(ctx, scopedEngineFixture, nil); err == nil || !strings.Contains(err.Error(), "session mismatch") {
		t.Fatalf("foreign identity accepted: %v", err)
	}
}

type scopedEngineReportStore struct {
	store.DrillReportStore
	reports []*store.DrillReport
	err     error
}

func (s *scopedEngineReportStore) SaveReport(ctx context.Context, report *store.DrillReport) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.reports = append(s.reports, report)
	return s.err
}

func TestChatRuntimeScopedDrillReportPersistence(t *testing.T) {
	for _, fail := range []bool{false, true} {
		deps := runtimeFlowDrillDeps(t)
		deps.UseScopedEngines = true
		deps.Redactor = credentials.NewRedactor()
		deps.Redactor.AddSecret("fixture", "sensitive-fixture-value")
		reports := &scopedEngineReportStore{}
		if fail {
			reports.err = errors.New("report store unavailable")
		}
		fixture, err := functiontool.New(functiontool.Config{Name: "nested_fixture", Description: "report fixture"}, func(tool.Context, struct{}) (map[string]any, error) {
			return map[string]any{"stdout": "sensitive-fixture-value"}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{AdditionalTools: []tool.Tool{fixture}, Context: runtimeFlowDrillBinding(func(ctx context.Context) (context.Context, error) {
			ctx = store.WithServices(ctx, &store.Services{DrillReports: reports})
			return store.WithTeamFlowStore(store.WithUserID(ctx, "fixture-owner"), &runtimeFlowDrillStore{}), nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Cleanup()
		_, err = runtimeFlowDrillInvoke(runtime, t.Context(), "run_drill")
		if fail && !errors.Is(err, reports.err) || !fail && err != nil {
			t.Fatalf("persistence failure=%t err=%v", fail, err)
		}
		if len(reports.reports) != 1 {
			t.Fatalf("saved %d reports", len(reports.reports))
		}
		report := reports.reports[0]
		if report.Suite != "suite" || report.Status != "passed" || report.CreatedBy != "fixture-owner" || !json.Valid(report.ReportData) || strings.Contains(string(report.ReportData), "sensitive-fixture-value") {
			t.Fatalf("invalid or unredacted report: %+v", report)
		}
	}
}

func TestChatRuntimeScopedFlowResume(t *testing.T) {
	deps := runtimeFlowDrillDeps(t)
	deps.UseScopedEngines = true
	yaml := strings.Replace(scopedEngineFixture, "    type: tool\n    tools_selection: [nested_fixture]\n    args: {}", "    type: input\n    prompt: Choose\n    options: [yes, no]", 1)
	calls := 0
	fixture, err := functiontool.New(functiontool.Config{Name: "nested_fixture", Description: "resume fixture"}, func(tool.Context, struct{}) (map[string]any, error) {
		calls++
		return map[string]any{"stdout": "resumed"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{AdditionalTools: []tool.Tool{fixture}, Context: runtimeFlowDrillBinding(func(ctx context.Context) (context.Context, error) {
		return store.WithFlowStore(store.WithUserID(ctx, "fixture-owner"), &productionFlowStore{yaml: yaml}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Cleanup()
	var target chatRuntimeRunnable
	for _, candidate := range runtime.InternalTools {
		if candidate.Name() == "run_flow" {
			target = candidate.(chatRuntimeRunnable)
		}
	}
	ctx := runtimeFlowDrillContext{runtimeTestToolContext{lifetime: t.Context()}}
	got, err := target.Run(ctx, map[string]any{"flow_name": "flow", "parameters": map[string]string{"unused": "start"}})
	if err != nil || got["status"] != "waiting_for_input" || calls != 0 {
		t.Fatalf("pause: %v %v calls=%d", got, err, calls)
	}
	got, err = target.Run(ctx, map[string]any{"flow_name": "flow", "parameters": map[string]string{"first": "yes"}})
	if err != nil || got["status"] != "completed" || got["output"] != "engine finished" || calls != 1 {
		t.Fatalf("resume: %v %v calls=%d", got, err, calls)
	}
}

type continuationFlowModel struct {
	calls int
	text  string
}

func (m *continuationFlowModel) Name() string { return "continuation-model" }
func (m *continuationFlowModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		yield(&model.LLMResponse{Content: genai.NewContentFromText(m.text, "model"), TurnComplete: true}, nil)
	}
}

func TestChatRuntimeScopedFlowRebindAcrossAttempts(t *testing.T) {
	yaml := strings.Replace(scopedEngineFixture, "    type: tool\n    tools_selection: [nested_fixture]\n    args: {}", "    type: input\n    prompt: Choose\n    options: [yes, no]", 1)
	yaml = strings.Replace(yaml, "  - name: done\n    type: output", "  - name: think\n    type: llm\n    prompt: Say resumed\n  - name: done\n    type: output", 1)
	yaml = strings.Replace(yaml, "  - from: second\n    to: done", "  - from: second\n    to: think\n  - from: think\n    to: done", 1)
	continuation := &scopedChatFlowContinuation{}
	defer continuation.Discard()
	firstModel, nextModel := &continuationFlowModel{text: "old"}, &continuationFlowModel{text: "fresh"}
	calls := [2]int{}
	for attempt := range 2 {
		deps := runtimeFlowDrillDeps(t)
		deps.UseScopedEngines, deps.flowContinuation = true, continuation
		deps.LLM = firstModel
		if attempt == 1 {
			deps.LLM = nextModel
		}
		fixture, err := functiontool.New(functiontool.Config{Name: "nested_fixture", Description: "fresh execution"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			calls[attempt]++
			return map[string]any{"stdout": "fresh"}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		runtime, err := NewScopedChatRuntime(ctx, deps, ChatRuntimeHooks{AdditionalTools: []tool.Tool{fixture}, Context: runtimeFlowDrillBinding(func(c context.Context) (context.Context, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return store.WithFlowStore(store.WithUserID(c, "fixture-owner"), &productionFlowStore{yaml: yaml}), nil
		})})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		entry := runtime.ChatAgent.ToolIndex.GetToolEntry("run_flow")
		params := map[string]string{"unused": "start"}
		if attempt == 1 {
			params = map[string]string{"first": "yes"}
		}
		got, err := entry.Tool.(chatRuntimeRunnable).Run(runtimeFlowDrillContext{runtimeTestToolContext{lifetime: ctx}}, map[string]any{"flow_name": "flow", "parameters": params})
		runtime.Cleanup()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 && got["status"] != "waiting_for_input" {
			t.Fatalf("pause: %v", got)
		}
		if attempt == 1 && got["status"] != "completed" {
			t.Fatalf("resume: %v", got)
		}
		for _, sess := range continuation.sessions {
			if sess.runner != nil {
				t.Fatal("retained execution authority")
			}
		}
	}
	if calls[0] != 0 || calls[1] != 1 || firstModel.calls != 0 || nextModel.calls != 1 {
		t.Fatalf("stale dependencies: tools=%v models=%d/%d", calls, firstModel.calls, nextModel.calls)
	}
}

func TestChatRuntimeScopedFlowCancellationCleanup(t *testing.T) {
	deps := runtimeFlowDrillDeps(t)
	deps.UseScopedEngines = true
	entered := make(chan struct{})
	calls := 0
	fixture, err := functiontool.New(functiontool.Config{Name: "nested_fixture", Description: "blocking fixture"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
		calls++
		if calls == 1 {
			close(entered)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{AdditionalTools: []tool.Tool{fixture}, Context: runtimeFlowDrillBinding(func(ctx context.Context) (context.Context, error) {
		return store.WithFlowStore(store.WithUserID(ctx, "fixture-owner"), &productionFlowStore{yaml: scopedEngineFixture}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := runtimeFlowDrillInvoke(runtime, ctx, "run_flow"); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not enter nested tool")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not stop")
	}
	if calls != 1 {
		t.Fatalf("ran next node after cancellation: %d", calls)
	}
	for _, candidate := range runtime.InternalTools {
		if candidate.Name() == "run_flow" {
			f := candidate.(*chatRuntimeTool).binding.flowRunner.(*scopedChatFlowRunner)
			count := 0
			f.engine.sessions.Range(func(_, _ any) bool { count++; return true })
			if count != 0 {
				t.Fatalf("leaked %d child sessions", count)
			}
			runtime.Cleanup()
			runtime.Cleanup()
			if f.lifetime.Err() == nil {
				t.Fatal("runtime cleanup did not close engine")
			}
		}
	}
}
