package launcher

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// Inspect the real factory's provider request without changing the owner's turn
// or teaching a controlled provider to choose tools based on prompt substrings.
func TestBotFactoryRoutineScheduleConsent(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
	f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
	f.req.Authorization = interactiveFactoryAuthorization{}
	f.req.Instructions = "send this here every minute"
	f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
		return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &interactiveOutcomePool{node: &fileOutcomeNode{}}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(factory)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil || result.Status != store.RunSucceeded {
		t.Fatalf("status=%s err=%v", result.Status, err)
	}
	if len(f.resolver.model.requests) != 1 {
		t.Fatalf("model calls=%d", len(f.resolver.model.requests))
	}
	req := f.resolver.model.requests[0]
	var system string
	for _, p := range req.Config.SystemInstruction.Parts {
		system += p.Text
	}
	for _, want := range []string{"An explicit owner request for recurring work within the current bounded mandate", "call schedule_job", "do not ask for redundant frequency confirmation", "even every minute", "minimum interval and budgets", "unknown destination", "RequireAuthorization", "pending worker commit"} {
		if !strings.Contains(system, want) {
			t.Errorf("effective system prompt missing %q", want)
		}
	}
	if strings.Contains(system, "Background responsibilities remain separate and require review.") {
		t.Error("unconditional review in provider prompt")
	}
	foundUser := false
	for _, content := range req.Contents {
		for _, p := range content.Parts {
			foundUser = foundUser || content.Role == "user" && p.Text == f.req.Instructions
			if strings.Contains(p.Text, "do not ask for redundant frequency confirmation") {
				t.Error("system scheduling guidance injected into conversation")
			}
		}
	}
	if !foundUser {
		t.Fatal("natural owner turn was rewritten or missing")
	}

	// Discovery and schema descriptions must agree with the system instruction.
	constructed, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer constructed.Cleanup()
	foundTool := false
	for _, candidate := range constructed.Agent.Tools {
		if candidate.Name() != "schedule_job" {
			continue
		}
		foundTool = true
		for _, want := range []string{"explicit owner request", "current bounded mandate", "do not ask for redundant frequency confirmation", "minimum interval and budgets", "pending receipt"} {
			if !strings.Contains(candidate.Description(), want) {
				t.Errorf("schedule description missing %q", want)
			}
		}
	}
	if !foundTool {
		t.Fatal("schedule_job missing")
	}
}
