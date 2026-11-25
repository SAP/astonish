package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
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
			setBotSharedInteractive(f)
			cfg := &config.AppConfig{Providers: map[string]config.ProviderConfig{"openai": {"type": "openai"}}}
			cfg.General.WebSearchTool = "perplexity:perplexity_web_search"
			cfg.PerplexityWebSearch.Provider, cfg.PerplexityWebSearch.Model = "openai", "fixture"
			prepare := f.cfg.Prepare
			sentinel := errors.New("controlled factory failure")
			f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
				safe, err := prepare(ctx, req, scope)
				if err != nil {
					return safe, err
				}
				safe.ChatConfig = cfg
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
			entry := runtime.Agent.ToolIndex.GetToolEntry("perplexity_web_search")
			_, err = entry.Tool.(chatRuntimeRunnable).Run(runtimeCapabilityToolContext{sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: f.ctx}, sid: f.req.Run.SessionID.String()}}}, map[string]any{"query": "fixture"})
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

// This is real tool/provider HTTP execution with a controlled local model, not
// live-provider, scheduler-trigger, or physical sandbox evidence.
func TestChatRuntimeConfiguredSearchProviderParity(t *testing.T) {
	const name = "perplexity_web_search"
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"chat", "interactive-bot", "autonomous-bot"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				workspace := &retainedResultWorkspace{}
				if mode != "chat" {
					f.services.Bots = sharedTaskCaptureRepo{BotStore: f.services.Bots}
					f.services.PersonalBots = sharedTaskCaptureRepo{BotStore: f.services.PersonalBots}
				}
				budget := &sharedSearchBudget{}
				f.req.Budget = budget
				if scope == chatruntime.ResourceScopeTeam {
					f.req.Bot.Scope = store.BotScopeTeam
				}
				args := map[string]any{"query": "controlled public evidence", "max_results": float64(2)}
				guard := &drillGuard{consumed: map[uuid.UUID]bool{}}
				if mode == "interactive-bot" {
					setBotSharedInteractive(f)
				}
				if mode == "autonomous-bot" {
					drillGrants(t, f, map[string][]map[string]any{name: {args}})
				}
				var calls atomic.Int32
				key := "selected-" + string(scope) + "-search-secret"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/chat/completions" {
						http.NotFound(w, r)
						return
					}
					calls.Add(1)
					if r.Header.Get("Authorization") != "Bearer "+key {
						t.Error("wrong scoped provider credential")
					}
					var request struct {
						Model    string `json:"model"`
						Messages []struct {
							Content string `json:"content"`
						} `json:"messages"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.Model != "sonar-fixture" || len(request.Messages) == 0 || !strings.Contains(request.Messages[len(request.Messages)-1].Content, "controlled public evidence") {
						t.Errorf("wrong search request: %+v", request)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"search-fixture","object":"chat.completion","model":"sonar-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"Controlled sourced answer https://evidence.example/source"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21}}`)
				}))
				defer server.Close()
				cfg := &config.AppConfig{Providers: map[string]config.ProviderConfig{"scoped-search": {"type": "litellm", "api_key": key, "base_url": server.URL}}}
				cfg.General.WebSearchTool = "perplexity:perplexity_web_search"
				cfg.PerplexityWebSearch.Provider, cfg.PerplexityWebSearch.Model = "scoped-search", "sonar-fixture"
				f.services.PlatformSettings = botKnowledgePlatformSettings{row: &store.PlatformSettings{WebSearchTool: cfg.General.WebSearchTool, PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "scoped-search", Model: "sonar-fixture"}, Providers: map[string]store.ProviderConfig{"scoped-search": {"type": "litellm", "api_key": key, "base_url": server.URL}}}}
				f.services.OrgSettings = botKnowledgeOrgSettings{row: &store.OrgSettings{}}
				f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{}}
				var factoryCalls int
				factory := func(ctx context.Context, p, m string, selected *config.AppConfig) (model.LLM, error) {
					factoryCalls++
					principal, ok := execution.PrincipalFromContext(ctx)
					if !ok || principal.Subject != f.req.Principal.Subject || principal.TeamSlug != f.req.Principal.TeamSlug {
						t.Fatal("factory lost trusted owner scope")
					}
					if p != "scoped-search" || m != "sonar-fixture" || selected.Providers[p]["api_key"] != key {
						t.Fatal("factory lost selected config")
					}
					return provider.GetProvider(ctx, p, m, selected)
				}
				resources := ChatCatalogResources{Pool: &catalogTestPool{}}
				var chat *agent.ChatAgent
				var cleanup func()
				if mode == "chat" {
					inputs, err := chatruntime.Build(f.ctx, chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: scope, AppConfig: cfg, Pool: resources.Pool, ProviderFactory: factory, FallbackPolicy: chatruntime.FallbackForbidden})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = inputs.Close() })
					runtime, err := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: cfg}, resources, nil)(f.ctx, api.StudioChatRuntimeRequest{Inputs: inputs, AppConfig: cfg, LLM: provider.NewPlaceholderLLM(), SessionService: f.sessions, SessionID: f.req.Run.SessionID.String(), UserID: f.req.Principal.Subject, AppName: "astonish"})
					if err != nil {
						t.Fatal(err)
					}
					chat, cleanup = runtime.ChatAgent, runtime.Cleanup
				} else {
					prepare := f.cfg.Prepare
					f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
						safe, err := prepare(ctx, req, scope)
						if err == nil {
							safe.TaskWorkspace = workspace
							safe.ChatConfig, safe.Catalog, safe.ProviderFactory = cfg, &resources, factory
							safe.PolicyRuntime.Classifier = BotCatalogueClassifier{}
							safe.PolicyRuntime.Guard = guard
						}
						return safe, err
					}
					runtime, err := f.construct(t)
					if err != nil {
						t.Fatal(err)
					}
					chat, cleanup = runtime.Agent, runtime.Cleanup
				}
				t.Cleanup(cleanup)
				if factoryCalls != 0 || calls.Load() != 0 {
					t.Fatal("eager search execution")
				}
				entry := chat.ToolIndex.GetToolEntry(name)
				if entry == nil {
					t.Fatal("configured search missing")
				}
				if _, ok := entry.Tool.(*chatRuntimeTool); !ok {
					t.Fatalf("search bypassed shared wrapper: %T", entry.Tool)
				}
				invoke := func(ctx context.Context, a map[string]any) (map[string]any, error) {
					return entry.Tool.(chatRuntimeRunnable).Run(runtimeCapabilityToolContext{sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: ctx}, sid: f.req.Run.SessionID.String()}}}, a)
				}
				if mode == "autonomous-bot" {
					_, err := invoke(f.ctx, map[string]any{"query": "unapproved query"})
					var denied *agent.BotPolicyError
					if !errors.As(err, &denied) || !denied.ApprovalRequired() || calls.Load() != 0 || factoryCalls != 0 {
						t.Fatalf("unapproved search dispatched: %v", err)
					}
				}
				out, err := invoke(f.ctx, args)
				encoded, _ := json.Marshal(out)
				if err != nil || calls.Load() != 1 || factoryCalls != 1 || !strings.Contains(string(encoded), "Controlled sourced answer") || !strings.Contains(string(encoded), "https://evidence.example/source") {
					t.Fatalf("search: %s err=%v requests=%d factory=%d", encoded, err, calls.Load(), factoryCalls)
				}
				if mode != "chat" && workspace.publications != 1 {
					t.Fatalf("authorized shared search Task capture=%d want exactly one; output=%s", workspace.publications, encoded)
				}
				if mode != "chat" && (budget.reserved != 1 || budget.settled != 1) {
					t.Fatalf("search escaped run budget: reserve=%d settle=%d", budget.reserved, budget.settled)
				}
				if mode == "interactive-bot" {
					budget.deny = true
					if _, err := invoke(f.ctx, args); !errors.Is(err, bot.ErrBudgetExceeded) || calls.Load() != 1 {
						t.Fatalf("exhausted model budget dispatched: %v", err)
					}
					if factoryCalls != 2 {
						t.Fatalf("unexpected factory count after budget denial: %d", factoryCalls)
					}
				}
				factoriesBeforeDenied := factoryCalls
				cancelled, cancel := context.WithCancel(f.ctx)
				cancel()
				foreign := f.req.Principal
				foreign.TeamSlug = "foreign-team"
				foreignCtx, err := execution.WithPrincipal(t.Context(), foreign)
				if err != nil {
					t.Fatal(err)
				}
				for _, ctx := range []context.Context{cancelled, foreignCtx} {
					if _, err := invoke(ctx, args); err == nil || calls.Load() != 1 || factoryCalls != factoriesBeforeDenied {
						t.Fatalf("denied scope dispatched: %v", err)
					}
				}
				if mode == "autonomous-bot" {
					if _, err := invoke(f.ctx, args); err == nil || calls.Load() != 1 {
						t.Fatalf("consumed exact grant replayed: %v", err)
					}
				}
				if mode != "chat" {
					f.req.Run.Lease.Fence++
					if _, err := invoke(f.ctx, args); err == nil || calls.Load() != 1 {
						t.Fatalf("stale lease dispatched: %v", err)
					}
				}
				cleanup()
				cleanup()
				if mode != "chat" && (f.safeCleaned != 1 || f.scopeCleaned != 1) {
					t.Fatal("cleanup not exactly once")
				}
			})
		}
	}
}
