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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type taskHTTPBackend struct {
	lifecycleHTTPBackend
	sandbox.BotHomeBackend
}

type taskHTTPCall struct {
	name string
	args map[string]any
}
type taskHTTPModel struct {
	workerTestModel
	calls  []taskHTTPCall
	answer string
}

func (m *taskHTTPModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(r, nil); ok {
		return response
	}
	if len(m.calls) == 0 {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText(m.answer, "model"), TurnComplete: true}, nil)
		}
	}
	call := m.calls[0]
	m.calls = m.calls[1:]
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: call.name, Args: call.args}}}}}, nil)
	}
}

// Owner HTTP -> production factories/worker -> physical durable Docker Home.
// The model and public-source fixture are deterministic; no paid provider is used.
func TestBotTaskScheduleHTTPAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_TASK_EXECUTION") != "1" {
		t.Skip("explicit physical execution acceptance required")
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := t.Context()
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "task-execution", Name: "Task execution", Status: "active"}
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
			repo, ss, jobs, pin := ods.ForTeam(team.Slug).Bots(), ods.ForTeam(team.Slug).Sessions(), ods.ForTeam(team.Slug).ScheduledJobs(), ods.ForTeam(team.Slug).SetSessionPin
			if scope == store.BotScopePersonal {
				repo, ss, jobs, pin = ods.ForUser(owner.String()).Bots(), ods.ForUser(owner.String()).Sessions(), ods.ForUser(owner.String()).ScheduledJobs(), ods.ForUser(owner.String()).SetSessionPin
			}
			auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			backend, err := docker.New(docker.Config{Sessions: sandbox.NewSessionRegistryFromStore(nil), LayersDir: filepath.Join(dir, "layers"), UppersDir: filepath.Join(dir, "uppers"), SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE")})
			if err != nil {
				t.Fatal(err)
			}
			var searchCalls, detailCalls atomic.Int32
			detail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := detailCalls.Add(1)
				fmt.Fprintf(w, "<html><body>Fresh decisive source observation %d</body></html>", n)
			}))
			defer detail.Close()
			if err = db.PlatformSettings().Save(auth, &store.PlatformSettings{PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "search", Model: "sonar"}}); err != nil {
				t.Fatal(err)
			}
			if err = ods.ForTeam(team.Slug).Settings().Save(auth, &store.TeamSettings{WebSearchTool: "perplexity:perplexity_web_search", Providers: map[string]store.ProviderConfig{"search": {"api_key": "owned-fixture-secret"}}}); err != nil {
				t.Fatal(err)
			}
			route, err := launcher.BotEffectiveConfiguredSearchRoute(auth, &store.Services{OrgSettings: db.OrgSettings(org.Slug), PlatformSettings: db.PlatformSettings(), Settings: ods.ForTeam(team.Slug).Settings()})
			if err != nil {
				t.Fatal(err)
			}
			policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, e := newProductionBotMandatePolicy(db, &config.AppConfig{})(c, ref)
				p.Constraints = append(p.Constraints, execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: []string{launcher.BotConfiguredSearchToolName, "web_fetch", detail.URL, "bot_task_list", "bot_task_read", "bot_task_select", "bot_task_capture", "bot_task_update", "bot_task_asset", "bot_task_state", "bot_task_execute"}})
				p.PublicResearch = &execution.BotPublicResearchScope{Version: 1, Route: route, Facets: []execution.BotResearchFacet{execution.BotResearchOverview}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 2}
				return p, e
			}
			registry := execution.NewBotTurnAuthorizationRegistry(20)
			defer registry.Close()
			llm := &taskHTTPModel{answer: "Fresh decisive sources inspected; captured observations are not correctness guarantees."}
			tasks := repo.(store.BotTaskStore)
			workspace := &launcher.BotTaskWorkspace{Backend: backend, Bots: repo, Tasks: tasks}
			if err = workspace.Install(); err != nil {
				t.Fatal(err)
			}
			wc := BotWorkerConfig{Backend: db, MandatePolicy: policy, TurnRegistry: registry, Sandbox: taskHTTPBackend{BotHomeBackend: backend}, Pool: workerTestPool{}, WorkerID: "task-http", ContextWindow: 16384, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				if !strings.Contains(q.Instructions, "Public research subject:") {
					return &launcher.BotChatSafeResources{TaskWorkspace: workspace, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
				}
				episode, e := launcher.NewBotResearchEpisode(q.Run.ID, q.Run.SessionID.String(), launcher.BotResearchOpenWeb, nil)
				if e != nil {
					return nil, e
				}
				reads, e := launcher.BotMandatedReadTools(q.Mandate, tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }})
				if e != nil {
					return nil, e
				}
				search, e := launcher.BotConfiguredResearchTool(c, q, s, func(context.Context, string, string, *config.AppConfig) (model.LLM, error) {
					return taskSearchEngine{calls: &searchCalls, url: detail.URL}, nil
				}, nil, episode)
				if e != nil {
					return nil, e
				}
				reads = append(reads, search)
				return &launcher.BotChatSafeResources{TaskWorkspace: workspace, Research: episode, Tools: reads, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.WithBotMandatedReadClassifier(launcher.BotCatalogueClassifier{}, reads)}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}}
			worker, err := NewBotWorker(wc)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			worker.schedulerReady.Store(true)
			v := store.DefaultBotVersion()
			v.Charter = "Research public sources"
			v.Budget.MaxTokens = nil
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Retained researcher", OwnerID: owner, Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			h, err := backend.EnsureBotHome(auth, sandbox.BotHomeScope{OrgID: org.Slug, TeamID: team.Slug, UserID: owner.String(), BotID: b.ID.String()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				c, done := context.WithTimeout(context.Background(), 30*time.Second)
				defer done()
				if e := backend.DeleteBotHome(c, h); e != nil {
					t.Error(e)
				}
			})
			internal, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "owned-task-registration", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			if err = entstore.RegisterBotHomeBinding(internal, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: string(h.Backend), StorageKey: h.Ref, Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "owned execution acceptance"})
			if err != nil {
				t.Fatal(err)
			}
			svc := &store.Services{Bots: repo, Sessions: ss, BotMandatePolicy: policy, BotTurnRegistry: registry}
			if scope == store.BotScopePersonal {
				svc.PersonalBots = repo
				svc.PersonalSessions = ss
			}
			httpCtx := store.WithServices(store.WithTenantContext(auth, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug}), svc)
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
			defer server.Close()
			admit := func(text string, id *uuid.UUID) store.BotRun {
				t.Helper()
				current, e := repo.Get(auth, b.ID)
				if e != nil {
					t.Fatal(e)
				}
				if id != nil {
					text = "Public research subject: Stock Market News\n" + text
				}
				body, _ := json.Marshal(map[string]any{"version": current.Version, "idempotency_key": uuid.NewString(), "instructions": text, "task_id": id})
				resp, e := server.Client().Post(server.URL+"/bots/"+b.ID.String()+"/messages?scope="+string(scope), "application/json", strings.NewReader(string(body)))
				if e != nil {
					t.Fatal(e)
				}
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 202 {
					t.Fatalf("admission %d %s", resp.StatusCode, raw)
				}
				var receipt store.BotMessageReceipt
				if e = json.Unmarshal(raw, &receipt); e != nil {
					t.Fatal(e)
				}
				return receipt.Run
			}
			execute := func(run store.BotRun) {
				t.Helper()
				if e := worker.execute(auth, repo, ss, b.ID, run.ID); e != nil {
					t.Fatal(e)
				}
				saved, e := repo.GetRun(auth, b.ID, run.ID)
				if e != nil || saved.Status != store.RunSucceeded {
					t.Fatalf("execution %+v %v", saved, e)
				}
			}
			research := func() {
				llm.calls = []taskHTTPCall{{launcher.BotConfiguredSearchToolName, map[string]any{"facet": "overview"}}, {"web_fetch", map[string]any{"url": detail.URL}}}
			}
			t.Run("greeting", func(t *testing.T) {
				execute(admit("Hello", nil))
				rows, e := tasks.ListTasks(auth, b.ID, store.BotTaskPage{Limit: 20})
				if e != nil || len(rows.Tasks) != 0 {
					t.Fatalf("greeting captured task: %+v %v", rows, e)
				}
			})
			var task *store.BotTask
			t.Run("automatic-capture", func(t *testing.T) {
				research()
				source := admit("Public research subject: Stock Market News\nInspect decisive sources and retain honest failures.", nil)
				execute(source)
				rows, e := tasks.ListTasks(auth, b.ID, store.BotTaskPage{Limit: 20})
				if e != nil || len(rows.Tasks) != 1 {
					t.Fatalf("automatic capture: %+v %v", rows, e)
				}
				task = &rows.Tasks[0]
				if task.Status != store.BotTaskDraft || task.Head == nil {
					t.Fatalf("incomplete draft %+v", task)
				}
				revision, e := workspace.Load(auth, b.ID, *task.Head)
				if e != nil {
					t.Fatal(e)
				}
				if len(revision.Bundle) < 2 || !strings.Contains(string(revision.Bundle["TASK.md"]), "Incomplete") {
					t.Fatal("missing actual files/incomplete procedure")
				}
				physical, e := backend.ReadBotHomeWorkspace(auth, h, task.HomePath+"/TASK.md", 8192)
				if e != nil || string(physical) != string(revision.Bundle["TASK.md"]) {
					t.Fatalf("actual physical bytes %q %v", physical, e)
				}
				saved, e := repo.GetRun(auth, b.ID, source.ID)
				if e != nil || saved.Usage.ToolCalls != 2 || searchCalls.Load() != 1 || detailCalls.Load() != 1 {
					t.Fatalf("optional ritual/search accounting %+v %v", saved, e)
				}
			})
			if task == nil {
				t.Fatal("capture prerequisite failed")
			}
			old := admit("Repeat retained task", &task.ID)
			t.Run("owner-correction-pin", func(t *testing.T) {
				definition := "# Stock Market News\n\nPublic research subject: Stock Market News\n\n## Owner requirements\nInspect configured search then decisive sources. Report source failures honestly.\n\n## Procedure\nUse bot_configured_search overview then web_fetch on the actual returned source. Include source limitations.\n\n## Output\nUse concise bullet points, not a table.\n"
				llm.calls = []taskHTTPCall{{"bot_task_update", map[string]any{"content": definition}}}
				execute(admit("Correction: use concise bullet points, not a table.", &task.ID))
				current, e := tasks.GetTask(auth, b.ID, task.ID)
				if e != nil || current.Head.Revision <= old.Task.Revision {
					t.Fatalf("new corrected head %+v %v", current, e)
				}
				input, e := repo.(store.BotRuntimeStore).LoadRunInput(auth, b.ID, old.ID)
				if e != nil || input.Task.Ref != *old.Task || strings.Contains(input.Instructions, "Use concise bullet") {
					t.Fatalf("old immutable pin changed %+v %v", input, e)
				}
				task = current
				research()
				execute(old)
			})
			t.Run("manual", func(t *testing.T) {
				research()
				run := admit("Manual retained invocation", &task.ID)
				input, e := repo.(store.BotRuntimeStore).LoadRunInput(auth, b.ID, run.ID)
				if e != nil || !strings.Contains(input.Instructions, "Use concise bullet") {
					t.Fatalf("loader not file-based %+v %v", input, e)
				}
				execute(run)
			})
			t.Run("schedule-three-ticks", func(t *testing.T) {
				llm.calls = []taskHTTPCall{{"schedule_job", map[string]any{"name": "Retained researcher", "mode": "adaptive", "schedule": "* * * * *", "timezone": "UTC", "protocol": "task_v1", "task_id": task.ID.String()}}}
				source := admit("Run this retained procedure every minute.", &task.ID)
				execute(source)
				for i := 0; i < 3; i++ {
					if e := worker.Poll(auth); e != nil {
						t.Fatal(e)
					}
				}
				listed := jobs.List(auth)
				if len(listed) != 1 {
					t.Fatalf("schedule missing %+v", listed)
				}
				job := listed[0]
				wire, _ := json.Marshal(job)
				if strings.Contains(string(wire), "Use concise bullet") || strings.Contains(string(wire), "decisive sources") {
					t.Fatalf("scheduler copied working instructions %s", wire)
				}
				mts := NewMultiTenantScheduler(db, &scheduler.Executor{}, nil, nil)
				mts.SetBotSchedulerCallbacks(worker.SchedulerCallbacks())
				for i := 0; i < 3; i++ {
					job = jobs.Get(auth, job.ID)
					due := time.Now().UTC().Add(-time.Second)
					job.NextRun = &due
					if e := jobs.Update(auth, job); e != nil {
						t.Fatal(e)
					}
					research()
					if scope == store.BotScopePersonal {
						mts.executePersonalJob(auth, job, org.Slug, ods, ods.ForUser(owner.String()), jobs, owner.String())
					} else {
						mts.executeTeamJob(auth, job, org.Slug, team.Slug, ods, ods.ForTeam(team.Slug), jobs)
					}
					if e := worker.Poll(auth); e != nil {
						t.Fatal(e)
					}
					runs, e := repo.ListRuns(auth, b.ID, store.BotPage{Limit: 100})
					if e != nil {
						t.Fatal(e)
					}
					count := 0
					for _, r := range runs {
						if r.Trigger == store.TriggerCron {
							count++
							if r.Task == nil || r.Task.TaskID != task.ID || r.Status != store.RunSucceeded {
								t.Fatalf("scheduled task pin/result %+v", r)
							}
						}
					}
					if count != i+1 {
						t.Fatalf("real scheduler occurrences=%d want=%d job=%+v", count, i+1, jobs.Get(auth, job.ID))
					}
				}
				llm.calls = []taskHTTPCall{{"remove_scheduled_job", map[string]any{"job_id": job.ID}}}
				execute(admit("Delete only the schedule; retain the Task", &task.ID))
				for i := 0; i < 3; i++ {
					if e := worker.Poll(auth); e != nil {
						t.Fatal(e)
					}
				}
				if j := jobs.Get(auth, job.ID); j != nil && j.Enabled {
					t.Fatalf("schedule still enabled %+v", j)
				}
			})
			t.Run("delete-manual", func(t *testing.T) {
				current, e := tasks.GetTask(auth, b.ID, task.ID)
				if e != nil || current.Head == nil {
					t.Fatalf("schedule deletion purged Task %+v %v", current, e)
				}
				research()
				execute(admit("Manual after schedule deletion", &task.ID))
				physical, e := backend.ReadBotHomeWorkspace(auth, h, task.HomePath+"/TASK.md", 8192)
				if e != nil || !strings.Contains(string(physical), "Use concise bullet") {
					t.Fatalf("retained corrected bytes %q %v", physical, e)
				}
				if detailCalls.Load() != 7 || searchCalls.Load() != 7 {
					t.Fatalf("fresh observations: detail=%d search=%d", detailCalls.Load(), searchCalls.Load())
				}
			})
		})
	}
}

type taskSearchEngine struct {
	calls *atomic.Int32
	url   string
}

func (taskSearchEngine) Name() string { return "owned-search" }
func (m taskSearchEngine) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	m.calls.Add(1)
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Source: "+m.url, "model"), TurnComplete: true}, nil)
	}
}
