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
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// The provider only chooses from actual source/context supplied by orientation.
// No skill, pin or operation is preseeded.
type titleSkillScheduleModel struct {
	skillScheduleModel
	source string
}

func (m *titleSkillScheduleModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if m.work != nil {
		return m.skillScheduleModel.GenerateContent(ctx, req, stream)
	}
	prompt := ""
	if req.Config != nil && req.Config.SystemInstruction != nil {
		prompt = req.Config.SystemInstruction.Parts[0].Text
	}
	if strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1") {
		var in struct {
			Owner   string        `json:"owner_source"`
			Initial bool          `json:"initial_assignment"`
			Prior   *store.BotRun `json:"prior_skill_task"`
		}
		if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &in); err != nil {
			m.t.Fatal(err)
		}
		if m.assignment == "AI News" {
			if in.Owner != "AI News" || !in.Initial || !strings.Contains(prompt, "terse title") {
				m.t.Errorf("initial assignment absent: %s", req.Contents[0].Parts[0].Text)
			}
		} else if m.assignment == "yes" {
			if in.Owner != "yes" || in.Prior == nil || in.Prior.Report.Skill == nil || in.Prior.Report.Skill.SkillID.String() != m.skill {
				m.t.Fatalf("follow-up lost prior task context: %s", req.Contents[0].Parts[0].Text)
			}

			raw, _ := json.Marshal(map[string]any{"kind": "use_existing", "skill_id": in.Prior.Report.Skill.SkillID.String(), "title": "", "responsibility": "", "method": []string{}, "expectations": []string{}, "reason": "Referential follow-up to the sole committed task"})
			return func(yield func(*model.LLMResponse, error) bool) {
				yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
			}
		}

	}
	if strings.HasPrefix(prompt, "BOT_SKILL_LEARN_V1") {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"method":[],"output_format":"","evaluation_rules":[],"evidence_refs":[],"reason":"No additional generalization"}`, "model")}, nil)
		}
	}
	if strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1") || strings.HasPrefix(prompt, "BOT_WORK_PENDING_REVIEW_V1") {
		assessment, ok := workerWorkAssessment(req, func(w *store.BotWorkState) bool {
			return w.Candidate.Text == "Verified AI news source" || strings.Contains(w.Candidate.Text, "pending")
		})
		if !ok {
			m.t.Fatal("review input not recognized")
		}
		return assessment
	}
	if !m.control && m.tool == "" && !strings.HasPrefix(prompt, "BOT_") {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Verified AI news source", "model")}, nil)
		}
	}
	return m.skillScheduleModel.GenerateContent(ctx, req, stream)
}

// Only the fixture's single read-only HTTP endpoint is authorized by this
// injected network boundary. All other calls retain production classification.
type titleSkillReadClassifier struct{ url string }

func (c titleSkillReadClassifier) DescribeAction(ctx context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	var args map[string]any
	if name == "web_fetch" && json.Unmarshal(raw, &args) == nil && args["url"] == c.url && len(args) == 1 {
		return execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
	}
	return (launcher.BotCatalogueClassifier{}).DescribeAction(ctx, name, raw)
}

type titleSkillPool struct {
	workerTestPool
	url   string
	calls int
}

func (p *titleSkillPool) GetOrCreate(string) sandbox.ToolNodeClient                     { return p }
func (p *titleSkillPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient { return p }
func (p *titleSkillPool) Call(_ string, name string, args map[string]interface{}) (json.RawMessage, error) {
	if name != "web_fetch" || args["url"] != p.url {
		return nil, store.ErrBotInvalid
	}
	response, err := http.Get(p.url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	p.calls++
	return json.Marshal(map[string]any{"content": string(body), "status": response.StatusCode, "final_url": p.url})
}

func TestBotSkillTitleFollowupHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			backend, owner := setupChannelResolverFixture(t, "skill-schedule", "team", "telegram", "linked")
			ctx := t.Context()
			org, err := backend.ForOrg("skill-schedule")
			if err != nil {
				t.Fatal(err)
			}
			repo, ss, jobs := org.ForTeam("team").Bots(), org.ForTeam("team").Sessions(), org.ForTeam("team").ScheduledJobs()
			pin := org.ForTeam("team").SetSessionPin
			if scope == store.BotScopePersonal {
				repo, ss, jobs = org.ForUser(owner).Bots(), org.ForUser(owner).Sessions(), org.ForUser(owner).ScheduledJobs()
				pin = org.ForUser(owner).SetSessionPin
			}
			auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "skill-schedule", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			auth = store.WithTenantContext(auth, &store.TenantContext{UserID: owner, OrgSlug: "skill-schedule", TeamSlug: "team"})
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "Verified AI news source") }))
			defer source.Close()
			mdl := &titleSkillScheduleModel{skillScheduleModel: skillScheduleModel{t: t}, source: source.URL}
			pool := &titleSkillPool{url: source.URL}
			registry := execution.NewBotTurnAuthorizationRegistry(32)
			defer registry.Close()
			policy := newProductionBotMandatePolicy(backend, &config.AppConfig{})
			worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, MandatePolicy: policy, TurnRegistry: registry, Sandbox: lifecycleHTTPBackend{}, Pool: pool, WorkerID: "skills-http", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return mdl, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, _ *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				mdl.stage, mdl.interactive = 0, q.Run.IsInteractiveChat()
				mdl.skillScheduleModel.source = source.URL
				mdl.work = func() *store.BotWorkState {
					w, err := repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease)
					if err != nil {
						t.Fatal(err)
					}
					return w
				}
				core, err := launcher.BotCatalogueTools(pool, q.Run.SessionID.String())
				if err != nil {
					return nil, err
				}
				return &launcher.BotChatSafeResources{Tools: core, WorkspaceDir: "/workspace", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: titleSkillReadClassifier{url: source.URL}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			worker.schedulerReady.Store(true)
			v := store.DefaultBotVersion()
			v.Charter = "Report the controlled result"
			v.Budget.MaxTokens = nil
			createServices := &store.Services{Bots: repo, PersonalBots: repo, Sessions: ss, PersonalSessions: ss, BotMandatePolicy: policy, BotTurnRegistry: registry, BotInteractiveReady: true}
			createServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				api.BotCreateHandler(w, r.WithContext(store.WithServices(auth, createServices)))
			}))
			defer createServer.Close()
			response, err := http.Post(createServer.URL+"?scope="+string(scope), "application/json", strings.NewReader(`{"name":"AI News","start_from_title":true,"idempotency_key":"title"}`))
			if err != nil {
				t.Fatal(err)
			}
			var created struct {
				store.Bot
				InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
			}
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != 201 {
				t.Fatalf("create: %d %s", response.StatusCode, raw)
			}
			if err := json.Unmarshal(raw, &created); err != nil {
				t.Fatal(err)
			}
			b := &created.Bot
			if err != nil {
				t.Fatal(err)
			}
			internal, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: "skill-schedule", TeamSlug: "team", Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			if err = entstore.RegisterBotHomeBinding(internal, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "registered-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scope == store.BotScopePersonal {
				// Exercise inherited owner defaults, not only a literal Home pin.
				err = org.ForUser(owner).PersonalSettings().Save(auth, &store.PersonalSettings{DefaultProvider: "controlled", DefaultModel: "model"})
			} else {
				err = pin(auth, b.HomeSessionID.String(), "controlled", "model")
			}
			if err != nil {
				t.Fatal(err)
			}
			svc := &store.Services{Bots: repo, Sessions: ss, BotMandatePolicy: policy, BotTurnRegistry: registry}
			if scope == store.BotScopePersonal {
				svc.PersonalBots = repo
				svc.PersonalSessions = ss
			}
			router := mux.NewRouter()
			root := "/bots/{botID}"
			router.HandleFunc(root+"/messages", api.BotMessageHandler).Methods("POST")
			router.HandleFunc(root+"/runs/{runID}", api.BotRunHandler).Methods("GET")
			router.HandleFunc(root+"/skills", api.BotSkillsHandler).Methods("GET")
			router.HandleFunc(root+"/skills/import-legacy", api.BotLegacySkillImportHandler).Methods("POST")
			router.HandleFunc(root+"/skills/{skillID}/head", api.BotSkillHeadHandler).Methods("GET")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				router.ServeHTTP(w, r.WithContext(store.WithServices(auth, svc)))
			}))
			defer server.Close()
			request := func(method, path string, body any, status int) []byte {
				t.Helper()
				raw, _ := json.Marshal(body)
				req, e := http.NewRequestWithContext(ctx, method, server.URL+path+"?scope="+string(scope), strings.NewReader(string(raw)))
				if e != nil {
					t.Fatal(e)
				}
				req.Header.Set("Content-Type", "application/json")
				res, e := server.Client().Do(req)
				if e != nil {
					t.Fatal(e)
				}
				defer res.Body.Close()
				data, e := io.ReadAll(res.Body)
				if e != nil {
					t.Fatal(e)
				}
				if res.StatusCode != status {
					t.Fatalf("%s %s: %d want %d: %s", method, path, res.StatusCode, status, data)
				}
				return data
			}
			path := "/bots/" + b.ID.String()
			readRun := func(id uuid.UUID) *store.BotRun {
				t.Helper()
				var run store.BotRun
				if e := json.Unmarshal(request("GET", path+"/runs/"+id.String(), nil, 200), &run); e != nil {
					t.Fatal(e)
				}
				if run.Provider != "controlled" || run.Model != "model" {
					t.Fatalf("HTTP run lost resolved model identity: provider=%q model=%q", run.Provider, run.Model)
				}
				return &run
			}
			ownerTurn := func(text, tool string, args map[string]any, feedback bool) *store.BotRun {
				t.Helper()
				current, e := repo.Get(auth, b.ID)
				if e != nil {
					t.Fatal(e)
				}
				mdl.assignment, mdl.tool, mdl.args, mdl.control, mdl.feedback = text, tool, args, tool != "", feedback
				var receipt store.BotMessageReceipt
				if e = json.Unmarshal(request("POST", path+"/messages", map[string]any{"version": current.Version, "idempotency_key": uuid.NewString(), "instructions": text}, 202), &receipt); e != nil {
					t.Fatal(e)
				}
				if e = worker.Poll(ctx); e != nil {
					t.Fatal(e)
				}
				run := readRun(receipt.Run.ID)
				if run.Status != store.RunSucceeded {
					t.Fatalf("owner turn failed: %+v", run)
				}
				return run
			}
			operation := func(run *store.BotRun) store.BotOperation {
				t.Helper()
				saved, e := repo.GetRun(auth, b.ID, run.ID)
				if e != nil {
					t.Fatal(e)
				}
				rt, e := worker.AuthorizedOperations(repo)
				if e != nil {
					t.Fatal(e)
				}
				if saved.MandateRef == nil {
					t.Fatal("missing mandate")
				}
				ops, e := rt.ListAuthorizedOperations(auth, *saved.MandateRef)
				if e != nil || len(ops) != 1 || ops[0].Status != store.BotOperationApplied {
					t.Fatalf("operation not applied: %+v %v", ops, e)
				}
				return ops[0]
			}
			executor := &scheduler.Executor{}
			mts := NewMultiTenantScheduler(backend, executor, nil, nil)
			mts.SetBotSchedulerCallbacks(worker.SchedulerCallbacks())
			dispatch := func(job *store.ScheduledJob) {
				t.Helper()
				if scope == store.BotScopePersonal {
					mts.executePersonalJob(ctx, job, "skill-schedule", org, org.ForUser(owner), jobs, owner)
				} else {
					mts.executeTeamJob(ctx, job, "skill-schedule", "team", org, org.ForTeam("team"), jobs)
				}
			}
			wake := func(id string, duplicate bool) *store.BotRun {
				t.Helper()
				job := jobs.Get(auth, id)
				if job == nil {
					t.Fatal("missing scheduled job")
				}
				due := time.Now().UTC().Add(-time.Second)
				job.NextRun = &due
				if e := jobs.Update(auth, job); e != nil {
					t.Fatal(e)
				}
				prior, e := repo.ListRuns(auth, b.ID, store.BotPage{})
				if e != nil {
					t.Fatal(e)
				}
				ids := map[uuid.UUID]bool{}
				for _, r := range prior {
					ids[r.ID] = true
				}
				dispatch(job)
				if duplicate {
					dispatch(job)
				}
				runs, e := repo.ListRuns(auth, b.ID, store.BotPage{})
				if e != nil {
					t.Fatal(e)
				}
				var selected *store.BotRun
				count := 0
				for i := range runs {
					if runs[i].Trigger == store.TriggerCron && !ids[runs[i].ID] {
						selected = &runs[i]
						count++
					}
				}
				if count != 1 {
					t.Fatalf("occurrence count=%d job=%+v", count, jobs.Get(auth, id))
				}
				mdl.control, mdl.feedback = false, false
				mdl.tool, mdl.args = "", nil
				if e := worker.Poll(ctx); e != nil {
					t.Fatal(e)
				}
				return readRun(selected.ID)
			}
			var scheduleID string
			t.Run("create-schedule", func(t *testing.T) {
				before, e := repo.(store.BotSkillStore).ListSkills(auth, b.ID, store.BotSkillPage{})
				if e != nil || len(before.Skills) != 0 {
					t.Fatal("new Bot has seeded skills")
				}
				mdl.assignment, mdl.tool, mdl.args = "AI News", "", nil
				if e := worker.Poll(ctx); e != nil {
					t.Fatal(e)
				}
				run := readRun(created.InitialTurn.Run.ID)
				if run.Status != store.RunSucceeded || pool.calls != 1 {
					t.Fatalf("initial fetch failed: %+v calls=%d", run, pool.calls)
				}
				if run.Report.Skill == nil {
					t.Fatal("no skill created")
				}
				mdl.skill = run.Report.Skill.SkillID.String()
				head, e := repo.(store.BotSkillStore).GetSkill(auth, b.ID, run.Report.Skill.SkillID)
				if e != nil {
					t.Fatal(e)
				}
				method, e := repo.(store.BotSkillStore).GetSkillRevision(auth, b.ID, head.ID, head.HeadRevision)
				if e != nil {
					t.Fatal(e)
				}
				if len(method.Expectations) != 1 || method.Expectations[0].Text != "AI News" || method.Expectations[0].Provenance.Quote != "AI News" {
					t.Fatalf("title expanded into invented requirements: %+v", method.Expectations)
				}
				r := ownerTurn("yes", "schedule_job", map[string]any{"name": "AI News", "mode": "adaptive", "schedule": "0 9 * * *", "timezone": "UTC", "skill_id": mdl.skill, "invocation": "Daily AI & tech news briefing"}, false)
				op := operation(r)
				scheduleID = op.Receipt.Schedule.ScheduleID.String()
				job := jobs.Get(auth, scheduleID)
				if job == nil || !job.Enabled {
					t.Fatal("schedule missing")
				}
				current, e := repo.Get(auth, b.ID)
				if e != nil {
					t.Fatal(e)
				}
				version, e := repo.GetVersion(auth, b.ID, current.Version)
				if e != nil {
					t.Fatal(e)
				}
				if len(version.Triggers) != 1 || version.Triggers[0].SkillID == nil || version.Triggers[0].SkillID.String() != mdl.skill || version.Triggers[0].Instructions != "Daily AI & tech news briefing" {
					t.Fatalf("schedule copied method or lost binding: %+v", version.Triggers)
				}
			})
			if scheduleID == "" {
				t.Fatal("schedule setup failed")
			}
			t.Run("duplicate-wake", func(t *testing.T) {
				run := wake(scheduleID, true)
				if run.Status != store.RunSucceeded || run.Report.Skill == nil || run.Report.Skill.SkillID.String() != mdl.skill || run.Report.Skill.UsedRevision < 1 {
					t.Fatalf("scheduled pin: %+v", run)
				}
			})
		})
	}
}

func (p *titleSkillPool) BindSession(string)                     {}
func (p *titleSkillPool) SetSessionScope(string, string, string) {}
func (p *titleSkillPool) EnsureReady(string) error               { return nil }
