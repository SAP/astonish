package daemon

import (
	"context"
	"crypto/sha256"
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
	"google.golang.org/genai"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type employeeOperationsModel struct {
	workerTestModel
	capture bool
	payload map[string]any
}

func (m *employeeOperationsModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(r, nil); ok {
		return response
	}
	if !m.capture {
		return m.workerTestModel.GenerateContent(ctx, r, stream)
	}
	m.capture = false
	return func(yield func(*model.LLMResponse, error) bool) {
		payload := m.payload
		if payload == nil {
			payload = map[string]any{"operation": "goal_create", "reason": "Owner requested goal", "goal": map[string]any{"goal": map[string]any{"title": "Ship report", "description": "Prepare the report", "completion_criteria": "Report delivered", "status": "active"}}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "propose_bot_control", Args: map[string]any{"payload": payload}}}}}}, nil)
	}
}

// Real HTTP admission, production scoped database and worker, controlled model/compute.
func TestBotEmployeeOperationsHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"goal", "paused_goal", "task_once", "priority_pause", "deny", "memory_deny", "approve", "expiry"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				org := &store.Organization{ID: uuid.NewString(), Slug: "routine", Name: "Routine", Status: "active"}
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
				policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
					p, e := authorizedHTTPPolicy(ctx, ref)
					resources := []string{"propose_bot_control"}
					if scenario == "goal" || scenario == "paused_goal" {
						resources = append(resources, entstore.RoutineControlResource(store.BotControlGoalCreate))
					}
					if scenario == "task_once" {
						resources = append(resources, entstore.RoutineControlResource(store.BotControlTaskOnceOperation))
					}
					if scenario == "priority_pause" {
						resources = append(resources, entstore.RoutineControlResource(store.BotControlPriorityPause))
					}
					p.Constraints = append(p.Constraints, execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: resources})
					p.ExceptionConstraints = []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{entstore.RoutineControlResource(store.BotControlGoalCreate)}}}
					return p, e
				}
				llm := &employeeOperationsModel{capture: true}
				if scenario == "task_once" {
					llm.payload = map[string]any{"operation": "task_once", "reason": "Owner requested one task", "task_once": map[string]any{"instructions": "Prepare exactly one report."}}
				}
				if scenario == "priority_pause" {
					llm.payload = map[string]any{"operation": "priority_pause", "reason": "Owner requested immediate pause"}
				}
				registry := execution.NewBotTurnAuthorizationRegistry(10)
				defer registry.Close()
				physical := &priorityHTTPBackend{}
				worker, err := NewBotWorker(BotWorkerConfig{Backend: db, MandatePolicy: policy, TurnRegistry: registry, Sandbox: physical, Pool: workerTestPool{}, WorkerID: "routine-http", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
					return llm, nil
				}, Prepare: func(_ context.Context, q bot.RunRequest, _ *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
					return &launcher.BotChatSafeResources{WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Stop()
				auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
				if err != nil {
					t.Fatal(err)
				}
				auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				v := store.DefaultBotVersion()
				v.Charter = "Prepare reports"
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
				var memories store.MemoryStore
				var memoryBefore *store.MemorySearchResult
				if scenario == "memory_deny" {
					memories = ods.ForTeam(team.Slug).Memories()
					if scope == store.BotScopePersonal {
						memories = ods.ForUser(owner.String()).Memories()
					}
					if err = memories.Add(auth, store.MemoryEntry{Content: "Prefers Celsius", Category: "fact", SessionID: b.HomeSessionID.String(), CreatedBy: owner.String()}); err != nil {
						t.Fatal(err)
					}
					rows, e := memories.ListBySession(auth, b.HomeSessionID.String())
					if e != nil || len(rows) != 1 {
						t.Fatalf("memory fixture: %+v %v", rows, e)
					}
					memoryBefore = &rows[0]
					llm.payload = map[string]any{"operation": store.BotControlMemoryForget, "reason": "Owner requested forget", "memory": map[string]any{"memory_id": memoryBefore.ID, "expected_revision": b.Revision, "expected_fingerprint": store.MemoryContentFingerprint(*memoryBefore)}}
				}
				if scenario == "paused_goal" {
					b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "initial activation"})
					if err != nil {
						t.Fatal(err)
					}
					b, err = repo.Pause(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "pause before owner control"})
					if err != nil {
						t.Fatal(err)
					}
				}
				var busy *store.BotRun
				var busyLease store.BotLease
				jobs := ods.ForTeam(team.Slug).ScheduledJobs()
				if scope == store.BotScopePersonal {
					jobs = ods.ForUser(owner.String()).ScheduledJobs()
				}
				if scenario == "priority_pause" {
					b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "start autonomous work"})
					if err != nil {
						t.Fatal(err)
					}
					tid := uuid.New()
					triggers := repo.(store.BotTriggerStore)
					b, err = triggers.ChangeTrigger(auth, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "routine", Definition: store.TriggerSpec{Kind: store.TriggerCron, Instructions: "Observe", Cron: "* * * * *", Timezone: "UTC"}})
					if err != nil {
						t.Fatal(err)
					}
					b, err = triggers.ChangeTrigger(auth, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "routine"})
					if err != nil {
						t.Fatal(err)
					}
					job := jobs.List(auth)[0]
					due := time.Now().Add(-time.Second)
					job.NextRun = &due
					if err = jobs.Update(auth, job); err != nil {
						t.Fatal(err)
					}
					admitted, e := repo.(store.BotScheduledRunStore).AdmitScheduledRun(auth, store.BotScheduledOccurrence{JobID: job.ID, BotID: b.ID, Version: b.Version, TriggerID: tid, OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, tid, due.UTC().Format(time.RFC3339Nano)))))})
					if e != nil {
						t.Fatal(e)
					}
					busy = &admitted.Run
					wc := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: busy.ID, Version: busy.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
					busyLease, err = repo.ClaimRun(wc, store.BotClaimRequest{BotID: b.ID, RunID: busy.ID, WorkerID: "routine-http", ExpiresAt: time.Now().Add(time.Minute)})
					if err != nil {
						t.Fatal(err)
					}
					physical.pending = true
				}
				svc := &store.Services{Mode: store.ModePlatform, TenantRouter: db, BotMandatePolicy: policy, BotTurnRegistry: registry, BotHomeSetup: worker.provisionHome}
				// Exercise the same bootstrap and request-scoped clone as daemon.Run.
				// Installing a factory directly here hid the production denial failure.
				setBotInteractiveReady(svc, worker)
				httpCtx := store.WithTenantContext(auth, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
				router := mux.NewRouter()
				api.RegisterRoutes(router, svc, db, entstore.TenantMiddleware(db))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
				defer server.Close()
				body := fmt.Sprintf(`{"version":%d,"idempotency_key":"owner-goal","instructions":"Create a goal to ship the report."}`, b.Version)
				resp, err := server.Client().Post(server.URL+"/api/studio/bots/"+b.ID.String()+"/messages?scope="+string(scope), "application/json", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 202 {
					t.Fatalf("admit %d: %s", resp.StatusCode, raw)
				}
				var source store.BotMessageReceipt
				if err = json.Unmarshal(raw, &source); err != nil {
					t.Fatal(err)
				}
				if busy != nil {
					old, e := repo.GetRun(auth, b.ID, busy.ID)
					if e != nil || old.Status != store.RunInterrupted {
						t.Fatalf("cron not preempted: %+v %v", old, e)
					}
					wc := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: busy.ID, Version: busy.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
					if _, e = repo.RenewLease(wc, busyLease, time.Now().Add(time.Minute)); e == nil {
						t.Fatal("old writer not fenced")
					}
					if e = worker.Poll(ctx); e == nil {
						t.Fatal("unverified teardown accepted")
					}
					queued, e := repo.GetRun(auth, b.ID, source.Run.ID)
					if e != nil || queued.Status != store.RunQueued || !llm.capture {
						t.Fatal("second writer before verified teardown", e)
					}
					if !jobs.List(auth)[0].Enabled {
						t.Fatal("admission disabled scheduling")
					}
					if physical.session != busy.SessionID.String() {
						t.Fatal("wrong session verified")
					}
					physical.pending = false
				}
				if err = worker.Poll(ctx); err != nil {
					t.Fatal(err)
				}
				saved, err := repo.GetRun(auth, b.ID, source.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				rt, err := worker.AuthorizedOperations(repo)
				if err != nil {
					t.Fatal(err)
				}
				ops, err := rt.ListAuthorizedOperations(auth, *saved.MandateRef)
				if err != nil || len(ops) != 1 {
					t.Fatalf("operations %+v %v; run %+v", ops, err, saved)
				}
				if scenario == "deny" || scenario == "memory_deny" || scenario == "approve" || scenario == "expiry" {
					if ops[0].Status != store.BotOperationBlocked {
						t.Fatalf("not blocked: %+v", ops[0])
					}
					q := store.BotRoutineExceptionDecision{MandateRef: *saved.MandateRef, OperationID: ops[0].ID, PayloadDigest: ops[0].Provenance.PayloadDigest, ExpectedRevision: ops[0].Revision, Approve: scenario != "deny" && scenario != "memory_deny", ExpiresAt: time.Now().Add(time.Minute)}
					if scenario == "expiry" {
						q.ExpiresAt = time.Now().Add(-time.Second)
					}
					if scenario != "expiry" && scenario != "memory_deny" {
						stale, _ := json.Marshal(map[string]any{"payload_digest": q.PayloadDigest, "expected_revision": q.ExpectedRevision + 1, "expires_at": q.ExpiresAt, "approve": q.Approve})
						response, e := server.Client().Post(server.URL+"/api/studio/bots/"+b.ID.String()+"/operations/"+ops[0].ID.String()+"/decision?scope="+string(scope), "application/json", strings.NewReader(string(stale)))
						if e != nil {
							t.Fatal(e)
						}
						data, _ := io.ReadAll(response.Body)
						response.Body.Close()
						if response.StatusCode != http.StatusConflict {
							t.Fatalf("wrong revision accepted: %d %s", response.StatusCode, data)
						}
						unchanged, e := rt.ListAuthorizedOperations(auth, *saved.MandateRef)
						if e != nil || len(unchanged) != 1 || unchanged[0].Status != store.BotOperationBlocked || unchanged[0].Revision != q.ExpectedRevision {
							t.Fatalf("wrong revision mutated operation: %+v %v", unchanged, e)
						}
					}
					encoded, _ := json.Marshal(map[string]any{"payload_digest": q.PayloadDigest, "expected_revision": q.ExpectedRevision, "expires_at": q.ExpiresAt, "approve": q.Approve})
					response, e := server.Client().Post(server.URL+"/api/studio/bots/"+b.ID.String()+"/operations/"+ops[0].ID.String()+"/decision?scope="+string(scope), "application/json", strings.NewReader(string(encoded)))
					if e != nil {
						t.Fatal(e)
					}
					data, _ := io.ReadAll(response.Body)
					response.Body.Close()
					if scenario == "expiry" {
						if response.StatusCode != 410 {
							t.Fatalf("expiry %d %s", response.StatusCode, data)
						}
						return
					}
					if response.StatusCode != 200 {
						t.Fatalf("decision %d %s", response.StatusCode, data)
					}
					var decided store.BotOperationView
					if strings.Contains(string(data), "mandate_ref") || strings.Contains(string(data), "provenance") {
						t.Fatalf("authority leaked: %s", data)
					}
					if e = json.Unmarshal(data, &decided); e != nil {
						t.Fatal(e)
					}
					if scenario == "approve" && (decided.Status != store.BotOperationPending || decided.VerifiedSchedule != nil) {
						t.Fatalf("decision applied prematurely: %+v", decided)
					}
					if e = worker.Poll(ctx); e != nil {
						t.Fatal(e)
					}
					if scenario == "deny" || scenario == "memory_deny" {
						if saved.Status != store.RunSucceeded || saved.FinishedAt == nil {
							t.Fatalf("source did not terminate: %+v", saved)
						}
						if decided.Status != store.BotOperationCancelled || decided.Decision != store.BotOperationDeny || decided.Revision != q.ExpectedRevision+1 || decided.Exception != nil {
							t.Fatalf("denial not durable: %+v", decided)
						}
						// A fresh runtime and repeated ticks must neither execute nor emit
						// another operation/receipt for this source.
						for range 3 {
							restarted, e := worker.AuthorizedOperations(repo)
							if e != nil {
								t.Fatal(e)
							}
							if e = restarted.(interface {
								ReconcileAuthorizedOperations(context.Context, store.BotID, int) error
							}).ReconcileAuthorizedOperations(auth, b.ID, 32); e != nil {
								t.Fatal(e)
							}
							if e = worker.Poll(ctx); e != nil {
								t.Fatal(e)
							}
							sourceAfter, e := repo.GetRun(auth, b.ID, source.Run.ID)
							if e != nil || sourceAfter.Status != store.RunSucceeded || sourceAfter.FinishedAt == nil {
								t.Fatalf("denied source reopened after tick: %+v %v", sourceAfter, e)
							}
							current, e := restarted.ListAuthorizedOperations(auth, *saved.MandateRef)
							if e != nil || len(current) != 1 || current[0].Revision != decided.Revision || current[0].Decision != store.BotOperationDeny || current[0].Status != store.BotOperationCancelled || current[0].Receipt == nil || current[0].Receipt.Status != store.BotOperationCancelled || current[0].Receipt.ObservedAt.IsZero() || current[0].Receipt.Reason != "authorization_denied" {
								t.Fatalf("denial changed after tick: %+v %v", current, e)
							}
						}
						if memoryBefore != nil {
							after, e := memories.Get(auth, memoryBefore.ID)
							if e != nil || after == nil || store.MemoryContentFingerprint(*after) != store.MemoryContentFingerprint(*memoryBefore) {
								t.Fatalf("denied memory changed: %+v %v", after, e)
							}
						}
						goals, e := repo.(store.BotGoalStore).ListGoals(auth, b.ID)
						if e != nil || len(goals) != 0 || decided.Decision != store.BotOperationDeny {
							t.Fatalf("deny %+v %v", goals, e)
						}
						return
					}
					ops, err = rt.ListAuthorizedOperations(auth, *saved.MandateRef)
					if err != nil {
						t.Fatal(err)
					}
				}
				if ops[0].Status != store.BotOperationApplied || ops[0].Receipt == nil {
					t.Fatalf("not applied %+v receipt=%+v run=%+v", ops[0], ops[0].Receipt, saved)
				}
				if scenario == "priority_pause" {
					paused, e := repo.Get(auth, b.ID)
					for _, job := range jobs.List(auth) {
						if job.Enabled {
							t.Fatal("pause left schedule enabled")
						}
					}
					if e != nil || paused.Status != store.BotPaused || saved.Status != store.RunInterrupted {
						t.Fatalf("pause not fenced: %+v %+v %v", paused, saved, e)
					}
					if e = worker.Poll(ctx); e != nil {
						t.Fatal(e)
					}
					return
				}
				if scenario == "task_once" {
					taskID := uuid.NewSHA1(ops[0].ID, []byte("run"))
					task, e := repo.GetRun(auth, b.ID, taskID)
					if e != nil || task.MandateRef == nil || *task.MandateRef != *saved.MandateRef {
						t.Fatalf("unbound task %+v %v", task, e)
					}
					if e = worker.Poll(ctx); e != nil {
						t.Fatal(e)
					}
					task, e = repo.GetRun(auth, b.ID, taskID)
					if e != nil || task.Status != store.RunSucceeded {
						t.Fatalf("task outcome %+v %v", task, e)
					}
					return
				}
				if scenario == "paused_goal" {
					paused, e := repo.Get(auth, b.ID)
					for _, job := range jobs.List(auth) {
						if job.Enabled {
							t.Fatal("pause left schedule enabled")
						}
					}
					if e != nil || paused.Status != store.BotPaused {
						t.Fatalf("owner control activated bot: %+v %v", paused, e)
					}
					mandates, e := entstore.NewBotMandateRuntime(repo, policy)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = mandates.VerifyBotMandate(auth, *saved.MandateRef, execution.BotMandatePurposeTask); e == nil {
						t.Fatal("paused control became task authority")
					}
				}
				goals, err := repo.(store.BotGoalStore).ListGoals(auth, b.ID)
				if err != nil || len(goals) != 1 || goals[0].Title != "Ship report" {
					t.Fatalf("goals %+v %v", goals, err)
				}
				approvals, err := repo.ListApprovals(auth, b.ID, store.BotPage{})
				if err != nil || len(approvals) != 0 {
					t.Fatalf("unexpected approvals %+v %v", approvals, err)
				}
				if err = worker.Poll(ctx); err != nil {
					t.Fatal(err)
				}
				goals, err = repo.(store.BotGoalStore).ListGoals(auth, b.ID)
				if err != nil || len(goals) != 1 {
					t.Fatalf("replay %+v %v", goals, err)
				}
			})
		}
	}
}
