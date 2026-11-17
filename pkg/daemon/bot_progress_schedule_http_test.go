package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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

// Real HTTP read surface + production scheduler callback over routed SQLite
// scopes. Scheduling is intentionally not an HTTP permission-grant endpoint.
func TestBotProgressScheduledAdmissionHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			backend, owner := setupChannelResolverFixture(t, "progress", "exact-team", "telegram", "linked")
			ctx := t.Context()
			worker := &BotWorker{config: BotWorkerConfig{Backend: backend, WorkerID: "progress-scheduler"}, runtime: admissionOnlyRuntime{}}
			auth, org, err := worker.authenticateOwner(ctx, BotSchedulerScope{"progress", "exact-team", owner})
			if err != nil {
				t.Fatal(err)
			}
			repo, jobs := org.ForTeam("exact-team").Bots(), org.ForTeam("exact-team").ScheduledJobs()
			route := BotSchedulerScope{"progress", "exact-team", ""}
			if scope == store.BotScopePersonal {
				repo, jobs = org.ForUser(owner).Bots(), org.ForUser(owner).ScheduledJobs()
				route.UserID = owner
			}
			v := store.DefaultBotVersion()
			v.Charter = "Long observation"
			v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			one := int64(1)
			v.Budget.MaxRunsPerDay = &one
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Progress", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "progress"})
			if err != nil {
				t.Fatal(err)
			}
			backend.(store.BotActivationInstaller).SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			registry, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: "progress", TeamSlug: "exact-team", Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			if err = entstore.RegisterBotHomeBinding(registry, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
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
			svc := &store.Services{Bots: repo}
			if scope == store.BotScopePersonal {
				svc.PersonalBots = repo
			}
			httpCtx := store.WithServices(store.WithTenantContext(auth, &store.TenantContext{UserID: owner, OrgSlug: "progress", TeamSlug: "exact-team"}), svc)
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/runs", api.BotRunsHandler)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
			defer server.Close()
			readRuns := func() []store.BotRun {
				t.Helper()
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/bots/"+b.ID.String()+"/runs?scope="+string(scope), nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != 200 {
					t.Fatalf("HTTP %d: %s", resp.StatusCode, raw)
				}
				var value struct {
					Runs []store.BotRun `json:"runs"`
				}
				if err = json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				return value.Runs
			}
			if len(readRuns()) != 0 {
				t.Fatal("unexpected initial assignment")
			}
			admit := worker.SchedulerCallbacks().Admission()
			job := jobs.List(ctx)[0]
			var first uuid.UUID
			var lease store.BotLease
			var runWorker context.Context
			var last scheduler.BotTriggerRequest
			for i := 0; i < 30; i++ {
				due := time.Now().Add(-time.Duration(100-i) * time.Second).UTC()
				job = jobs.Get(ctx, job.ID)
				job.NextRun = &due
				if err = jobs.Update(ctx, job); err != nil {
					t.Fatal(err)
				}
				identity := fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, tid, due.Format(time.RFC3339Nano))
				last = scheduler.BotTriggerRequest{JobID: job.ID, BotID: b.ID, Version: b.Version, TriggerID: tid, OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))}
				receipt, e := admit(withBotSchedulerScope(auth, route), last)
				if e != nil {
					t.Fatal(e)
				}
				if i == 0 {
					first = receipt.RunID
					if receipt.Status != scheduler.BotAdmissionAdmitted {
						t.Fatal(receipt)
					}
				} else if receipt.RunID != first || receipt.Status != scheduler.BotAdmissionCoalesced {
					t.Fatalf("tick %d: %+v", i, receipt)
				}
				if receipt.Acknowledgement != scheduler.BotScheduleAcknowledged {
					t.Fatal("occurrence not atomically acknowledged")
				}
				runs := readRuns()
				if len(runs) != 1 || runs[0].ID != first {
					t.Fatalf("backlog at tick %d: %+v", i, runs)
				}
				if i == 10 {
					runWorker = execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: first, Version: b.Version, ActorID: owner, OrgSlug: "progress", TeamSlug: "exact-team"})
					lease, err = repo.ClaimRun(runWorker, store.BotClaimRequest{BotID: b.ID, RunID: first, WorkerID: "progress", ExpiresAt: time.Now().Add(time.Minute)})
					if err != nil {
						t.Fatal(err)
					}
				}
				if i == 20 {
					work, e := repo.(store.BotWorkStore).LoadWork(runWorker, lease)
					if e != nil {
						t.Fatal(e)
					}
					binding := store.BotProgressBinding{BotID: b.ID, RunID: first, SessionID: runs[0].SessionID, Version: b.Version, Task: runs[0].Task, WorkRevision: work.Revision}
					_, e = repo.(store.BotProgressStore).CheckpointProgress(runWorker, store.BotProgressCheckpointRequest{Mutation: store.BotProgressMutation{Lease: lease, Binding: binding}, State: store.BotProgressState{SchemaVersion: 1, Phase: store.BotProgressSuspended, Checkpoint: &store.BotProgressCheckpoint{Kind: store.BotProgressCheckpointV1, SchemaVersion: 1, Binding: binding, Revision: 1, TurnID: uuid.New(), ContextRef: "private:context", ContextDigest: fmt.Sprintf("%x", sha256.Sum256([]byte("context"))), NextEligibleAt: time.Now().Add(time.Minute)}}})
					if e != nil {
						t.Fatal(e)
					}
				}
				if i > 20 {
					if runs[0].Progress == nil || runs[0].Progress.CoalescedOccurrences != int64(i) {
						t.Fatalf("HTTP coalesced count at tick %d: %+v", i, runs[0].Progress)
					}
					snapshot, e := repo.(store.BotSnapshotReader).Snapshot(auth, b.ID)
					if e != nil {
						t.Fatal(e)
					}
					found := false
					for _, state := range snapshot.RunStates {
						if state.RunID == first {
							found = reflect.DeepEqual(state.Progress, runs[0].Progress) && state.StateSequence == runs[0].StateSequence
						}
					}
					if !found {
						t.Fatal("snapshot differs from canonical coalesced count")
					}
				}
				persisted := jobs.Get(ctx, job.ID)
				if persisted.NextRun == nil || !persisted.NextRun.After(time.Now()) || persisted.ConsecutiveFailures != 0 {
					t.Fatal("schedule not advanced", persisted)
				}
			}
			// Duplicate receipt survives pause, but a NEW occurrence may not turn an
			// authority/lifecycle denial into successful coalescing.
			b, err = repo.Pause(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "pause"})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := admit(withBotSchedulerScope(auth, route), last)
			if err != nil || receipt.RunID != first || receipt.Status != scheduler.BotAdmissionDuplicate {
				t.Fatal("lost occurrence replay after pause", receipt, err)
			}
			last.OccurrenceAt = last.OccurrenceAt.Add(time.Second)
			identity := fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, tid, last.OccurrenceAt.Format(time.RFC3339Nano))
			last.IdempotencyKey = fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))
			if _, err = admit(withBotSchedulerScope(auth, route), last); err == nil {
				t.Fatal("paused bot coalesced new occurrence")
			}
			if len(readRuns()) != 1 {
				t.Fatal("HTTP shows backlog")
			}
			t.Log("30 scheduler ticks, one logical assignment, daily cap one, HTTP scoped reads and pause/replay verified")
		})
	}
}
