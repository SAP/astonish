package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

type configuredSearchWorkerModel struct {
	workerTestModel
	step int
	url  string
}

func (m *configuredSearchWorkerModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(r, nil); ok {
		return response
	}
	m.step++
	if m.step > 2 {
		return m.workerTestModel.GenerateContent(ctx, r, stream)
	}
	name, args := launcher.BotConfiguredSearchToolName, map[string]any{"facet": "overview"}
	if m.step == 2 {
		name, args = "web_fetch", map[string]any{"url": m.url}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		call := &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}
		if _, direct := r.Tools[name]; !direct {
			call.Name, call.Args = "execute_tool", map[string]any{"name": name, "arguments": args}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
	}
}

type configuredSearchEngineFixture struct {
	calls *int
	url   string
}

func (configuredSearchEngineFixture) Name() string { return "search-fixture" }
func (m configuredSearchEngineFixture) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	*m.calls++
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Sources: "+m.url, "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 3, CandidatesTokenCount: 5}}, nil)
	}
}

// Real owner HTTP admission -> production worker -> guarded ChatAgent -> exact
// configured engine -> detail HTTP. No stored run pin or mandate is forged.
func TestBotConfiguredSearchHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := t.Context()
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "search", Name: "Search", Status: "active"}
			if err = db.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err = db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			ods, err := db.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err = ods.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err = db.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err = db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			repo := ods.ForTeam(team.Slug).Bots()
			pin := ods.ForTeam(team.Slug).SetSessionPin
			if scope == store.BotScopePersonal {
				repo = ods.ForUser(owner.String()).Bots()
				pin = ods.ForUser(owner.String()).SetSessionPin
			}
			auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			searchCalls, detailCalls := 0, 0
			detail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				detailCalls++
				if searchCalls != 1 {
					t.Error("detail before exact configured search")
				}
				fmt.Fprint(w, "<html><body>Decisive primary source details.</body></html>")
			}))
			defer detail.Close()
			settings := store.TeamSettings{WebSearchTool: "perplexity:perplexity_web_search", Providers: map[string]store.ProviderConfig{"search": {"api_key": "owned-fixture-secret"}}}
			if err = db.PlatformSettings().Save(auth, &store.PlatformSettings{PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "search", Model: "sonar"}}); err != nil {
				t.Fatal(err)
			}
			if err = ods.ForTeam(team.Slug).Settings().Save(auth, &settings); err != nil {
				t.Fatal(err)
			}
			route, err := launcher.BotEffectiveConfiguredSearchRoute(auth, &store.Services{OrgSettings: db.OrgSettings(org.Slug), PlatformSettings: db.PlatformSettings(), Settings: ods.ForTeam(team.Slug).Settings()})
			if err != nil {
				t.Fatal(err)
			}
			policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, e := authorizedHTTPPolicy(c, ref)
				// This fixture exercises search then detail, not token exhaustion.
				// Leave room for the full guarded prompt and synthesis reserve;
				// retain finite per-run and aggregate ceilings on this NEW grant.
				p.Limits.MaxTokens = 200000
				p.Limits.MaxTotalTokens = 400000
				p.Constraints = append(p.Constraints, execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: []string{launcher.BotConfiguredSearchToolName, "web_fetch", detail.URL}})
				p.PublicResearch = &execution.BotPublicResearchScope{Version: 1, Route: route, Facets: []execution.BotResearchFacet{execution.BotResearchOverview}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 2}
				return p, e
			}
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			defer registry.Close()
			llm := &configuredSearchWorkerModel{url: detail.URL}
			backend := newRegressionHomeBackend(t)
			if err := db.InstallBotContentHomeBackend(backend); err != nil {
				t.Fatal(err)
			}
			pool := &titleSkillPool{url: detail.URL}
			worker, err := NewBotWorker(BotWorkerConfig{TurnRegistry: registry, Backend: db, MandatePolicy: policy, Sandbox: backend, Pool: pool, WorkerID: "search-http", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				s.Services.BotFilesFactory = func(c context.Context, svc *store.Services, scope store.BotScope) (store.BotFiles, error) {
					return productionBotFiles(c, backend, svc, scope)
				}
				episode, e := launcher.NewBotResearchEpisode(q.Run.ID, q.Run.SessionID.String(), launcher.BotResearchOpenWeb, nil)
				if e != nil {
					return nil, e
				}
				reads, e := launcher.BotMandatedReadTools(q.Mandate, tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }})
				if e != nil {
					return nil, e
				}
				search, e := launcher.BotConfiguredResearchTool(c, q, s, func(c context.Context, p, m string, cfg *config.AppConfig) (model.LLM, error) {
					if !provider.IsConfinedProviderSecrets(c) || p != "search" || m != "sonar" {
						return nil, store.ErrBotForbidden
					}
					return configuredSearchEngineFixture{calls: &searchCalls, url: detail.URL}, nil
				}, nil, episode)
				if e != nil {
					return nil, e
				}
				reads = append(reads, search)
				// Only configured research extends Chat's catalog; ordinary web_fetch
				// stays the shared pool-backed executable. The read adapter supplies
				// mandate classification, never a replacement builtin.
				return &launcher.BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool, Groups: map[string]*agent.ToolGroup{"bot_research": {Name: "bot_research", Tools: []tool.Tool{search}}}}, Research: episode, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.WithBotMandatedReadClassifier(launcher.BotCatalogueClassifier{}, reads)}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			v := store.DefaultBotVersion()
			v.Charter = "Research public sources"
			v.Budget.MaxTokens = nil
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Search", OwnerID: owner, Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = worker.provisionHome(auth, repo, b.ID, b.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if err = pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "owned search acceptance"})
			if err != nil {
				t.Fatal(err)
			}
			svc := &store.Services{Bots: repo, BotMandatePolicy: policy, BotTurnRegistry: registry}
			if scope == store.BotScopePersonal {
				svc.PersonalBots = repo
			}
			httpCtx := store.WithServices(store.WithTenantContext(auth, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug}), svc)
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
			defer server.Close()
			body := fmt.Sprintf(`{"version":%d,"idempotency_key":"search","instructions":"Public research subject: Stock Market News\nResearch decisive sources."}`, b.Version)
			resp, err := server.Client().Post(server.URL+"/bots/"+b.ID.String()+"/messages?scope="+string(scope), "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 202 {
				t.Fatalf("admit %d %s", resp.StatusCode, raw)
			}
			var receipt store.BotMessageReceipt
			if err = json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			if err = worker.execute(auth, repo, ods.ForTeam(team.Slug).Sessions(), b.ID, receipt.Run.ID); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.GetRun(auth, b.ID, receipt.Run.ID)
			if err != nil || saved.Status != store.RunSucceeded || searchCalls != 1 || detailCalls != 1 || pool.calls != 1 {
				t.Fatalf("worker search ordering: run=%+v err=%v search=%d detail=%d pool=%d", saved, err, searchCalls, detailCalls, pool.calls)
			}
			t.Run("restart-receipts", func(t *testing.T) {
				worker.Stop()
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				_, restoredDB, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
				if err != nil {
					t.Fatal(err)
				}
				defer restoredDB.Close()
				if err := restoredDB.InstallBotContentHomeBackend(backend); err != nil {
					t.Fatal(err)
				}
				restoredOrg, err := restoredDB.ForOrg(org.Slug)
				if err != nil {
					t.Fatal(err)
				}
				restoredBots, restoredSessions := restoredOrg.ForTeam(team.Slug).Bots(), restoredOrg.ForTeam(team.Slug).Sessions()
				if scope == store.BotScopePersonal {
					restoredBots, restoredSessions = restoredOrg.ForUser(owner.String()).Bots(), restoredOrg.ForUser(owner.String()).Sessions()
				}
				restoredRun, err := restoredBots.GetRun(auth, b.ID, receipt.Run.ID)
				if err != nil || restoredRun.MandateRef == nil || *restoredRun.MandateRef != *saved.MandateRef {
					t.Fatalf("restored run pin: %+v %v", restoredRun, err)
				}
				events, err := restoredSessions.ReadTranscriptEvents(auth, "astonish", owner.String(), receipt.Run.SessionID.String())
				if err != nil {
					t.Fatal(err)
				}
				foundSearch, foundDetail := false, false
				// Chat persists deferred calls under execute_tool. Recover the exact
				// target from its matching call, rather than assuming direct tools.
				targets := map[string]string{}
				for _, event := range events {
					if event.Content == nil {
						continue
					}
					for _, part := range event.Content.Parts {
						if call := part.FunctionCall; call != nil && call.Name == "execute_tool" {
							targets[call.ID], _ = call.Args["name"].(string)
						}
						if part.FunctionResponse == nil {
							continue
						}
						name := part.FunctionResponse.Name
						if name == "execute_tool" {
							name = targets[part.FunctionResponse.ID]
						}
						switch name {
						case launcher.BotConfiguredSearchToolName:
							payload, err := json.Marshal(part.FunctionResponse.Response)
							if err != nil || !strings.Contains(string(payload), detail.URL) || strings.Contains(string(payload), "owned-fixture-secret") {
								t.Fatalf("search receipt not retained safely: %s %v", payload, err)
							}
							foundSearch = true
						case "web_fetch":
							payload, err := json.Marshal(part.FunctionResponse.Response)
							if err != nil || !strings.Contains(string(payload), "Decisive primary source details.") || strings.Contains(string(payload), "owned-fixture-secret") {
								t.Fatalf("detail receipt not retained safely: %s %v", payload, err)
							}
							if !foundSearch {
								t.Fatal("restored detail precedes search receipt")
							}
							foundDetail = true
						}
					}
				}
				if !foundSearch || !foundDetail {
					t.Fatalf("missing durable search/detail journals: search=%v detail=%v events=%d", foundSearch, foundDetail, len(events))
				}
				restoredEpisode, err := launcher.NewBotResearchEpisode(restoredRun.ID, restoredRun.SessionID.String(), launcher.BotResearchOpenWeb, nil)
				if err != nil || restoredEpisode.AllowDetail(auth, restoredRun.ID, restoredRun.SessionID.String(), detail.URL) == nil {
					t.Fatal("restored transcript became search authority", err)
				}
			})
		})
	}
}
