package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type intentAttemptModel struct {
	workerTestModel
	attempt  string
	requests [][]*genai.Content
}

func (m *intentAttemptModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.requests = append(m.requests, append([]*genai.Content(nil), r.Contents...))
	if m.attempt == "" {
		return m.workerTestModel.GenerateContent(ctx, r, stream)
	}
	name := m.attempt
	m.attempt = ""
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: map[string]any{}}}}}}, nil)
	}
}

func TestBotIntentHTTPWorkerConversation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "intent", Name: "Intent", Status: "active"}
			if err := db.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			ods, err := db.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err := ods.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err := db.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			llm := &intentAttemptModel{}
			effects := 0
			var targets []tool.Tool
			// One physical test tool detects accidental resource reuse without
			// duplicating the factory's built-in delegation/interaction tools.
			for _, name := range []string{"read_file"} {
				target, err := functiontool.New(functiontool.Config{Name: name, Description: "Count execution"}, func(tool.Context, map[string]any) (map[string]any, error) {
					effects++
					return map[string]any{"ok": true}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				targets = append(targets, target)
			}
			prepareCalls := 0
			worker, err := NewBotWorker(BotWorkerConfig{Backend: db, Sandbox: activationHomeBackend{}, Pool: workerTestPool{}, WorkerID: "intent-worker", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(_ context.Context, q bot.RunRequest, _ *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				prepareCalls++
				if q.Run.ControlOnly() {
					t.Fatal("control-only conversation prepared physical execution resources")
				}
				return &launcher.BotChatSafeResources{Tools: targets, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: workerTestGuard{}, Classifier: botUnknownClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			auth, _, err := worker.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			repo := ods.ForTeam(team.Slug).Bots()
			ss := ods.ForTeam(team.Slug).Sessions()
			pin := ods.ForTeam(team.Slug).SetSessionPin
			svc := &store.Services{Bots: repo, Sessions: ss}
			if scope == store.BotScopePersonal {
				repo = ods.ForUser(owner.String()).Bots()
				ss = ods.ForUser(owner.String()).Sessions()
				pin = ods.ForUser(owner.String()).SetSessionPin
				svc.PersonalBots = repo
				svc.PersonalSessions = ss
			}
			v := store.DefaultBotVersion()
			v.Charter = "Reply to owner"
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Intent", OwnerID: owner, Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			if err := entstore.RegisterBotHomeBinding(registry, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "registered-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			httpCtx := store.WithServices(store.WithTenantContext(auth, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug}), svc)
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/test-run", api.BotTestRunHandler)
			router.HandleFunc("/bots/{botID}/conversation", api.BotConversationHandler)
			router.HandleFunc("/bots/{botID}/runs", api.BotRequestRunHandler)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
			defer server.Close()
			for _, intent := range []store.BotExecutionIntent{store.BotIntentDraftTest, store.BotIntentConversation} {
				route := "test-run"
				if intent == store.BotIntentConversation {
					b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "review"})
					if err != nil {
						t.Fatal(err)
					}
					b, err = repo.Pause(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "pause"})
					if err != nil {
						t.Fatal(err)
					}
					route = "conversation"
				}
				payload := fmt.Sprintf(`{"version":%d,"idempotency_key":%q,"instructions":"Owner says hello"}`, b.Version, route)
				post := func(path string) *http.Response {
					response, e := server.Client().Post(server.URL+"/bots/"+b.ID.String()+"/"+path+"?scope="+string(scope), "application/json", strings.NewReader(payload))
					if e != nil {
						t.Fatal(e)
					}
					return response
				}
				denied := post("runs")
				denied.Body.Close()
				if denied.StatusCode != 403 {
					t.Fatalf("ordinary inactive admission: %d", denied.StatusCode)
				}
				response := post(route)
				raw, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode != 202 {
					t.Fatalf("admission %d: %s", response.StatusCode, raw)
				}
				var run store.BotRun
				if err := json.Unmarshal(raw, &run); err != nil {
					t.Fatal(err)
				}
				if err := worker.Poll(ctx); err != nil {
					t.Fatal(err)
				}
				if intent == store.BotIntentConversation {
					seen := false
					for _, content := range llm.requests[len(llm.requests)-1] {
						for _, part := range content.Parts {
							if part.Text == "durable controlled result" {
								seen = true
							}
						}
					}
					if !seen {
						t.Fatal("conversation model never saw prior Bot home history")
					}
				}
				saved, err := repo.GetRun(auth, b.ID, run.ID)
				if err != nil || saved.Status != store.RunSucceeded || saved.Intent != intent {
					t.Fatalf("run %+v: %v", saved, err)
				}
				after, err := repo.Get(auth, b.ID)
				if err != nil || after.Status != b.Status {
					t.Fatal("status changed", err)
				}
				b = after
				events, err := ss.ReadTranscriptEvents(auth, "astonish", owner.String(), b.HomeSessionID.String())
				if err != nil {
					t.Fatal(err)
				}
				user, assistant := false, false
				for _, e := range events {
					if e.Content == nil {
						continue
					}
					for _, part := range e.Content.Parts {
						if part.Text == "Owner says hello" {
							user = true
						}
						if part.Text == "durable controlled result" {
							assistant = true
						}
					}
				}
				if !user || !assistant {
					t.Fatalf("home missing conversation user=%v assistant=%v events=%+v", user, assistant, events)
				}
				calls := llm.calls
				again := post(route)
				again.Body.Close()
				if again.StatusCode != 202 {
					t.Fatal("idempotency replay rejected")
				}
				if err := worker.Poll(ctx); err != nil {
					t.Fatal(err)
				}
				if calls != llm.calls {
					t.Fatal("automatic rerun")
				}
				if intent == store.BotIntentConversation {
					// A distinct follow-up must load both preceding turns exactly once,
					// and persist only the new user/model turn in the home transcript.
					payload = fmt.Sprintf(`{"version":%d,"idempotency_key":"followup","instructions":"Follow up on my prior message"}`, b.Version)
					follow := post(route)
					follow.Body.Close()
					if follow.StatusCode != 202 {
						t.Fatalf("followup: %d", follow.StatusCode)
					}
					if err := worker.Poll(ctx); err != nil {
						t.Fatal(err)
					}
					priorUsers, priorModels := 0, 0
					for _, content := range llm.requests[len(llm.requests)-1] {
						for _, part := range content.Parts {
							if part.Text == "Owner says hello" {
								priorUsers++
							}
							if part.Text == "durable controlled result" {
								priorModels++
							}
						}
					}
					if priorUsers != 2 || priorModels != 2 {
						t.Fatalf("model history users=%d models=%d", priorUsers, priorModels)
					}
					events, err = ss.ReadTranscriptEvents(auth, "astonish", owner.String(), b.HomeSessionID.String())
					if err != nil {
						t.Fatal(err)
					}
					oldUsers, followUsers, models := 0, 0, 0
					for _, event := range events {
						if event.Content == nil {
							continue
						}
						for _, part := range event.Content.Parts {
							switch part.Text {
							case "Owner says hello":
								oldUsers++
							case "Follow up on my prior message":
								followUsers++
							case "durable controlled result":
								models++
							}
						}
					}
					if oldUsers != 2 || followUsers != 1 || models != 3 {
						t.Fatalf("duplicate/missing persisted turn old=%d follow=%d models=%d", oldUsers, followUsers, models)
					}
					for _, name := range []string{"read_file", "run_flow", "delegate_tasks", "unknown_tool", "ask_user"} {
						llm.attempt = name
						payload = fmt.Sprintf(`{"version":%d,"idempotency_key":%q,"instructions":"Attempt tool"}`, b.Version, name)
						rsp := post(route)
						var attempted store.BotRun
						err := json.NewDecoder(rsp.Body).Decode(&attempted)
						rsp.Body.Close()
						if rsp.StatusCode != 202 || err != nil {
							t.Fatalf("tool attempt admission %d: %v", rsp.StatusCode, err)
						}
						if err := worker.Poll(ctx); err != nil {
							t.Fatal(err)
						}
						if llm.attempt != "" {
							t.Fatal("model was not exercised")
						}
						if prepareCalls != 1 {
							t.Fatalf("conversation prepared resources: calls=%d; only draft test may prepare", prepareCalls)
						}
						if effects != 0 {
							t.Fatalf("conversation executed %s: %d effects", name, effects)
						}
						after, err := repo.Get(auth, b.ID)
						if err != nil || after.Status != store.BotPaused {
							t.Fatal("tool attempt changed paused state", err)
						}
					}
				}
			}
		})
	}
}
