package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Production worker recovery/poll, cleanup transaction, scheduler callback and
// HTTP reads over routed SQLite. Physical teardown is a controlled attestation,
// not proof that Docker removed a container or an OS daemon was restarted.
func TestBotRecoveredScheduleCleanupHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"pending-model", "settled-tool", "pending-unknown-write", "unconfirmed", "foreign-teardown"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				backend, owner := setupChannelResolverFixture(t, "recovery", "exact-team", "telegram", "linked")
				ctx := t.Context()
				physical := &cancellationReceiptBackend{err: errors.New("exact old session still exists")}
				worker := &BotWorker{config: BotWorkerConfig{Backend: backend, WorkerID: "replacement", Sandbox: physical}, runtime: admissionOnlyRuntime{}}
				auth, org, err := worker.authenticateOwner(ctx, BotSchedulerScope{"recovery", "exact-team", owner})
				if err != nil {
					t.Fatal(err)
				}
				repo, jobs := org.ForTeam("exact-team").Bots(), org.ForTeam("exact-team").ScheduledJobs()
				route := BotSchedulerScope{"recovery", "exact-team", ""}
				if scope == store.BotScopePersonal {
					repo, jobs = org.ForUser(owner).Bots(), org.ForUser(owner).ScheduledJobs()
					route.UserID = owner
				}
				v := store.DefaultBotVersion()
				v.Charter = "Observe without replay"
				v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
				b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Recovered", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "recovery"})
				if err != nil {
					t.Fatal(err)
				}
				backend.(store.BotActivationInstaller).SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
				registry, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: "recovery", TeamSlug: "exact-team", Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
				if err != nil {
					t.Fatal(err)
				}
				if err := entstore.RegisterBotHomeBinding(registry, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
					t.Fatal(err)
				}
				b, err = repo.Get(auth, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "reviewed"})
				if err != nil {
					t.Fatal(err)
				}
				tid := uuid.New()
				triggers := repo.(store.BotTriggerStore)
				b, err = triggers.ChangeTrigger(auth, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "reviewed", Definition: store.TriggerSpec{Kind: store.TriggerCron, Instructions: "Observe", Cron: "* * * * *", Timezone: "UTC"}})
				if err != nil {
					t.Fatal(err)
				}
				b, err = triggers.ChangeTrigger(auth, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "reviewed"})
				if err != nil {
					t.Fatal(err)
				}
				job := jobs.List(ctx)[0]
				admit := worker.SchedulerCallbacks().Admission()
				tick := func() scheduler.BotAdmission {
					t.Helper()
					due := time.Now().Add(-time.Millisecond).UTC()
					job = jobs.Get(ctx, job.ID)
					job.NextRun = &due
					if err := jobs.Update(ctx, job); err != nil {
						t.Fatal(err)
					}
					identity := fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, tid, due.Format(time.RFC3339Nano))
					got, err := admit(withBotSchedulerScope(auth, route), scheduler.BotTriggerRequest{JobID: job.ID, BotID: b.ID, Version: b.Version, TriggerID: tid, OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))})
					if err != nil {
						t.Fatal(err)
					}
					return got
				}
				first := tick()
				if first.Status != scheduler.BotAdmissionAdmitted {
					t.Fatal(first)
				}
				run, err := repo.GetRun(auth, b.ID, first.RunID)
				if err != nil {
					t.Fatal(err)
				}
				runCtx := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: owner, OrgSlug: "recovery", TeamSlug: "exact-team"})
				lease, err := repo.ClaimRun(runCtx, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "old-worker", ExpiresAt: time.Now().Add(250 * time.Millisecond)})
				if err != nil {
					t.Fatal(err)
				}
				work, err := repo.(store.BotWorkStore).LoadWork(runCtx, lease)
				if err != nil {
					t.Fatal(err)
				}
				progress := repo.(store.BotProgressStore)
				mutation := store.BotProgressMutation{Lease: lease, Binding: store.BotProgressBinding{BotID: b.ID, RunID: run.ID, SessionID: run.SessionID, Version: b.Version, WorkRevision: work.Revision}}
				kind := store.BotProgressDispatchModel
				if scenario == "settled-tool" || scenario == "pending-unknown-write" {
					kind = store.BotProgressDispatchTool
				}
				begin := store.BotProgressBeginDispatchRequest{Mutation: mutation, DispatchID: uuid.New(), Kind: kind, OperationDigest: strings.Repeat("a", 64), ReservedUsage: store.BotUsage{ToolCalls: 1}}
				if _, err := progress.BeginProgressDispatch(runCtx, begin); err != nil {
					t.Fatal(err)
				}
				if scenario == "settled-tool" {
					if _, err := progress.SettleProgressDispatch(runCtx, store.BotProgressSettleDispatchRequest{Mutation: mutation, DispatchID: begin.DispatchID, Outcome: store.BotProgressDispatchSettled, EvidenceRef: "exact-call-receipt", Usage: begin.ReservedUsage}); err != nil {
						t.Fatal(err)
					}
				}
				// Wait only for a fixture lease; no live DB edits or daemon restarts.
				time.Sleep(time.Until(lease.ExpiresAt) + 10*time.Millisecond)
				if err := worker.pollStore(auth, repo, nil); err != nil {
					t.Fatal(err)
				}
				interrupted, err := repo.GetRun(auth, b.ID, run.ID)
				if err != nil || interrupted.Status != store.RunInterrupted {
					t.Fatalf("recovery: %+v %v", interrupted, err)
				}
				if err := worker.pollStore(auth, repo, nil); err == nil {
					t.Fatal("unconfirmed physical teardown accepted")
				}
				if physical.session != run.SessionID.String() {
					t.Fatal("verifier inspected wrong session")
				}
				if held := tick(); held.Status != scheduler.BotAdmissionCoalesced || held.RunID != run.ID {
					t.Fatalf("missing teardown admitted: %+v", held)
				}
				// A receipt for a foreign run, even on this same Home, cannot release it.
				if scenario == "foreign-teardown" {
					internal, e := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, ClientID: "replacement", OrgSlug: "recovery", TeamSlug: "exact-team", Authenticated: true, Scopes: []string{store.BotCleanupScope}})
					if e != nil {
						t.Fatal(e)
					}
					called := false
					internal = store.WithBotCleanupVerifier(internal, func(context.Context, store.BotCleanupRequest) error { called = true; return nil })
					e = repo.(store.BotCleanupStore).ConfirmCleanup(internal, store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: b.ID, RunID: uuid.New(), RunFence: lease.Fence, Fence: lease.Fence + 1, ExpectedRevision: b.Revision, Home: b.Home})
					if e == nil || called {
						t.Fatal("foreign teardown reached verification", e)
					}
				} else if scenario != "unconfirmed" {
					physical.err = nil
					// A newly composed worker discovers the interrupted record; it does not
					// execute or adopt the old run, and replays the durable receipt on retry.
					worker = &BotWorker{config: BotWorkerConfig{Backend: backend, WorkerID: "replacement-2", Sandbox: physical}, runtime: admissionOnlyRuntime{}}
					if err := worker.pollStore(auth, repo, nil); err != nil {
						t.Fatal(err)
					}
					if err := worker.pollStore(auth, repo, nil); err != nil {
						t.Fatal(err)
					}
				}
				time.Sleep(3 * time.Millisecond) // occurrence must follow the cleanup receipt
				next := tick()
				safe := scenario == "pending-model" || scenario == "settled-tool"
				if safe && (next.Status != scheduler.BotAdmissionAdmitted || next.RunID == run.ID) {
					t.Fatalf("new occurrence held: %+v", next)
				}
				if !safe && (next.Status != scheduler.BotAdmissionCoalesced || next.RunID != run.ID) {
					t.Fatalf("unsafe occurrence admitted: %+v", next)
				}
				after, err := repo.GetRun(auth, b.ID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if after.Status != store.RunInterrupted || after.SessionID != run.SessionID || !reflect.DeepEqual(after.Report, interrupted.Report) || after.Usage != interrupted.Usage {
					t.Fatal("old interruption or billing rewritten")
				}
				if _, err := repo.RenewLease(runCtx, lease, time.Now().Add(time.Minute)); err == nil {
					t.Fatal("old worker revived")
				}
				svc := &store.Services{Bots: repo}
				if scope == store.BotScopePersonal {
					svc.PersonalBots = repo
				}
				httpCtx := store.WithServices(store.WithTenantContext(auth, &store.TenantContext{UserID: owner, OrgSlug: "recovery", TeamSlug: "exact-team"}), svc)
				router := mux.NewRouter()
				router.HandleFunc("/bots/{botID}/runs", api.BotRunsHandler)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
				defer server.Close()
				resp, err := server.Client().Get(server.URL + "/bots/" + b.ID.String() + "/runs?scope=" + string(scope))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				var result struct {
					Runs []store.BotRun `json:"runs"`
				}
				if resp.StatusCode != http.StatusOK {
					t.Fatal(resp.StatusCode)
				}
				if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
					t.Fatal(err)
				}
				want := 1
				if safe {
					want = 2
				}
				if len(result.Runs) != want {
					t.Fatalf("HTTP runs=%d want=%d", len(result.Runs), want)
				}
				if safe {
					for _, current := range result.Runs {
						if current.ID == next.RunID && (current.SessionID == run.SessionID || current.Status != store.RunQueued) {
							t.Fatal("fresh run reused old execution")
						}
					}
				}
			})
		}
	}
}
