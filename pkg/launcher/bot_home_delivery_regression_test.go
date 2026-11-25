package launcher

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

// Regression: the saved Nov 7 session asked owner/team and Telegram/email before
// making any tool call. Inspect the actual interactive catalog, not the separate
// autonomous BotScheduleTools fixture that already passed before this fix.
func TestInteractiveBotScheduleUsesHomeAdapter(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
	f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
	f.req.Authorization = interactiveFactoryAuthorization{}
	f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
		return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	for _, candidate := range factoryCatalogTools(t, got) {
		if candidate.Name() != "schedule_job" {
			continue
		}
		req := &model.LLMRequest{}
		if err := candidate.(botRunnableTool).ProcessRequest(botFactoryHandoffContext{base: f.ctx, session: f.req.Run.SessionID.String()}, req); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if strings.Contains(text, "ALWAYS ask") || strings.Contains(text, "Ask the user which channel") || !strings.Contains(text, "home chat") {
			t.Fatalf("ordinary scheduler leaked into Bot request: %s", text)
		}
		return
	}
	t.Fatal("schedule_job missing")
}

func TestInteractiveBostonScheduleReviewedHomeOwnership(t *testing.T) {
	f, repo := scheduleFixture(t, store.BotScopePersonal)
	f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
	f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
	f.req.Authorization = interactiveFactoryAuthorization{}
	f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
		return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}
	r, c := scheduleFactory(t, f, repo)
	args := map[string]any{"name": "Boston weather", "mode": "adaptive", "schedule": "* * * * *", "instructions": "Report current Boston weather in the same format as the preceding weather answer."}
	// The tool-level approval and subsequent owner configuration review are
	// independent. Exercise the real bridge with one exact, consumable grant.
	raw, _ := json.Marshal(args)
	digest, err := (execution.ActionDescriptor{Tool: "schedule_job", Arguments: raw, Effect: execution.EffectUnknown}).Digest()
	if err != nil {
		t.Fatal(err)
	}
	grant := execution.ScopedGrant{ID: uuid.New(), Identity: f.req.Policy.Identity(), ActionDigest: digest, ExpiresAt: time.Now().Add(time.Minute)}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: grant.Identity, Level: execution.AutonomySupervised, AllowedTools: []string{"schedule_job"}, Grants: []execution.ScopedGrant{grant}})
	if err != nil {
		t.Fatal(err)
	}
	guard := &planExactGrantGuard{grants: map[string]execution.ScopedGrant{digest: grant}, calls: map[string]int{}}
	c.base = agent.WithBotPolicyRuntime(execution.WithRunPolicy(c.base, policy), agent.BotPolicyRuntime{Guard: guard, Classifier: BotCatalogueClassifier{}})
	review := decodeScheduleReview(t, basicCall(t, r, c, "schedule_job", args))
	if guard.calls["schedule_job"] != 1 || len(guard.grants) != 0 {
		t.Fatal("schedule did not consume exact approval")
	}
	if review.Review.Body.Definition.Enabled || review.Review.Body.Definition.ChannelBinding != nil {
		t.Fatal("schedule unexpectedly activates or selects transport")
	}
	before, _ := repo.GetVersion(f.ctx, f.req.Bot.ID, f.req.Version.Version)
	if len(before.Triggers) != 0 {
		t.Fatal("schedule created before review")
	}
	r.Cleanup()
	saved, err := applyScheduleReview(f.ctx, repo, f.req.Bot.ID, review)
	if err != nil {
		t.Fatal(err)
	}
	v, err := repo.GetVersion(f.ctx, saved.ID, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Triggers) != 1 || v.Triggers[0].Enabled || v.Triggers[0].ScheduleID == nil || len(v.Delivery.Bindings) != 0 {
		t.Fatalf("wrong saved target: %+v", v)
	}
	if saved.ID != f.req.Bot.ID {
		t.Fatal("cross-Bot schedule")
	}
	external := 0
	delivery := scheduler.WithoutBotTriggerDelivery(func(context.Context, *scheduler.Job, string, error) error { external++; return nil })
	if err := delivery(f.ctx, &scheduler.Job{Mode: scheduler.ModeBotTrigger}, "Boston weather", nil); err != nil {
		t.Fatal(err)
	}
	if external != 0 {
		t.Fatal("Bot tick reached external scheduler callback")
	}
}
