package daemon

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
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
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Exercise native and progressive dispatch through production composition. The
// model checks the actual empty skill page, not merely that execution continued.
type optionalSkillReadHTTPModel struct {
	workerTestModel
	t       *testing.T
	bridge  bool
	stage   int
	missing uuid.UUID
	reads   int
}

func (m *optionalSkillReadHTTPModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.stage > 0 {
			want := "ok"
			if m.stage == 2 {
				want = "unavailable"
			}
			found := false
			for _, c := range req.Contents {
				for _, p := range c.Parts {
					if p.FunctionResponse == nil {
						continue
					}
					r := p.FunctionResponse.Response
					if r["status"] == want {
						if m.stage == 1 {
							raw, _ := json.Marshal(r["result"])
							var page store.BotSkillList
							if err := json.Unmarshal(raw, &page); err != nil || len(page.Skills) != 0 {
								m.t.Fatalf("expected real empty skill page: %s %v", raw, err)
							}
						}
						found = true
					}
				}
			}
			if !found {
				raw, _ := json.Marshal(req.Contents)
				m.t.Fatalf("lawful optional read did not return %s: %s", want, raw)
			}
			m.reads++
		}
		if m.stage == 2 {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("No saved methods are available; I can continue without one.", "model"), TurnComplete: true}, nil)
			m.stage++
			return
		}
		name, args := "bot_skill_list", map[string]any{}
		if m.stage == 1 {
			name, args = "bot_skill_read", map[string]any{"skill_id": m.missing.String()}
		}
		call := &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}
		if m.bridge {
			call.Name, call.Args = "execute_tool", map[string]any{"name": name, "arguments": args}
		}
		m.stage++
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
	}
}

func TestBotOptionalSkillReadNoApprovalHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			backend, owner := setupChannelResolverFixture(t, "optional-skills", "team", "telegram", "linked")
			org, err := backend.ForOrg("optional-skills")
			if err != nil {
				t.Fatal(err)
			}
			repo, ss := org.ForTeam("team").Bots(), org.ForTeam("team").Sessions()
			pin := org.ForTeam("team").SetSessionPin
			if scope == store.BotScopePersonal {
				repo, ss = org.ForUser(owner).Bots(), org.ForUser(owner).Sessions()
				pin = org.ForUser(owner).SetSessionPin
			}
			ctx, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "optional-skills", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			defer registry.Close()
			policy := newProductionBotMandatePolicy(backend, &config.AppConfig{})
			mdl := &optionalSkillReadHTTPModel{t: t, bridge: scope == store.BotScopeTeam, missing: uuid.New()}
			worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, MandatePolicy: policy, TurnRegistry: registry, Sandbox: lifecycleHTTPBackend{}, Pool: workerTestPool{}, WorkerID: "optional-skills-http", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return mdl, nil
			}, Prepare: func(_ context.Context, q bot.RunRequest, _ *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				return &launcher.BotChatSafeResources{WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			worker.schedulerReady.Store(true)
			v := store.DefaultBotVersion()
			v.Charter = "Inspect optional saved methods"
			v.Budget.MaxTokens = nil
			b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Optional skills", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			internal, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: "optional-skills", TeamSlug: "team", Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			err = entstore.RegisterBotHomeBinding(internal, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "registered-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = pin(ctx, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			svc := &store.Services{Bots: repo, Sessions: ss, BotMandatePolicy: policy, BotTurnRegistry: registry}
			if scope == store.BotScopePersonal {
				svc.PersonalBots, svc.PersonalSessions = repo, ss
			}
			caller := store.WithServices(store.WithTenantContext(ctx, &store.TenantContext{UserID: owner, OrgSlug: "optional-skills", TeamSlug: "team"}), svc)
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(caller)) }))
			defer server.Close()
			response, err := server.Client().Post(server.URL+"/bots/"+b.ID.String()+"/messages?scope="+string(scope), "application/json", strings.NewReader(`{"version":1,"idempotency_key":"read","instructions":"Inspect saved methods, then continue without one if none exist."}`))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				t.Fatalf("admission %d: %s", response.StatusCode, raw)
			}
			var receipt store.BotMessageReceipt
			if err = json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			ownerCtx, _, err := worker.authenticateOwner(ctx, BotSchedulerScope{"optional-skills", "team", owner})
			if err != nil {
				t.Fatal(err)
			}
			if err = worker.execute(ownerCtx, repo, ss, b.ID, receipt.Run.ID); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.GetRun(ctx, b.ID, receipt.Run.ID)
			if err != nil || saved.Status != store.RunSucceeded || mdl.reads != 2 || !strings.Contains(saved.Report.Summary, "continue without one") {
				t.Fatalf("optional reads failed: run=%+v reads=%d err=%v", saved, mdl.reads, err)
			}
			approvals, err := repo.ListApprovals(ctx, b.ID, store.BotPage{})
			if err != nil || len(approvals) != 0 {
				t.Fatalf("lawful reads requested approvals: %+v %v", approvals, err)
			}
			page, err := repo.(store.BotSkillStore).ListSkills(ctx, b.ID, store.BotSkillPage{Limit: 20})
			if err != nil || len(page.Skills) != 0 {
				t.Fatalf("read created skills: %+v %v", page, err)
			}
		})
	}
}
