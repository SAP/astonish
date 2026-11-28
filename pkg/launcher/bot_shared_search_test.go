package launcher

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

// A counting budget proves auxiliary search generation reserves and settles the
// same admitted run meter rather than becoming an unmetered tool implementation.
type sharedSearchBudget struct {
	botFactoryTestBudget
	reserved, settled int
	deny              bool
}

func (b *sharedSearchBudget) ReserveModel(ctx context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	b.reserved++
	if b.deny {
		return r, bot.ErrBudgetExceeded
	}
	return r, ctx.Err()
}
func (b *sharedSearchBudget) SettleModel(ctx context.Context, _ uuid.UUID, _ execution.BudgetUsage) error {
	b.settled++
	return ctx.Err()
}

func TestBotScopedChatConfigStrict(t *testing.T) {
	f := newBotFactoryFixture(t)
	knowledgeSearchSettings(f)
	cfg, err := BotScopedChatConfig(f.ctx, knowledgeScope(f))
	if err != nil || cfg.Providers["search"]["api_key"] != "team-secret" || cfg.PerplexityWebSearch.Provider != "search" {
		t.Fatalf("scoped cascade: %v %v", cfg, err)
	}
	cfg.Providers["search"]["api_key"] = "changed"
	again, err := BotScopedChatConfig(f.ctx, knowledgeScope(f))
	if err != nil || again.Providers["search"]["api_key"] != "team-secret" {
		t.Fatal("config aliases store maps")
	}
	f.services.Settings = sharedSearchFailingSettings{}
	if _, err := BotScopedChatConfig(f.ctx, knowledgeScope(f)); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("failed team settings inherited credentials: %v", err)
	}
	for _, scope := range []*BotChatScope{nil, {}, {Services: &store.Services{}}} {
		if _, err := BotScopedChatConfig(f.ctx, scope); !errors.Is(err, store.ErrBotUnsupported) {
			t.Fatalf("missing settings fallback: %v", err)
		}
	}
}

type sharedSearchFailingSettings struct{ store.SettingsStore }

func (sharedSearchFailingSettings) Get(context.Context) (*store.TeamSettings, error) {
	return nil, errors.New("unavailable")
}

func TestBotSharedSearchFailsClosed(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-secret-must-not-resolve")
	for _, scenario := range []string{"missing-factory", "ambient-secret", "nil-model", "factory-error"} {
		t.Run(scenario, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			// The ambient-secret scenario must reach provider.GetProvider's env-var
			// resolution and fail closed, so configure the scoped provider as a
			// concrete openai provider in every settings layer before capturing the
			// research route, keeping the route fingerprint consistent.
			openaiProvider := map[string]store.ProviderConfig{"search": {"type": "openai"}}
			f.services.PlatformSettings = botKnowledgePlatformSettings{row: &store.PlatformSettings{WebSearchTool: "perplexity:perplexity_web_search", PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "search", Model: "sonar"}, Providers: openaiProvider}}
			f.services.OrgSettings = botKnowledgeOrgSettings{row: &store.OrgSettings{Providers: openaiProvider}}
			f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{Providers: openaiProvider}}
			route, err := BotEffectiveConfiguredSearchRoute(f.ctx, f.services)
			if err != nil {
				t.Fatal(err)
			}
			identity := f.req.Policy.Identity()
			m := execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: identity.BotID, OrgSlug: identity.OrgSlug, TeamSlug: identity.TeamSlug, OwnerID: identity.ActorID, Revision: 1}, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{string(execution.CapabilityToolExecute)}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{BotConfiguredSearchToolName, "web_fetch"}}}, Limits: execution.BotMandateLimits{MinScheduleIntervalSeconds: 1, MaxSchedules: 1, MaxDailyRuns: 1, MaxConcurrentRuns: 1, MaxRuntimeSeconds: 60, MaxToolCalls: 10, MaxTokens: 10000, MaxTotalToolCalls: 100, MaxTotalTokens: 100000}, PublicResearch: &execution.BotPublicResearchScope{Version: 1, Subject: "Stock Market News", SourceEventID: "owner-event", Route: route, Facets: []execution.BotResearchFacet{execution.BotResearchOverview, execution.BotResearchRecent}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 2}}
			quota := &researchTestRuntime{m: m}
			auth, err := execution.VerifyBotMandateAuthorization(f.ctx, quota, m.Ref, m.Purpose)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: quota, Identity: identity}
			episode, err := NewBotResearchEpisode(f.req.Run.ID, f.req.Run.SessionID.String(), BotResearchOpenWeb, nil)
			if err != nil {
				t.Fatal(err)
			}
			ref := quota.m.Ref
			f.req.Run.MandateRef = &ref
			f.req.Principal = f.req.Mandate.Authorization.Principal(f.req.Principal.Surface)
			f.ctx, _ = execution.WithPrincipal(context.Background(), f.req.Principal)
			f.req.Run.IdempotencyKey = "owner-confirmed-fixture"
			f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{"perplexity_web_search", "web_fetch"}})
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := BotScopedChatConfig(f.ctx, knowledgeScope(f))
			if err != nil {
				t.Fatal(err)
			}
			prepare := f.cfg.Prepare
			sentinel := errors.New("controlled factory failure")
			f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
				safe, err := prepare(ctx, req, scope)
				if err != nil {
					return safe, err
				}
				safe.ChatConfig = cfg
				safe.PolicyRuntime.Mandate = req.Mandate
				safe.Research = episode
				switch scenario {
				case "ambient-secret":
					safe.ProviderFactory = provider.GetProvider
				case "nil-model":
					safe.ProviderFactory = func(context.Context, string, string, *config.AppConfig) (model.LLM, error) { return nil, nil }
				case "factory-error":
					safe.ProviderFactory = func(context.Context, string, string, *config.AppConfig) (model.LLM, error) { return nil, sentinel }
				}
				return safe, nil
			}
			runtime, err := f.construct(t)
			if scenario == "missing-factory" {
				if err == nil || !strings.Contains(err.Error(), "scoped provider factory") {
					t.Fatalf("missing injection accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Cleanup()
			worker, err := runtime.WorkerFactory(f.ctx, api.BotEmployeeWorkerRequest{Request: f.req, ConversationID: f.req.Run.SessionID.String(), Sessions: runtime.SessionService})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Cleanup()
			entry := worker.Agent.ToolIndex.GetToolEntry("perplexity_web_search")
			if entry == nil {
				t.Fatal("missing shared search")
			}
			workerCtx := agent.WithBotPrivateWorkerExecution(execution.WithRunPolicy(execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity()), f.req.Policy))
			_, err = entry.Tool.(chatRuntimeRunnable).Run(runtimeCapabilityToolContext{sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: workerCtx}, sid: f.req.Run.SessionID.String()}}}, map[string]any{"query": "fixture"})
			switch scenario {
			case "ambient-secret":
				if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY not set") {
					t.Fatalf("ambient secret used: %v", err)
				}
			case "nil-model":
				if !errors.Is(err, store.ErrBotUnsupported) {
					t.Fatalf("nil model accepted: %v", err)
				}
			case "factory-error":
				if !errors.Is(err, sentinel) {
					t.Fatalf("factory error lost: %v", err)
				}
			}
		})
	}
}
