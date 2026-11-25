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

type authorizedScheduleHTTPModel struct {
	workerTestModel
	schedule    bool
	direct      bool
	pendingDone bool
	control     map[string]any
	cadence     *scheduleCadenceModel
	pausedRead  *pausedScheduleReadModel
	// Only owner acknowledgments are chat-only; scheduled report work retains
	// its ordinary file-delivery contract.
	chatOnly    bool
	deliverySet bool
	work        func() (*store.BotWorkState, error)
}

func (m *authorizedScheduleHTTPModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(r, nil); ok {
		return response
	}
	if m.chatOnly && !m.deliverySet {
		return func(yield func(*model.LLMResponse, error) bool) {
			_, direct := r.Tools["bot_work_update"]
			if _, bridge := r.Tools["execute_tool"]; !direct && !bridge {
				yield(nil, fmt.Errorf("conversational fixture requires authorized bot_work_update or Chat dispatch bridge"))
				return
			}
			work, err := m.work()
			if err != nil || work == nil {
				yield(nil, fmt.Errorf("load conversational delivery revision: work=%v err=%v", work, err))
				return
			}
			m.deliverySet = true
			call := &genai.FunctionCall{ID: "schedule-chat-delivery", Name: "bot_work_update", Args: map[string]any{"expected_revision": work.Revision, "kind": "set_delivery", "delivery_intent": string(store.BotDeliveryChatOnly)}}
			if !direct {
				call.Name, call.Args = "execute_tool", map[string]any{"name": "bot_work_update", "arguments": call.Args}
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
		}
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
			// This agent-led producer owns the wording; no mandatory reviewer
			// rewrites an invented application claim into a pending receipt.
			yield(&model.LLMResponse{Content: genai.NewContentFromText("The schedule request is pending; application is not yet verified.", "model"), TurnComplete: true}, nil)
		}
	}
	if !m.schedule {
		return m.workerTestModel.GenerateContent(ctx, r, stream)
	}
	m.schedule = false
	m.pendingDone = true
	return func(yield func(*model.LLMResponse, error) bool) {
		args := map[string]any{"name": "Daily report", "schedule": "0 9 * * *", "timezone": "UTC", "instructions": "Read the daily report", "invocation": "Deliver the daily report", "mode": "adaptive"}
		call := &genai.FunctionCall{ID: uuid.NewString(), Name: "schedule_job", Args: args}
		if _, available := r.Tools["schedule_job"]; !m.direct || !available {
			call.Name, call.Args = "execute_tool", map[string]any{"name": "schedule_job", "arguments": args}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
	}
}
func TestAuthorizedScheduleHTTPConversationalDelivery(t *testing.T) {
	m := &authorizedScheduleHTTPModel{chatOnly: true, pendingDone: true}
	revision := int64(7)
	m.work = func() (*store.BotWorkState, error) {
		return &store.BotWorkState{Revision: revision}, nil
	}
	request := &model.LLMRequest{Tools: map[string]any{"bot_work_update": struct{}{}}}
	for _, want := range []int64{7, 11} {
		// Prepare resets the declaration for each owner run; never reuse the
		// preceding run's revision or suppress its actual tool invocation.
		revision, m.deliverySet = want, false
		responses := 0
		for response, err := range m.GenerateContent(t.Context(), request, false) {
			if err != nil {
				t.Fatal(err)
			}
			responses++
			call := response.Content.Parts[0].FunctionCall
			if call == nil || call.Name != "bot_work_update" || call.Args["expected_revision"] != want || call.Args["kind"] != "set_delivery" || call.Args["delivery_intent"] != string(store.BotDeliveryChatOnly) {
				t.Fatalf("wrong delivery protocol: %+v", call)
			}
		}
		if responses != 1 || !m.deliverySet || !m.pendingDone {
			t.Fatalf("declaration consumed or repeated the producer: responses=%d model=%+v", responses, m)
		}
	}
	for response, err := range m.GenerateContent(t.Context(), request, false) {
		if err != nil || response.Content.Parts[0].FunctionCall != nil || !strings.Contains(response.Content.Parts[0].Text, "pending") {
			t.Fatalf("declaration did not resume pending acknowledgment: %+v %v", response, err)
		}
	}
	m.deliverySet = false
	for response, err := range m.GenerateContent(t.Context(), &model.LLMRequest{}, false) {
		if response != nil || err == nil || !strings.Contains(err.Error(), "authorized bot_work_update") {
			t.Fatalf("missing tool must remain a blocker: %+v %v", response, err)
		}
	}
	if m.deliverySet {
		t.Fatal("missing tool changed delivery state")
	}
	// An actual report task is not relabeled chat-only by this wrapper.
	m.chatOnly, m.pendingDone = false, true
	for response, err := range m.GenerateContent(t.Context(), request, false) {
		if err != nil || response.Content.Parts[0].FunctionCall != nil || m.deliverySet {
			t.Fatalf("nonconversational run acquired chat-only delivery: %+v %v", response, err)
		}
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
			llm := &authorizedScheduleHTTPModel{schedule: true, direct: scope == store.BotScopePersonal}
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				return newProductionBotMandatePolicy(db, &config.AppConfig{})(ctx, ref)
			}
			backend := newRegressionHomeBackend(t)
			if err := db.InstallBotContentHomeBackend(backend); err != nil {
				t.Fatal(err)
			}
			workerConfig := BotWorkerConfig{Backend: db, MandatePolicy: policy, TurnRegistry: registry, Sandbox: backend, Pool: workerTestPool{}, WorkerID: "schedule-http", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				llm.chatOnly, llm.deliverySet = q.Run.IsInteractiveChat(), false
				llm.work = func() (*store.BotWorkState, error) {
					return repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease)
				}
				s.Services.BotFilesFactory = func(c context.Context, svc *store.Services, scope store.BotScope) (store.BotFiles, error) {
					return productionBotFiles(c, backend, svc, scope)
				}
				return &launcher.BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: &titleSkillPool{}}, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
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
			b, err = worker.provisionHome(auth, repo, b.ID, b.Revision)
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
			if saved.FinishedAt == nil || ops[0].Receipt.ObservedAt.Before(*saved.FinishedAt) || len(saved.Report.Unchanged) != 2 || !strings.HasSuffix(saved.Report.Unchanged[0].Source, "/schedule-chat-delivery") || !strings.Contains(saved.Report.Unchanged[1].Source, ops[0].ID.String()) {
				t.Fatal("applied receipt must follow the terminal source and refer to its separately acknowledged operation")
			}
			approvals, err := repo.ListApprovals(auth, b.ID, store.BotPage{})
			if err != nil || len(approvals) != 0 {
				t.Fatalf("unexpected approvals: %+v %v", approvals, err)
			}
			if len(jobs.List(auth)) != 1 {
				t.Fatal("expected exactly one persisted job")
			}
			currentBot, err := repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			currentVersion, err := repo.GetVersion(auth, b.ID, currentBot.Version)
			if err != nil || len(currentVersion.Triggers) != 1 {
				t.Fatalf("missing persisted task schedule: %+v %v", currentVersion, err)
			}
			trigger := currentVersion.Triggers[0]
			if trigger.SkillID != nil || trigger.Invocation != "" || trigger.Instructions != "Read the daily report" {
				t.Fatalf("unbound invocation must not require or invent a skill: %+v", trigger)
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
				var persisted *store.BotRun
				var operations []store.BotOperation
				for attempt := 0; attempt < 3; attempt++ {
					if e = worker.Poll(ctx); e != nil {
						t.Fatal(e)
					}
					persisted, e = repo.GetRun(auth, b.ID, turn.Run.ID)
					if e != nil || persisted == nil || persisted.MandateRef == nil {
						t.Fatalf("missing control mandate: %+v %v", persisted, e)
					}
					operations, e = rt.ListAuthorizedOperations(auth, *persisted.MandateRef)
					if e != nil {
						t.Fatal(e)
					}
					// Poll is synchronous and normally applies the operation before
					// returning. Stop on the durable outcome, rather than charge two
					// extra full maintenance scans to this admission-to-receipt limit.
					if len(operations) == 1 && operations[0].Status == store.BotOperationApplied {
						break
					}
				}
				if len(operations) != 1 || operations[0].Status != store.BotOperationApplied {
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
			if err != nil || cadenceRun.Status != store.RunSucceeded || cadenceRun.Usage.ToolCalls != 3 || len(cadenceRun.Report.Unverified) != 0 || len(cadenceRun.Report.Changed) != 0 || len(cadenceRun.Report.Unchanged) != 3 || !strings.Contains(cadenceRun.Report.Summary, "pending") || cadenceRun.Report.NextRunAt != nil {
				t.Fatalf("cadence source accounting: %+v %v", cadenceRun, err)
			}
			if !strings.HasSuffix(cadenceRun.Report.Unchanged[0].Source, "/schedule-chat-delivery") || !strings.Contains(cadenceRun.Report.Unchanged[1].Source, "cadence-list") || cadence.Receipt.ObservedAt.Before(*cadenceRun.FinishedAt) || cadence.Receipt.Schedule.TriggerID != receipt.TriggerID || cadence.Receipt.Schedule.ScheduleID != receipt.ScheduleID {
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
			if e != nil || readRun.Status != store.RunSucceeded || readRun.Usage.ToolCalls != 3 || len(readRun.Report.Unverified) != 0 || len(readRun.Report.Changed) != 0 || len(readRun.Report.Unchanged) != 3 || readRun.Report.NextRunAt != nil {
				t.Fatalf("paused read accounting: %+v %v", readRun, e)
			}
			// Both the list and independently verified pending capture are observations,
			// not mutations. The worker's later receipt establishes application.
			if !strings.HasSuffix(readRun.Report.Unchanged[0].Source, "/schedule-chat-delivery") {
				t.Fatalf("missing exact delivery declaration: %+v", readRun.Report.Unchanged)
			}
			listObservation, captureObservation := readRun.Report.Unchanged[1], readRun.Report.Unchanged[2]
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
			if err := db.InstallBotContentHomeBackend(backend); err != nil {
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
			if result.Report.Summary != "durable controlled result" || result.Report.Quality != nil || result.Report.Work == nil || result.Report.Work.Protocol != store.BotWorkProtocolAgentLedV1 || result.Report.Work.Phase != store.BotWorkFinished || result.Report.Work.Disposition != store.AgentWorkAnswered {
				t.Fatalf("scheduled result lacks committed agent-led content: %+v", result.Report)
			}
			events, err := ss.ReadTranscriptEvents(auth, "astonish", owner.String(), b.HomeSessionID.String())
			if err != nil {
				t.Fatal(err)
			}
			if result.Report.ValidateEmployeeMessageIntegrity(result.Status) != nil {
				t.Fatal("scheduled result has no validated employee message")
			}
			scheduleReport, runReport := false, false
			canonicalSchedule := false
			scheduleAt, runAt, runReports := -1, -1, 0
			for eventIndex, e := range events {
				raw, _ := json.Marshal(e.Actions.StateDelta[store.BotOperationKey])
				var view store.BotOperationView
				if json.Unmarshal(raw, &view) == nil && view.Operation == store.BotControlScheduleCreate && view.Status == store.BotOperationApplied {
					canonicalSchedule = true
					if view.OperationID == ops[0].ID {
						scheduleAt = eventIndex
					}
				}
				if e.Content != nil {
					for _, p := range e.Content.Parts {
						scheduleReport = scheduleReport || strings.Contains(p.Text, "Schedule schedule_create applied")
						if strings.TrimSpace(p.Text) == strings.TrimSpace(bot.RenderReport(*result)) {
							runReport = true
							runAt = eventIndex
							runReports++
						}
					}
				}
			}
			if scheduleReport || !canonicalSchedule || !runReport || scheduleAt < 0 || runAt <= scheduleAt || runReports != 1 {
				t.Fatalf("Home must confirm the original schedule before exactly one scheduled result: schedule=%t run=%t scheduleAt=%d runAt=%d reports=%d", scheduleReport, runReport, scheduleAt, runAt, runReports)
			}
		})
	}
}
