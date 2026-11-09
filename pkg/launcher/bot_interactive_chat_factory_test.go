package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
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
	for _, candidate := range got.Agent.Tools {
		names[candidate.Name()] = true
		if candidate.Name() == "read_file" || candidate.Name() == "web_fetch" {
			if _, ok := candidate.(*interactiveBotTool); !ok {
				t.Fatalf("unguarded %s", candidate.Name())
			}
		}
	}
	for _, name := range []string{"read_file", "web_fetch", "ask_user", "run_flow", "distill_flow"} {
		if !names[name] {
			t.Errorf("shared catalog missing %s", name)
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
