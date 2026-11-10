package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"

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
)

type lifecycleHTTPBackend struct{ activationHomeBackend }

func (lifecycleHTTPBackend) VerifySessionDestroyed(context.Context, string) error { return nil }

type authorizedScheduleHTTPModel struct {
	workerTestModel
	schedule    bool
	pendingDone bool
	control     map[string]any
	cadence     *scheduleCadenceModel
	pausedRead  *pausedScheduleReadModel
}

func (m *authorizedScheduleHTTPModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(r, nil); ok {
		return response
	}
	if m.pausedRead != nil {
		return m.pausedRead.GenerateContent(ctx, r, stream)
	}
	if m.cadence != nil {
		return m.cadence.GenerateContent(ctx, r, stream)
	}
	if m.control != nil {
		payload := m.control
		m.control = nil
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "propose_bot_control", Args: map[string]any{"payload": payload}}}}}}, nil)
		}
	}
	if m.pendingDone {
		m.pendingDone = false
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Done — I've set up the schedule.", "model"), TurnComplete: true}, nil)
		}
	}
	if !m.schedule {
		return m.workerTestModel.GenerateContent(ctx, r, stream)
	}
	m.schedule = false
	m.pendingDone = true
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "execute_tool", Args: map[string]any{"name": "schedule_job", "arguments": map[string]any{"name": "Daily report", "schedule": "0 9 * * *", "timezone": "UTC", "instructions": "Read the daily report", "mode": "adaptive"}}}}}}}, nil)
	}
}
func authorizedHTTPPolicy(_ context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
	return store.BotMandatePolicy{TTL: 24 * time.Hour, Scopes: []string{string(execution.CapabilityChat), string(execution.CapabilityToolExecute)}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: []string{"schedule_job", "update_scheduled_job", "remove_scheduled_job", "read_file"}}}, Limits: store.BotMandateLimits{MinScheduleIntervalSeconds: 60, MaxSchedules: 4, MaxDailyRuns: 10, MaxConcurrentRuns: 1, MaxRuntimeSeconds: 300, MaxToolCalls: 20, MaxTokens: 20000, MaxTotalToolCalls: 100, MaxTotalTokens: 100000}}, nil
}

// Each independently checkable stage gets a bounded wall-clock budget. Race
// instrumentation makes SQLite migrations/reopen and the many authorization
// reads much slower; one shared 15s budget let earlier stages starve the final
// scheduled run. Keep real clocks, lease durations and synchronous polls intact.
const authorizedScheduleHTTPStageTimeout = 15 * time.Second

func authorizedScheduleHTTPStage(t *testing.T, parent context.Context, name string) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, authorizedScheduleHTTPStageTimeout)
	started := time.Now()
	t.Logf("starting stage %q", name)
	return ctx, func() {
		t.Helper()
		defer cancel()
		t.Logf("stage %q elapsed=%s context=%v", name, time.Since(started), ctx.Err())
		if err := ctx.Err(); err != nil {
			t.Errorf("stage %q: %v", name, err)
		}
	}
}

func TestAuthorizedScheduleHTTPStageBoundsAndIsolation(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	parent, err := execution.WithPrincipal(parent, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	first, finishFirst := authorizedScheduleHTTPStage(t, parent, "first")
	deadline, ok := first.Deadline()
	if !ok || time.Until(deadline) > authorizedScheduleHTTPStageTimeout {
		t.Fatalf("stage lacks bounded deadline: %v %t", deadline, ok)
	}
	finishFirst()
	if first.Err() != context.Canceled || parent.Err() != nil {
		t.Fatalf("finishing stage must cancel only its own context: first=%v parent=%v", first.Err(), parent.Err())
	}
	second, finishSecond := authorizedScheduleHTTPStage(t, parent, "second")
	defer finishSecond()
	if second.Err() != nil {
		t.Fatalf("later stage inherited cancellation: %v", second.Err())
	}
	want, _ := execution.PrincipalFromContext(parent)
	got, ok := execution.PrincipalFromContext(second)
	if !ok || got.Subject != want.Subject || got.OrgSlug != want.OrgSlug || got.TeamSlug != want.TeamSlug {
		t.Fatalf("stage lost authenticated identity: %+v", got)
	}
}

func TestAuthorizedScheduleHTTPStageHonorsParentDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx, finish := authorizedScheduleHTTPStage(t, parent, "short parent")
	defer finish()
	want, _ := parent.Deadline()
	got, ok := ctx.Deadline()
	if !ok || !got.Equal(want) {
		t.Fatalf("stage extended parent deadline: got %v want %v", got, want)
	}
}

// A real HTTP owner admission invokes the actual scheduling tool and worker. The
// model and isolated compute are controlled; no operation or run pin is forged.
func TestBotAuthorizedScheduleHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, finishStage := authorizedScheduleHTTPStage(t, t.Context(), "provision")
			defer func() { finishStage() }()
			dir := t.TempDir()
			cfg := entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
			_, db, err := entstore.NewPlatformServices(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			org := &store.Organization{ID: uuid.NewString(), Slug: "scheduling", Name: "Scheduling", Status: "active"}
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
			ss := ods.ForTeam(team.Slug).Sessions()
			jobs := ods.ForTeam(team.Slug).ScheduledJobs()
			pin := ods.ForTeam(team.Slug).SetSessionPin
			if scope == store.BotScopePersonal {
				repo = ods.ForUser(owner.String()).Bots()
				ss = ods.ForUser(owner.String()).Sessions()
				jobs = ods.ForUser(owner.String()).ScheduledJobs()
				pin = ods.ForUser(owner.String()).SetSessionPin
			}
			llm := &authorizedScheduleHTTPModel{schedule: true}
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				return newProductionBotMandatePolicy(db, &config.AppConfig{})(ctx, ref)
			}
			workerConfig := BotWorkerConfig{Backend: db, MandatePolicy: policy, TurnRegistry: registry, Sandbox: lifecycleHTTPBackend{}, Pool: workerTestPool{}, WorkerID: "schedule-http", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(_ context.Context, q bot.RunRequest, _ *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				return &launcher.BotChatSafeResources{WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}}
			worker, err := NewBotWorker(workerConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { worker.Stop() }()
			worker.schedulerReady.Store(true)
			auth, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			// Keep identity/evidence on a stable parent, not a completed stage.
			// Every subsequent store read and worker call uses the new stage context.
			authParent := auth
			nextStage := func(name string) {
				t.Helper()
				finishStage()
				ctx, finishStage = authorizedScheduleHTTPStage(t, authParent, name)
				auth = ctx
			}
			nextStage("source pending acknowledgment and applied receipt")
			v := store.DefaultBotVersion()
			v.Charter = "Read the daily report"
			v.Budget.MaxTokens = nil
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Reporter", OwnerID: owner, Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			internal, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
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
			httpCtx := store.WithServices(store.WithTenantContext(authParent, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug}), svc)
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCtx, cancel := context.WithTimeout(httpCtx, authorizedScheduleHTTPStageTimeout)
				defer cancel()
				router.ServeHTTP(w, r.WithContext(requestCtx))
			}))
			defer server.Close()
			post := func(body string) *http.Response {
				t.Helper()
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/bots/"+b.ID.String()+"/messages?scope="+string(scope), strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				response, err := server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			body := fmt.Sprintf(`{"version":%d,"idempotency_key":"owner-schedule","instructions":"Every day at nine, read the daily report."}`, b.Version)
			resp := post(body)
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 202 {
				t.Fatalf("admit %d: %s", resp.StatusCode, raw)
			}
			var source store.BotMessageReceipt
			if err = json.Unmarshal(raw, &source); err != nil {
				t.Fatal(err)
			}
			if err = worker.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.GetRun(auth, b.ID, source.Run.ID)
			if err != nil || saved.Status != store.RunSucceeded || len(saved.Report.Unverified) != 0 || len(saved.Report.Changed) != 0 || saved.Report.NextRunAt != nil || strings.Contains(saved.Report.Summary, "Done") || !strings.Contains(saved.Report.Summary, "pending") {
				t.Fatalf("source must acknowledge enqueue, not application: %+v %v", saved, err)
			}
			rt, err := worker.AuthorizedOperations(repo)
			if err != nil {
				t.Fatal(err)
			}
			ops, err := rt.ListAuthorizedOperations(auth, *saved.MandateRef)
			if err != nil || len(ops) != 1 {
				t.Fatalf("operations %+v %v", ops, err)
			}
			if ops[0].Status != store.BotOperationApplied || ops[0].Receipt.Schedule.NextRun == nil || !ops[0].Receipt.Schedule.Enabled {
				t.Fatalf("not applied %+v", ops[0])
			}
			if saved.FinishedAt == nil || ops[0].Receipt.ObservedAt.Before(*saved.FinishedAt) || len(saved.Report.Unchanged) != 1 || !strings.Contains(saved.Report.Unchanged[0].Source, ops[0].ID.String()) {
				t.Fatal("applied receipt must follow the terminal source and refer to its separately acknowledged operation")
			}
			approvals, err := repo.ListApprovals(auth, b.ID, store.BotPage{})
			if err != nil || len(approvals) != 0 {
				t.Fatalf("unexpected approvals: %+v %v", approvals, err)
			}
			if len(jobs.List(auth)) != 1 {
				t.Fatal("expected exactly one persisted job")
			}
			receipt := ops[0].Receipt.Schedule
			// Compose owner controls over real HTTP, without forged operations or pins.
			ownerTurn := func(text string, payload map[string]any) store.BotOperation {
				t.Helper()
				llm.control = payload
				nextStage(text)
				current, e := repo.Get(auth, b.ID)
				if e != nil {
					t.Fatal(e)
				}
				encoded, _ := json.Marshal(map[string]any{"version": current.Version, "idempotency_key": uuid.NewString(), "instructions": text})
				response := post(string(encoded))
				data, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode != http.StatusAccepted {
					t.Fatalf("control admission %d: %s", response.StatusCode, data)
				}
				var turn store.BotMessageReceipt
				if e = json.Unmarshal(data, &turn); e != nil {
					t.Fatal(e)
				}
				for attempt := 0; attempt < 3; attempt++ {
					if e = worker.Poll(ctx); e != nil {
						t.Fatal(e)
					}
				}
				persisted, e := repo.GetRun(auth, b.ID, turn.Run.ID)
				if e != nil || persisted.MandateRef == nil {
					t.Fatalf("missing control mandate: %+v %v", persisted, e)
				}
				operations, e := rt.ListAuthorizedOperations(auth, *persisted.MandateRef)
				if e != nil || len(operations) != 1 || operations[0].Status != store.BotOperationApplied {
					t.Fatalf("control %s: %+v %v run=%+v", text, operations, e, persisted)
				}
				return operations[0]
			}
			llm.cadence = &scheduleCadenceModel{t: t, bridge: scope == store.BotScopeTeam, pending: func(id string) {
				job := jobs.Get(auth, receipt.ScheduleID.String())
				if job == nil || job.Schedule.Cron != "0 9 * * *" {
					t.Fatalf("update applied before source completion: %+v", job)
				}
				runs, e := repo.ListRuns(auth, b.ID, store.BotPage{})
				if e != nil {
					t.Fatal(e)
				}
				var running store.BotRun
				for _, run := range runs {
					if run.Status == store.RunRunning {
						running = run
					}
				}
				if running.ID == uuid.Nil || running.MandateRef == nil {
					t.Fatalf("source not running: %+v", runs)
				}
				op, e := rt.ReadAuthorizedOperation(auth, *running.MandateRef, uuid.MustParse(id))
				if e != nil || op.Status != store.BotOperationPending || op.Receipt != nil {
					t.Fatalf("pending readback: %+v %v", op, e)
				}
			}}
			cadence := ownerTurn("Change the daily report to every two minutes instead.", nil)
			if llm.cadence.step != 3 {
				t.Fatalf("cadence retried: %d", llm.cadence.step)
			}
			llm.cadence = nil
			cadenceRun, err := repo.GetRun(auth, b.ID, cadence.Provenance.SourceRunID)
			if err != nil || cadenceRun.Status != store.RunSucceeded || cadenceRun.Usage.ToolCalls != 2 || len(cadenceRun.Report.Unverified) != 0 || len(cadenceRun.Report.Changed) != 0 || len(cadenceRun.Report.Unchanged) != 2 || !strings.Contains(cadenceRun.Report.Summary, "pending") || cadenceRun.Report.NextRunAt != nil {
				t.Fatalf("cadence source accounting: %+v %v", cadenceRun, err)
			}
			if !strings.Contains(cadenceRun.Report.Unchanged[0].Source, "cadence-list") || cadence.Receipt.ObservedAt.Before(*cadenceRun.FinishedAt) || cadence.Receipt.Schedule.TriggerID != receipt.TriggerID || cadence.Receipt.Schedule.ScheduleID != receipt.ScheduleID {
				t.Fatalf("cadence lost call/target/source ordering: %+v", cadence)
			}
			jobAfter := jobs.Get(auth, receipt.ScheduleID.String())
			if jobAfter == nil || jobAfter.Schedule.Cron != "*/2 * * * *" || !jobAfter.Enabled || jobAfter.NextRun == nil {
				t.Fatalf("cadence not committed: %+v", jobAfter)
			}
			approvals, err = repo.ListApprovals(auth, b.ID, store.BotPage{})
			if err != nil || len(approvals) != 0 {
				t.Fatalf("cadence requested review: %+v %v", approvals, err)
			}
			llm.schedule = true
			other := ownerTurn("Create another daily report schedule.", nil)
			if other.Receipt.Schedule == nil {
				t.Fatal("missing second schedule")
			}
			ownerTurn("Pause immediately.", map[string]any{"operation": "priority_pause", "reason": "Owner requested pause"})
			for _, job := range jobs.List(auth) {
				if job.Enabled {
					t.Fatal("pause left a schedule enabled")
				}
			}
			ownerTurn("While paused, create a report goal.", map[string]any{"operation": "goal_create", "reason": "Owner requested paused change", "goal": map[string]any{"goal": map[string]any{"title": "Paused report", "description": "Prepare report", "completion_criteria": "Delivered", "status": "active"}}})
			paused, e := repo.Get(auth, b.ID)
			if e != nil || paused.Status != store.BotPaused {
				t.Fatalf("paused change activated bot: %+v %v", paused, e)
			}
			for _, job := range jobs.List(auth) {
				if job.Enabled {
					t.Fatal("paused change enabled schedule")
				}
			}
			llm.pausedRead = &pausedScheduleReadModel{t: t, bridge: scope == store.BotScopeTeam, inspected: func() {
				current, e := repo.Get(auth, b.ID)
				if e != nil || current.Status != store.BotPaused {
					t.Fatalf("inspection resumed bot: %+v %v", current, e)
				}
				for _, job := range jobs.List(auth) {
					if job.Enabled || job.NextRun != nil {
						t.Fatalf("inspection enabled work: %+v", job)
					}
				}
			}}
			resumed := ownerTurn("Inspect the paused schedules and resume only the first report schedule.", nil)
			if llm.pausedRead.step != 3 || llm.pausedRead.target != receipt.ScheduleID.String() {
				t.Fatalf("wrong inspected resume target: %+v", llm.pausedRead)
			}
			llm.pausedRead = nil
			readRun, e := repo.GetRun(auth, b.ID, resumed.Provenance.SourceRunID)
			if e != nil || readRun.Status != store.RunSucceeded || readRun.Usage.ToolCalls != 2 || len(readRun.Report.Unverified) != 0 || len(readRun.Report.Changed) != 0 || len(readRun.Report.Unchanged) != 2 || readRun.Report.NextRunAt != nil {
				t.Fatalf("paused read accounting: %+v %v", readRun, e)
			}
			// Both the list and independently verified pending capture are observations,
			// not mutations. The worker's later receipt establishes application.
			listObservation, captureObservation := readRun.Report.Unchanged[0], readRun.Report.Unchanged[1]
			if listObservation.Kind != store.BotEvidenceObservation || !strings.HasSuffix(listObservation.Source, "/"+readRun.ID.String()+"/paused-list") {
				t.Fatalf("lost exact-call list evidence: %+v", listObservation)
			}
			if captureObservation.Kind != store.BotEvidenceObservation || captureObservation.Source != "bot-operation://"+b.ID.String()+"/"+resumed.ID.String() || !strings.Contains(captureObservation.Summary, "pending, not applied") || !strings.Contains(readRun.Report.Summary, "pending, not applied") {
				t.Fatalf("pending resume claimed application or lost operation evidence: %+v", readRun.Report)
			}
			mandates, e := entstore.NewBotMandateRuntime(repo, policy)
			if e != nil {
				t.Fatal(e)
			}
			record, e := mandates.ReadBotMandate(auth, *readRun.MandateRef)
			if e != nil {
				t.Fatal(e)
			}
			assertPausedReadMandate(t, record.Mandate)
			if job := jobs.Get(auth, receipt.ScheduleID.String()); job == nil || !job.Enabled {
				t.Fatal("target schedule not resumed")
			}
			if job := jobs.Get(auth, other.Receipt.Schedule.ScheduleID.String()); job == nil || job.Enabled {
				t.Fatal("unselected schedule resumed")
			}
			nextStage("offline reopen")
			worker.Stop()
			registry.Close()
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			_, db, err = entstore.NewPlatformServices(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			ods, err = db.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			repo = ods.ForTeam(team.Slug).Bots()
			ss = ods.ForTeam(team.Slug).Sessions()
			jobs = ods.ForTeam(team.Slug).ScheduledJobs()
			if scope == store.BotScopePersonal {
				repo = ods.ForUser(owner.String()).Bots()
				ss = ods.ForUser(owner.String()).Sessions()
				jobs = ods.ForUser(owner.String()).ScheduledJobs()
			}
			workerConfig.Backend = db
			workerConfig.TurnRegistry = nil
			worker, err = NewBotWorker(workerConfig)
			if err != nil {
				t.Fatal(err)
			}
			worker.schedulerReady.Store(true)
			job := jobs.Get(auth, receipt.ScheduleID.String())
			if job == nil || !job.Enabled || job.NextRun == nil {
				t.Fatal("lost enabled schedule after reopen")
			}
			nextStage("scheduled delivery and Home receipt")
			due := time.Now().UTC().Add(-time.Second)
			job.NextRun = &due
			if err = jobs.Update(auth, job); err != nil {
				t.Fatal(err)
			}
			executor := &scheduler.Executor{}
			mts := NewMultiTenantScheduler(db, executor, nil, nil)
			mts.SetBotSchedulerCallbacks(worker.SchedulerCallbacks())
			if scope == store.BotScopePersonal {
				mts.executePersonalJob(ctx, job, org.Slug, ods, ods.ForUser(owner.String()), jobs, owner.String())
			} else {
				mts.executeTeamJob(ctx, job, org.Slug, team.Slug, ods, ods.ForTeam(team.Slug), jobs)
			}
			runs, err := repo.ListRuns(auth, b.ID, store.BotPage{})
			if err != nil {
				t.Fatal(err)
			}
			var admitted struct{ Run store.BotRun }
			for _, run := range runs {
				if run.Trigger == store.TriggerCron {
					admitted.Run = run
				}
			}
			if admitted.Run.ID == uuid.Nil {
				t.Fatalf("scheduler did not admit occurrence: %+v", jobs.Get(auth, job.ID))
			}
			if admitted.Run.MandateRef == nil || *admitted.Run.MandateRef == *saved.MandateRef {
				t.Fatal("no exact future task pin")
			}
			taskRuntime, e := entstore.NewBotMandateRuntime(repo, policy)
			if e != nil {
				t.Fatal(e)
			}
			taskRecord, e := taskRuntime.ReadBotMandate(auth, *admitted.Run.MandateRef)
			if e != nil {
				t.Fatal(e)
			}
			for _, c := range taskRecord.Mandate.Constraints {
				for _, resource := range c.Resources {
					if resource == "propose_bot_control" || resource == "schedule_job" || strings.HasPrefix(resource, "bot_control:") {
						t.Fatalf("scheduled child inherited own-control %s", resource)
					}
				}
			}
			if err = worker.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			result, err := repo.GetRun(auth, b.ID, admitted.Run.ID)
			if err != nil || result.Status != store.RunSucceeded {
				t.Fatalf("future run %+v %v", result, err)
			}
			if result.Report.Summary != "durable controlled result" || result.Report.Quality == nil || result.Report.Quality.Outcome != store.BotWorkAccepted {
				t.Fatalf("scheduled result lacks reviewed controlled content: %+v", result.Report)
			}
			events, err := ss.ReadTranscriptEvents(auth, "astonish", owner.String(), b.HomeSessionID.String())
			if err != nil {
				t.Fatal(err)
			}
			scheduleReport, runReport := false, false
			canonicalSchedule := false
			for _, e := range events {
				raw, _ := json.Marshal(e.Actions.StateDelta[store.BotOperationKey])
				var view store.BotOperationView
				if json.Unmarshal(raw, &view) == nil && view.Operation == store.BotControlScheduleCreate && view.Status == store.BotOperationApplied {
					canonicalSchedule = true
				}
				if e.Content != nil {
					for _, p := range e.Content.Parts {
						scheduleReport = scheduleReport || strings.Contains(p.Text, "Schedule schedule_create applied")
						runReport = runReport || p.Text == bot.RenderReport(*result)
					}
				}
			}
			if scheduleReport || !canonicalSchedule || !runReport {
				t.Fatalf("Home reports schedule=%t run=%t", scheduleReport, runReport)
			}
		})
	}
}
