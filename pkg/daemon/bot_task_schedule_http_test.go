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
	if r.Config != nil && r.Config.SystemInstruction != nil && len(r.Config.SystemInstruction.Parts) > 0 && strings.HasPrefix(r.Config.SystemInstruction.Parts[0].Text, "Understand this entire completed Bot turn") {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"schema_version":1,"kind":"no_change","reason":"Controlled task fixture retains its procedure through the task workspace; no new standing knowledge.","proposals":[]}`, "model"), TurnComplete: true}, nil)
		}
	}
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
	if call.name == "web_fetch" {
		// Require a source in the actual shared search response for this turn;
		// neither retained task prose nor an old research episode is evidence.
		found, observed := false, false
		for i := len(r.Contents) - 1; i >= 0 && !observed; i-- {
			for _, part := range r.Contents[i].Parts {
				if part == nil || part.FunctionResponse == nil {
					continue
				}
				response := part.FunctionResponse
				if response.Name == launcher.BotConfiguredSearchToolName || response.Name == "execute_tool" {
					observed = true
					raw, _ := json.Marshal(response.Response)
					found = strings.Contains(string(raw), fmt.Sprint(call.args["url"])) && (strings.Contains(string(raw), "search_results") || strings.Contains(string(raw), "citations"))
				}
			}
		}
		if !found {
			return func(yield func(*model.LLMResponse, error) bool) {
				yield(nil, fmt.Errorf("fresh configured search response did not return the detail source"))
			}
		}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		fc := &genai.FunctionCall{ID: uuid.NewString(), Name: call.name, Args: call.args}
		if _, direct := r.Tools[call.name]; !direct {
			fc.Name, fc.Args = "execute_tool", map[string]any{"name": call.name, "arguments": call.args}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: fc}}}}, nil)
	}
}

// Owner HTTP -> production factories/worker -> physical durable Docker Home.
// The model and public-source fixture are deterministic; no paid provider is used.
func TestBotTaskScheduleHTTPAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_TASK_EXECUTION") != "1" {
		t.Skip("explicit physical execution acceptance required")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", wd)
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
			sessionStore, err := sandbox.NewLocalSessionStore(filepath.Join(dir, "registry"))
			if err != nil {
				t.Fatal(err)
			}
			backend, err := docker.New(docker.Config{Sessions: sandbox.NewSessionRegistryFromStore(sessionStore), LayersDir: os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS"), OverlayMode: "fuse", UppersDir: filepath.Join(dir, "uppers"), SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE")})
			if err != nil {
				t.Fatal(err)
			}
			var searchCalls, detailCalls atomic.Int32
			detail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := detailCalls.Add(1)
				fmt.Fprintf(w, "<html><body>Fresh decisive source observation %d</body></html>", n)
			}))
			defer detail.Close()
			// Public logical source identity; the owned transport maps it to the
			// local fixture without treating loopback evidence as public research.
			detailURL := "https://task-evidence.example/source"
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
				p.Constraints = append(p.Constraints, execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: []string{launcher.BotConfiguredSearchToolName, "web_fetch", detailURL, "bot_task_list", "bot_task_read", "bot_task_select", "bot_task_capture", "bot_task_update", "bot_task_asset", "bot_task_state", "bot_task_execute"}})
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
			pool := sandbox.NewBackendPool(backend, sandbox.ResourceLimits{})
			t.Cleanup(pool.Cleanup)
			wc := BotWorkerConfig{Backend: db, MandatePolicy: policy, TurnRegistry: registry, Sandbox: backend, Pool: pool, WorkerID: "task-http", ContextWindow: 16384, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				s.Services.BotFilesFactory = func(c context.Context, svc *store.Services, scope store.BotScope) (store.BotFiles, error) {
					return productionBotFiles(c, backend, svc, scope)
				}
				if !strings.Contains(q.Instructions, "Public research subject:") && q.Run.Trigger != store.TriggerCron {
					return &launcher.BotChatSafeResources{TaskWorkspace: workspace, ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: &taskCapturePool{titleSkillPool: titleSkillPool{url: detail.URL}, publicURL: detailURL}}, WorkspaceDir: "/workspace", PolicyRuntime: productionBotPolicyRuntime(botLiveGuard{repo: repo, request: q}), VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
				}
				// The shared Chat executable takes an exact public query, not the
				// retired facet adapter's arguments or private research episode.
				chatConfig, e := launcher.BotScopedChatConfig(c, s)
				if e != nil {
					return nil, e
				}
				reads, e := launcher.BotMandatedReadTools(q.Mandate, tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }})
				if e != nil {
					return nil, e
				}
				factory := func(context.Context, string, string, *config.AppConfig) (model.LLM, error) {
					return taskSearchEngine{calls: &searchCalls, url: detailURL}, nil
				}
				return &launcher.BotChatSafeResources{TaskWorkspace: workspace, ChatConfig: chatConfig, ProviderFactory: factory, Catalog: &launcher.ChatCatalogResources{Pool: &taskCapturePool{titleSkillPool: titleSkillPool{url: detail.URL}, publicURL: detailURL}}, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Mandate: q.Mandate, Guard: botLiveGuard{repo: repo, request: q}, Classifier: taskHTTPResearchClassifier{fallback: launcher.WithBotMandatedReadClassifier(launcher.BotCatalogueClassifier{}, reads)}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
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
			b, err := repo.Create(auth, store.BotCreateRequest{Name: "Retained researcher", OwnerID: owner, Version: v, IdempotencyKey: "draft"})
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
			if err = backend.BindBotHomeSession(h, b.HomeSessionID.String()); err != nil {
				t.Fatal(err)
			}
			if _, err = backend.CreateSession(auth, sandbox.SessionSpec{SessionID: b.HomeSessionID.String(), Type: sandbox.SessionTypeChat, UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, BotHome: h, TemplateID: sandbox.BaseTemplateID, LayerChain: []string{sandbox.BaseTemplateID}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				c, done := context.WithTimeout(context.Background(), 30*time.Second)
				defer done()
				if e := backend.DestroySession(c, b.HomeSessionID.String()); e != nil {
					t.Error(e)
				}
			})
			if err = backend.WaitForSessionReady(auth, b.HomeSessionID.String()); err != nil {
				t.Fatal(err)
			}
			if err = initializeBotHomeDocuments(auth, backend, h, v, func(commit func() error) error { return commit() }); err != nil {
				t.Fatal(err)
			}
			if err = pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			svc := &store.Services{Bots: repo, Sessions: ss, BotMandatePolicy: policy, BotTurnRegistry: registry, BotInteractiveReady: true}
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
				llm.calls = []taskHTTPCall{{launcher.BotConfiguredSearchToolName, map[string]any{"query": "Stock Market News"}}, {"web_fetch", map[string]any{"url": detailURL}}}
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
				definition := "# Stock Market News\n\nPublic research subject: Stock Market News\n\n## Owner requirements\nInspect configured search then decisive sources. Report source failures honestly.\n\n## Procedure\nUse perplexity_web_search with the public Stock Market News query then web_fetch on the actual returned source. Include source limitations.\n\n## Output\nUse concise bullet points, not a table.\n"
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

// taskHTTPResearchClassifier describes only the fixture's exact public search.
// It does not skip policy: cron still binds scoped configuration and every call
// passes live mandate/lease checks before the shared executable can run.
type taskHTTPResearchClassifier struct{ fallback agent.BotActionClassifier }

func (c taskHTTPResearchClassifier) DescribeAction(ctx context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	if name == "web_fetch" {
		var args map[string]any
		if json.Unmarshal(raw, &args) != nil || len(args) != 1 || args["url"] != "https://task-evidence.example/source" {
			return execution.ActionDescriptor{}, store.ErrBotForbidden
		}
		return execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
	}
	if name != launcher.BotConfiguredSearchToolName {
		return c.fallback.DescribeAction(ctx, name, raw)
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil || len(args) != 1 || args["query"] != "Stock Market News" {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	return execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}

func TestTaskHTTPResearchClassifierExactPublicQuery(t *testing.T) {
	classifier := taskHTTPResearchClassifier{fallback: launcher.BotCatalogueClassifier{}}
	for _, tc := range []struct {
		name    string
		raw     string
		allowed bool
	}{
		{"exact", `{"query":"Stock Market News"}`, true},
		{"retired-facet", `{"facet":"overview"}`, false},
		{"private-query", `{"query":"private task contents"}`, false},
		{"extra-context", `{"query":"Stock Market News","context":"private"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action, err := classifier.DescribeAction(t.Context(), launcher.BotConfiguredSearchToolName, json.RawMessage(tc.raw))
			if tc.allowed {
				if err != nil || action.Effect != execution.EffectRead || !action.ConstraintsVerified {
					t.Fatalf("exact classification %+v %v", action, err)
				}
			} else if err == nil {
				t.Fatal("unbounded search classified")
			}
		})
	}
}

func TestTaskHTTPModelRequiresFreshReturnedSource(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response map[string]any
		allowed  bool
	}{
		{"returned-source", map[string]any{"search_results": []any{map[string]any{"url": "https://source.test/news"}}}, true},
		{"missing-source", map[string]any{"answer": "No sources"}, false},
		{"failed-search", map[string]any{"error": "unavailable"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &taskHTTPModel{calls: []taskHTTPCall{{"web_fetch", map[string]any{"url": "https://source.test/news"}}}}
			r := &model.LLMRequest{Tools: map[string]any{"web_fetch": true}, Contents: []*genai.Content{{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "execute_tool", Response: tc.response}}}}}}
			for response, err := range m.GenerateContent(t.Context(), r, false) {
				if tc.allowed {
					if err != nil || response == nil || response.Content.Parts[0].FunctionCall.Name != "web_fetch" {
						t.Fatalf("returned source not followed: %+v %v", response, err)
					}
				} else if err == nil {
					t.Fatal("detail retrieval without a returned source")
				}
			}
		})
	}
}

type taskCapturePool struct {
	titleSkillPool
	publicURL string
}

func (p *taskCapturePool) GetOrCreate(string) sandbox.ToolNodeClient                     { return p }
func (p *taskCapturePool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient { return p }
func (p *taskCapturePool) Call(session, name string, args map[string]interface{}) (json.RawMessage, error) {
	if name != "web_fetch" || args["url"] != p.publicURL {
		return nil, store.ErrBotInvalid
	}
	values := make(map[string]interface{}, len(args))
	for k, v := range args {
		values[k] = v
	}
	values["url"] = p.url
	raw, err := p.titleSkillPool.Call(session, name, values)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	result["final_url"] = p.publicURL
	return json.Marshal(result)
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
