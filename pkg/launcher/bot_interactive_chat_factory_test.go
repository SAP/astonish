package launcher

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"testing"
	"time"
)

type interactiveFactoryAuthorization struct{}

func (interactiveFactoryAuthorization) Authorize(ctx context.Context, _ execution.Capability) error {
	return ctx.Err()
}
func (interactiveFactoryAuthorization) AuthorizeTool(ctx context.Context, _ string) error {
	return ctx.Err()
}

func TestEmployeeWorkerInteractiveDispatchBeforeEffect(t *testing.T) {
	for _, private := range []bool{false, true} {
		f := newBotFactoryFixture(t)
		setBotSharedInteractive(f)
		guard := &employeeInteractiveApprovalGuard{}
		got, err := f.construct(t)
		if err != nil {
			t.Fatal(err)
		}
		defer got.Cleanup()
		binding := *botFactoryModelForTest(t, got.Agent.LLM).binding
		binding.workerOnly = private
		binding.owner = agent.WithBotPolicyRuntime(binding.owner, agent.BotPolicyRuntime{Guard: guard, Classifier: botFactoryTestClassifier{}})
		calls := 0
		target, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "read"}, func(tool.Context, map[string]any) (map[string]any, error) { return nil, nil })
		if err != nil {
			t.Fatal(err)
		}
		wrapped := &interactiveBotTool{botRunnableTool: target.(botRunnableTool), binding: &binding, next: func(_ tool.Context, _ any) (map[string]any, error) {
			calls++
			return map[string]any{"content": "effect"}, nil
		}}
		_, err = wrapped.Run(employeeInteractiveToolContext{botFactoryHandoffContext{base: f.ctx, session: f.req.Run.SessionID.String()}}, map[string]any{"path": "/exact"})
		if private {
			var blocked *agent.BotPolicyError
			if !errors.As(err, &blocked) || !blocked.ApprovalRequired() || calls != 0 || guard.calls != 1 {
				t.Fatalf("private before effect: err=%v effects=%d guards=%d", err, calls, guard.calls)
			}
		} else if err != nil || calls != 1 || guard.calls != 0 {
			t.Fatalf("generic changed: err=%v effects=%d guards=%d", err, calls, guard.calls)
		}
	}
}

func TestInteractiveBotCatalogNoAutonomyTools(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
	f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
	f.req.Authorization = interactiveFactoryAuthorization{}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised})
	if err != nil {
		t.Fatal(err)
	}
	f.req.Policy = policy
	f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
		return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	if got.Agent.FlowRunner == nil || got.Agent.FlowDistiller == nil {
		t.Fatal("missing scoped distillation callbacks")
	}
	result, err := got.Agent.FlowRunner(context.Background(), "description: scoped test\nnodes:\n  - name: hello\n    type: output\n    prompt: Hello owner\nflow:\n  - from: START\n    to: hello\n  - from: hello\n    to: END\n", nil)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("scoped dry run failed: %+v %v", result, err)
	}
	names := map[string]bool{}
	for _, name := range []string{"read_file", "web_fetch", "ask_user", "run_flow", "distill_flow", "email_send", "browser_navigate"} {
		entry := got.Agent.ToolIndex.GetToolEntry(name)
		if entry == nil {
			continue
		}
		candidate := entry.Tool
		names[candidate.Name()] = true
		if candidate.Name() == "read_file" || candidate.Name() == "web_fetch" {
			wrapped, ok := candidate.(*chatRuntimeTool)
			if !ok || wrapped.binding == nil || wrapped.binding.hooks.Dispatch == nil {
				t.Fatalf("unguarded %s", candidate.Name())
			}
		}
	}
	for _, name := range []string{"ask_user", "employee_chat"} {
		if got.Agent.ToolIndex.GetToolEntry(name) == nil {
			t.Errorf("Employee catalog missing %s", name)
		}
	}
	for _, name := range []string{"read_file", "web_fetch", "run_flow", "distill_flow"} {
		if got.Agent.ToolIndex.GetToolEntry(name) != nil {
			t.Errorf("Employee inherited private-worker executable %s", name)
		}
	}
	if names["email_send"] || names["browser_navigate"] {
		t.Fatal("host transport inherited")
	}
	f.req.Authorization = nil
	if _, err = f.construct(t); err == nil {
		t.Fatal("interactive flag admitted without authority")
	}
}
