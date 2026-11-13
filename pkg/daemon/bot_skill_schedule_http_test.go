package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Provider answers only; all admission, skill provenance, grants, pins, quality
// review and operation application pass through the production repositories.
type skillScheduleModel struct {
	workerTestModel
	skill       string
	assignment  string
	feedback    bool
	control     bool
	tool        string
	args        map[string]any
	t           *testing.T
	pins        []store.BotSkillInvocation
	work        func() *store.BotWorkState
	source      string
	stage       int
	interactive bool
}

// Ordinary agent decisions; scoped tools persist learning and read fresh data.
func (m *skillScheduleModel) agentContent() iter.Seq2[*model.LLMResponse, error] {
	w := m.work()
	if w.Protocol != store.BotWorkProtocolAgentLedV1 {
		m.t.Fatal("expected agent-led work")
	}
	if w.Contract.Skill != nil && m.stage == 0 {
		m.pins = append(m.pins, *w.Contract.Skill)
	}
	name, args := "", map[string]any{}
	if m.feedback {
		if m.stage == 0 {
			name, args = "bot_skill_save", map[string]any{"expected_revision": w.Revision, "skill_id": m.skill, "owner_feedback_scope": "skill", "owner_quotes": []string{m.assignment}}
		}
	} else {
		switch m.stage {
		case 0:
			if m.skill != "" && w.Contract.Skill == nil {
				name, args = "bot_skill_use", map[string]any{"expected_revision": w.Revision, "skill_id": m.skill}
			} else {
				name = "bot_work_read"
			}
		case 1:
			name, args = "execute_tool", map[string]any{"name": "web_fetch", "arguments": map[string]any{"url": m.source}}
		case 2:
			if m.skill == "" {
				name, args = "bot_skill_save", map[string]any{"expected_revision": w.Revision, "title": "Report", "method": []string{"Read the current source; never reuse previous observations.", "Report truthfully."}, "evidence_refs": []string{"capture:agent-schedule-1"}}
			}
		}
	}
	if m.interactive && name != "" && name != "execute_tool" {
		name, args = "execute_tool", map[string]any{"name": name, "arguments": args}
	}
	id := fmt.Sprintf("agent-schedule-%d", m.stage)
	m.stage++
	return func(yield func(*model.LLMResponse, error) bool) {
		part := &genai.Part{Text: "durable controlled result"}
		if name != "" {
			part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, nil)
	}
}

func (m *skillScheduleModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	prompt := ""
	if req.Config != nil && req.Config.SystemInstruction != nil && len(req.Config.SystemInstruction.Parts) > 0 {
		prompt = req.Config.SystemInstruction.Parts[0].Text
	}
	if m.work != nil && strings.HasPrefix(prompt, "BOT_") {
		m.t.Fatal("agent work invoked mandatory assessment")
	}
	if m.work != nil && !m.control && m.tool == "" {
		return m.agentContent()
	}
	if strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1") {
		kind := "create_draft"
		quotes := []string{m.assignment}
		if m.skill != "" {
			kind = "use_existing"
		}
		if m.feedback {
			kind = "update_owner_expectations"
		}
		if m.control {
			kind = "no_skill_needed"
			quotes = []string{}
		}
		raw, _ := json.Marshal(map[string]any{"kind": kind, "skill_id": m.skill, "title": "Report", "responsibility": "Report the controlled result", "method": []string{"Read the report", "Report the result truthfully"}, "expectations": quotes, "reason": "Owner assignment"})
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
		}
	}
	if strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1") {
		var c store.BotWorkContract
		if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &c); err != nil {
			m.t.Fatal(err)
		}
		if c.Skill != nil {
			m.pins = append(m.pins, *c.Skill)
		}
	}
	if answer, ok := workerWorkAssessment(req, nil); ok {
		return answer
	}
	if m.tool != "" {
		name, args := m.tool, m.args
		m.tool = ""
		if m.work != nil && !m.control {
			m.stage = 3
		} // denial returns a final answer, not another scripted task
		if name != "propose_bot_control" {
			name, args = "execute_tool", map[string]any{"name": name, "arguments": args}
		}
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}}}}}, nil)
		}
	}
	return m.workerTestModel.GenerateContent(ctx, req, stream)
}

// Actual HTTP owner turns create the skill and authorized controls. The real
// multi-tenant scheduler authenticates its owner and admits persisted occurrences;
// the production worker executes them. No fabricated run/work/skill/grant rows.
func TestBotAgentScheduleHTTPAcceptance(t *testing.T) { testBotSkillScheduleHTTPAcceptance(t) }

func TestBotSkillScheduleHTTPAcceptance(t *testing.T) { testBotSkillScheduleHTTPAcceptance(t) }

func testBotSkillScheduleHTTPAcceptance(t *testing.T) {
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
			sourceCalls := 0
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceCalls++
				fmt.Fprintf(w, "fresh observation %d", sourceCalls)
			}))
			defer source.Close()
			mdl := &skillScheduleModel{t: t, source: source.URL}
			pool := &titleSkillPool{url: source.URL}
			registry := execution.NewBotTurnAuthorizationRegistry(32)
			defer registry.Close()
			policy := newProductionBotMandatePolicy(backend, &config.AppConfig{})
			worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, MandatePolicy: policy, TurnRegistry: registry, Sandbox: lifecycleHTTPBackend{}, Pool: pool, WorkerID: "skills-http", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return mdl, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, _ *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				if q.Work == nil || q.Work.Protocol != store.BotWorkProtocolAgentLedV1 || bot.BotWorkServicesFromContext(c) == nil {
					t.Fatalf("missing agent capability before prepare: work=%+v services=%v", q.Work, bot.BotWorkServicesFromContext(c))
				}
				mdl.stage = 0
				mdl.interactive = q.Run.IsInteractiveChat()
				mdl.work = func() *store.BotWorkState {
					w, e := repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease)
					if e != nil {
						t.Fatal(e)
					}
					return w
				}
				core, e := launcher.BotCatalogueTools(pool, q.Run.SessionID.String())
				if e != nil {
					return nil, e
				}
				return &launcher.BotChatSafeResources{Tools: core, WorkspaceDir: "/workspace", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: titleSkillReadClassifier{url: source.URL}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { worker.Stop() }()
			worker.schedulerReady.Store(true)
			v := store.DefaultBotVersion()
			v.Charter = "Report the controlled result"
			v.Budget.MaxTokens = nil
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Skill reporter", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "create"})
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
			if err = pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
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
			readHead := func() store.BotSkillRevision {
				t.Helper()
				var head store.BotSkillRevision
				if e := json.Unmarshal(request("GET", path+"/skills/"+mdl.skill+"/head", nil, 200), &head); e != nil {
					t.Fatal(e)
				}
				return head
			}
			seenOccurrences := map[uuid.UUID]bool{}
			seenOccurrenceKeys := map[string]bool{}
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
				beforeCalls := sourceCalls
				attemptDenied := mdl.tool != ""
				if e := worker.Poll(ctx); e != nil {
					t.Fatal(e)
				}
				result := readRun(selected.ID)
				if seenOccurrences[result.ID] || seenOccurrenceKeys[result.IdempotencyKey] {
					t.Fatal("distinct tick reused occurrence identity")
				}
				seenOccurrences[result.ID], seenOccurrenceKeys[result.IdempotencyKey] = true, true
				if !attemptDenied && sourceCalls != beforeCalls+1 {
					t.Fatalf("tick did not read fresh source exactly once: %d -> %d", beforeCalls, sourceCalls)
				}
				if e := worker.Poll(ctx); e != nil {
					t.Fatal(e)
				}
				if !attemptDenied && sourceCalls != beforeCalls+1 {
					t.Fatal("completed occurrence replayed retrieval")
				}
				return result
			}
			var scheduleID string
			t.Run("create-schedule", func(t *testing.T) {
				run := ownerTurn("Prepare the controlled report", "", nil, false)
				if run.Report.Skill == nil {
					t.Fatal("no skill created")
				}
				mdl.skill = run.Report.Skill.SkillID.String()
				r := ownerTurn("Run this report every day", "schedule_job", map[string]any{"name": "Report", "mode": "adaptive", "schedule": "0 9 * * *", "timezone": "UTC", "skill_id": mdl.skill, "invocation": "Prepare today's report"}, false)
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
				if len(version.Triggers) != 1 || version.Triggers[0].SkillID == nil || version.Triggers[0].SkillID.String() != mdl.skill || version.Triggers[0].Instructions != "Prepare today's report" {
					t.Fatalf("schedule copied method or lost binding: %+v", version.Triggers)
				}
			})
			if scheduleID == "" {
				t.Fatal("schedule setup failed")
			}
			t.Run("duplicate-wake", func(t *testing.T) {
				head := readHead()
				run := wake(scheduleID, true)
				if run.Status != store.RunSucceeded || run.Report.Skill == nil || run.Report.Skill.SkillID.String() != mdl.skill || run.Report.Skill.UsedRevision != head.Revision {
					t.Fatalf("scheduled pin: %+v skill=%+v pins=%+v", run, run.Report.Skill, mdl.pins)
				}
			})
			t.Run("feedback-latest", func(t *testing.T) {
				beforeBot, err := repo.Get(auth, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				beforeVersion, err := repo.GetVersion(auth, b.ID, beforeBot.Version)
				if err != nil {
					t.Fatal(err)
				}
				beforeJob := jobs.Get(auth, scheduleID)
				ownerTurn("From now on include report units", "", nil, true)
				afterBot, err := repo.Get(auth, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				afterVersion, err := repo.GetVersion(auth, b.ID, afterBot.Version)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(beforeVersion, afterVersion) || !reflect.DeepEqual(beforeJob, jobs.Get(auth, scheduleID)) {
					t.Fatal("feedback changed authority or schedule cadence")
				}
				head := readHead()
				// Drop all worker-local state; subsequent ticks must rediscover the
				// persisted feedback and skill through their freshly pinned work.
				worker.Stop()
				worker, err = NewBotWorker(worker.config)
				if err != nil {
					t.Fatal(err)
				}
				worker.schedulerReady.Store(true)
				mts.SetBotSchedulerCallbacks(worker.SchedulerCallbacks())
				mdl.pins, mdl.work = nil, nil
				beforeCalls := sourceCalls
				manual := ownerTurn("Prepare the report after restart", "", nil, false)
				if sourceCalls != beforeCalls+1 || manual.Report.Skill == nil || manual.Report.Skill.UsedRevision != head.Revision {
					t.Fatalf("manual restart lost method or fresh retrieval: %+v", manual)
				}
				// The manual outcome appends provenance, so the next tick must pin
				// the new head rather than the pre-manual feedback revision.
				head = readHead()
				run := wake(scheduleID, false)
				if run.Status != store.RunSucceeded || run.Report.Skill == nil || run.Report.Skill.UsedRevision != head.Revision {
					t.Fatalf("latest skill not resolved: %+v", run)
				}
				if len(mdl.pins) == 0 || len(mdl.pins[len(mdl.pins)-1].Snapshot.Expectations) != 2 {
					t.Fatal("feedback absent from pin")
				}
			})
			t.Run("remaining-fresh-occurrences-after-restart", func(t *testing.T) {
				for range 2 {
					head := readHead()
					run := wake(scheduleID, false)
					if run.Status != store.RunSucceeded || run.Report.Skill == nil || run.Report.Skill.UsedRevision != head.Revision {
						t.Fatalf("post-restart occurrence: %+v", run)
					}
				}
			})
			t.Run("denied-resource", func(t *testing.T) {
				before := len(jobs.List(auth))
				mdl.tool = "schedule_job"
				mdl.args = map[string]any{"name": "Unauthorized", "mode": "adaptive", "schedule": "0 8 * * *", "instructions": "Another report"}
				run := wake(scheduleID, false)
				if mdl.tool != "" {
					t.Fatal("denial did not reach provider tool attempt")
				}
				if len(jobs.List(auth)) != before {
					t.Fatal("skill granted scheduler permission")
				}
				// The source-specific catalog excludes scheduler mutation tools from
				// recurring task mandates. Rejection precedes executable-tool usage.
				if run.Status != store.RunFailed || run.Usage.ToolCalls != 0 || len(run.Report.Unverified) == 0 {
					t.Fatalf("denied tool must fail closed before dispatch: %+v", run)
				}
				if run.Report.Skill == nil {
					t.Fatal("denied run lost skill receipt")
				}
			})
			t.Run("task-once", func(t *testing.T) {
				head := readHead()
				r := ownerTurn("Run the report once", "propose_bot_control", map[string]any{"payload": map[string]any{"operation": "task_once", "reason": "Owner requested once", "task_once": map[string]any{"skill_id": mdl.skill, "invocation": "Prepare one report"}}}, false)
				operation(r)
				mdl.control = false
				if e := worker.Poll(ctx); e != nil {
					t.Fatal(e)
				}
				runs, e := repo.ListRuns(auth, b.ID, store.BotPage{})
				if e != nil {
					t.Fatal(e)
				}
				var run *store.BotRun
				for _, candidate := range runs {
					if candidate.SkillID != nil && candidate.Trigger == store.TriggerManual && candidate.Invocation == "Prepare one report" {
						run = readRun(candidate.ID)
					}
				}
				if run == nil {
					t.Fatal("missing one-off run")
				}
				if run.Status != store.RunSucceeded || run.SkillID == nil || run.SkillID.String() != mdl.skill || run.Report.Skill == nil || run.Report.Skill.UsedRevision != head.Revision {
					t.Fatalf("task once lost latest skill: %+v", run)
				}
			})
			t.Run("remove-then-manual", func(t *testing.T) {
				r := ownerTurn("Remove the schedule", "remove_scheduled_job", map[string]any{"job_id": scheduleID}, false)
				operation(r)
				if jobs.Get(auth, scheduleID) != nil {
					t.Fatal("schedule not removed")
				}
				head := readHead()
				beforeCalls := sourceCalls
				run := ownerTurn("Prepare the report manually", "", nil, false)
				if sourceCalls != beforeCalls+1 {
					t.Fatal("manual reuse did not fetch fresh data")
				}
				if run.Report.Skill == nil || run.Report.Skill.SkillID.String() != mdl.skill || run.Report.Skill.UsedRevision != head.Revision {
					t.Fatal("schedule removal deleted skill")
				}
			})
			var legacy store.BotOperation
			var importBody map[string]any
			var beforeVersion *store.BotVersion
			var beforeJob *store.ScheduledJob
			t.Run("import-rollback", func(t *testing.T) {
				legacy = operation(ownerTurn("Create the old report schedule", "schedule_job", map[string]any{"name": "Legacy", "mode": "adaptive", "schedule": "0 7 * * *", "instructions": "Read the legacy report"}, false))
				current, e := repo.Get(auth, b.ID)
				if e != nil {
					t.Fatal(e)
				}
				beforeVersion, e = repo.GetVersion(auth, b.ID, current.Version)
				if e != nil {
					t.Fatal(e)
				}
				beforeJob = jobs.Get(auth, legacy.Receipt.Schedule.ScheduleID.String())
				importBody = map[string]any{"expected_revision": current.Revision, "trigger_id": legacy.Receipt.Schedule.TriggerID, "version": current.Version, "instructions_digest": fmt.Sprintf("%x", sha256.Sum256([]byte("wrong"))), "idempotency_key": "legacy-import"}
				before := request("GET", path+"/skills", nil, 200)
				request("POST", path+"/skills/import-legacy", importBody, 409)
				after := request("GET", path+"/skills", nil, 200)
				if string(before) != string(after) {
					t.Fatal("failed import wrote a skill")
				}
			})
			t.Run("foreign-import", func(t *testing.T) {
				foreignVersion := store.DefaultBotVersion()
				foreignVersion.Charter = "Separate report responsibility"
				foreign, e := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Other", OwnerID: b.OwnerID, Version: foreignVersion, IdempotencyKey: "foreign"})
				if e != nil {
					t.Fatal(e)
				}
				copy := map[string]any{}
				for k, v := range importBody {
					copy[k] = v
				}
				copy["expected_revision"], copy["version"] = foreign.Revision, foreign.Version
				request("POST", "/bots/"+foreign.ID.String()+"/skills/import-legacy", copy, 404)
			})
			t.Run("legacy-import", func(t *testing.T) {
				importBody["instructions_digest"] = fmt.Sprintf("%x", sha256.Sum256([]byte("Read the legacy report")))
				raw := request("POST", path+"/skills/import-legacy", importBody, 200)
				var imported store.BotSkill
				if e := json.Unmarshal(raw, &imported); e != nil {
					t.Fatal(e)
				}
				if imported.Legacy == nil || imported.Status != store.BotSkillDraft {
					t.Fatal("not conservative import")
				}
				if string(raw) != string(request("POST", path+"/skills/import-legacy", importBody, 200)) {
					t.Fatal("import replay changed receipt")
				}
				var head store.BotSkillRevision
				if e := json.Unmarshal(request("GET", path+"/skills/"+imported.ID.String()+"/head", nil, 200), &head); e != nil {
					t.Fatal(e)
				}
				if head.Provenance[0].Level != store.BotSkillImported || head.Expectations[0].Provenance.Level != store.BotSkillImported {
					t.Fatal("legacy provenance promoted")
				}
				after, e := repo.GetVersion(auth, b.ID, beforeVersion.Version)
				if e != nil || !reflect.DeepEqual(beforeVersion, after) || !reflect.DeepEqual(beforeJob, jobs.Get(auth, beforeJob.ID)) {
					t.Fatalf("import rewrote trigger/grant/cadence/next-run: %v", e)
				}
				run := wake(beforeJob.ID, false)
				if run.Status != store.RunSucceeded || run.Report.Skill == nil || run.Report.Skill.SkillID != imported.ID {
					t.Fatalf("import association not used: %+v", run)
				}
			})
		})
	}
}
