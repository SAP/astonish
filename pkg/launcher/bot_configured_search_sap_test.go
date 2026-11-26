package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

// Exercise real SAP authentication, deployment discovery and generation rather
// than replacing the configured provider factory with a successful fake model.
func TestBotConfiguredSearchSAPProductionParity(t *testing.T) {
	for _, turn := range []string{"initial-mandated", "followup", "authentication-failure", "deployment-failure", "transient-deployment-timeout"} {
		t.Run(turn, func(t *testing.T) {
			f, runtime, episode := researchFixture(t)
			f.req.Run.IdempotencyKey = "owner-initial"
			if turn == "followup" {
				f.req.Run.IdempotencyKey = "owner-followup"
			}
			authCalls, deploymentCalls, generationCalls := 0, 0, 0
			var discoveryToken string
			var timeoutNextDeployment atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/oauth/token":
					authCalls++
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if r.Form.Get("client_id") != "scoped-client" || r.Form.Get("client_secret") != "scoped-secret" {
						t.Error("wrong SAP credentials")
					}
					if turn == "authentication-failure" {
						http.Error(w, "scoped-secret", http.StatusUnauthorized)
						return
					}
					fmt.Fprintf(w, `{"access_token":"fixture-token-%d","expires_in":3600}`, authCalls)
				case "/v2/lm/deployments":
					deploymentCalls++
					if timeoutNextDeployment.Swap(false) {
						// Real SAP's 10s http.Client timeout, not a canceled parent
						// context or a synthetic successful provider factory.
						<-r.Context().Done()
						return
					}
					discoveryToken = fmt.Sprintf("Bearer fixture-token-%d", authCalls)
					if r.Header.Get("Authorization") != discoveryToken || r.Header.Get("AI-Resource-Group") != "default" {
						t.Error("wrong SAP deployment authority")
					}
					if turn == "deployment-failure" {
						http.Error(w, "scoped-secret", http.StatusForbidden)
						return
					}
					fmt.Fprint(w, `{"resources":[{"id":"search-deployment","status":"RUNNING","details":{"resources":{"backendDetails":{"model":{"name":"sonar-pro"}}}}}]}`)
				case "/v2/inference/deployments/search-deployment/chat/completions":
					generationCalls++
					if r.Header.Get("Authorization") != discoveryToken || r.Header.Get("AI-Resource-Group") != "default" {
						t.Error("inference did not reuse this provider's discovery authority")
					}
					var req map[string]any
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					if req["model"] != "sonar-pro" {
						t.Errorf("wrong search model: %v", req["model"])
					}
					raw, _ := json.Marshal(req)
					if strings.Contains(string(raw), "private-assignment-marker") {
						t.Error("private instructions sent to search")
					}
					fmt.Fprint(w, `{"id":"search","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Sources: https://example.com/decisive"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`)
				default:
					t.Errorf("unexpected SAP request: %s", r.URL.Path)
					http.Error(w, "unexpected", http.StatusNotFound)
				}
			}))
			defer server.Close()
			f.services.PlatformSettings = botKnowledgePlatformSettings{row: &store.PlatformSettings{WebSearchTool: "perplexity:perplexity_web_search", PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "SAP AI Core", Model: "sonar-pro"}, Providers: map[string]store.ProviderConfig{"SAP AI Core": {"type": "sap_ai_core", "client_id": "scoped-client", "client_secret": "scoped-secret", "auth_url": server.URL, "base_url": server.URL, "resource_group": "default"}}}}
			f.services.OrgSettings = botKnowledgeOrgSettings{row: &store.OrgSettings{}}
			f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{}}
			route, err := BotEffectiveConfiguredSearchRoute(f.ctx, f.services)
			if err != nil {
				t.Fatal(err)
			}
			runtime.m.PublicResearch.Route = route
			// researchFixture's authorization is immutable; verify the replacement route.
			auth, err := execution.VerifyBotMandateAuthorization(f.ctx, runtime, runtime.m.Ref, runtime.m.Purpose)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Mandate.Authorization = auth
			f.req.Run.MandateRef = &runtime.m.Ref
			f.req.Principal = auth.Principal(f.req.Principal.Surface)
			f.ctx, _ = execution.WithPrincipal(f.ctx, f.req.Principal)
			f.ctx = store.WithServices(provider.WithConfinedProviderSecrets(f.ctx), f.services)
			cfg := provider.ResolveEffectiveConfig(f.ctx, f.services.PlatformSettings, f.services.OrgSettings, f.services.Settings)
			chat, err := tools.NewPerplexityWebSearchTool(cfg, provider.GetProvider)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{BotConfiguredSearchToolName, "web_fetch"}})
			if err != nil {
				t.Fatal(err)
			}
			tc := optionalKnowledgeContext{knowledgeCtx(f)}
			chatResult, chatErr := chat.(botRunnableTool).Run(tc, map[string]any{"query": "Stock Market News"})
			failureReason := ""
			switch turn {
			case "authentication-failure":
				failureReason = "SAP token exchange failed: HTTP 401"
			case "deployment-failure":
				failureReason = "failed to list deployments: 403 Forbidden"
			}
			if failureReason == "" && chatErr != nil {
				t.Fatalf("ordinary Chat: %v", chatErr)
			}
			if failureReason != "" && (chatErr == nil || !strings.Contains(chatErr.Error(), failureReason)) {
				t.Fatalf("ordinary Chat lost initialization diagnostic: %v", chatErr)
			}
			if episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/decisive") == nil {
				t.Fatal("detail allowed before search")
			}
			search, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), provider.GetProvider, nil, episode)
			if err != nil {
				t.Fatal(err)
			}
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				return &BotChatSafeResources{ProviderFactory: provider.GetProvider, ChatConfig: cfg, Catalog: &ChatCatalogResources{Pool: &remainingModePool{client: &fileOutcomeNode{}}, Groups: map[string]*agent.ToolGroup{"configured_search": {Name: "configured_search", Tools: []tool.Tool{search}}}}, Research: episode, Tools: []tool.Tool{search}, WorkspaceDir: "/isolated", PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}, Mandate: f.req.Mandate}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			timeoutNextDeployment.Store(turn == "transient-deployment-timeout")
			entry := resources.Agent.ToolIndex.GetToolEntry(BotConfiguredSearchToolName)
			if entry == nil {
				t.Fatal("shared catalog missing configured search")
			}
			// The shared catalog owns configured_search, so retain its scoped wrapper
			// but exercise the standalone adapter (including its SAP diagnostics).
			shared, ok := entry.Tool.(*chatRuntimeTool)
			if !ok || entry.GroupName != "configured_search" {
				t.Fatalf("configured search is not owned by the shared scoped catalog: %+v", entry)
			}
			shared.chatRuntimeRunnable = search.(chatRuntimeRunnable)
			result, err := entry.Tool.(botRunnableTool).Run(tc, map[string]any{"facet": "overview"})
			if failureReason != "" {
				if err == nil || !strings.Contains(err.Error(), failureReason) {
					t.Fatalf("Bot lost initialization diagnostic (Chat reports %q): %v", failureReason, err)
				}
				if strings.Contains(err.Error(), "scoped-secret") || strings.Contains(err.Error(), server.URL) {
					t.Fatalf("Bot leaked provider configuration: %v", err)
				}
				wantDeployments := 2
				if turn == "authentication-failure" {
					wantDeployments = 0
				}
				if authCalls != 2 || deploymentCalls != wantDeployments || runtime.calls != 1 {
					t.Fatalf("nontransient initialization retried: auth=%d deployment=%d quota=%d", authCalls, deploymentCalls, runtime.calls)
				}
				if generationCalls != 0 || episode.State() == BotResearchSearched || episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/decisive") == nil {
					t.Fatal("failed initialization generated content or unlocked detail")
				}
				return
			}
			if err != nil {
				t.Fatalf("Bot configured search: %v", err)
			}
			for _, result := range []map[string]any{chatResult, result} {
				raw, _ := json.Marshal(result)
				if !strings.Contains(string(raw), "https://example.com/decisive") {
					t.Fatalf("no source: %s", raw)
				}
			}
			// Each Chat/Bot provider authenticates once for discovery and reuses
			// that token for inference; failed discovery creates a fresh provider.
			wantAuth, wantDeployment := 2, 2
			if turn == "transient-deployment-timeout" {
				wantAuth, wantDeployment = 3, 3
			}
			if authCalls != wantAuth || deploymentCalls != wantDeployment || generationCalls != 2 || runtime.calls != 1 {
				t.Fatalf("SAP calls auth=%d deployment=%d generation=%d", authCalls, deploymentCalls, generationCalls)
			}
			if episode.State() != BotResearchSearched || episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/decisive") != nil {
				t.Fatal("successful search did not unlock detail")
			}
			if usage := f.req.Budget.(*bot.SharedRunBudget).Usage(); usage.InputTokens != 3 || usage.OutputTokens != 5 {
				t.Fatalf("search accounting: %+v", usage)
			}
		})
	}
}
