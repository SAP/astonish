package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

// Exercise the production scheduler lane and owner resolver, not an HTTP-only
// admission or a resolver fixture that supplies its own authenticated context.
func TestBotSchedulerSQLiteTrustedTeamAdmission(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			backend, owner := setupChannelResolverFixture(t, "schedule", "exact-team", "telegram", "linked")
			ctx := context.Background()
			w := &BotWorker{config: BotWorkerConfig{Backend: backend, WorkerID: "scheduler-test"}, runtime: admissionOnlyRuntime{}}
			auth, org, err := w.authenticateOwner(ctx, BotSchedulerScope{"schedule", "exact-team", owner})
			if err != nil {
				t.Fatal(err)
			}
			repo, jobs := org.ForTeam("exact-team").Bots(), org.ForTeam("exact-team").ScheduledJobs()
			if scope == store.BotScopePersonal {
				repo, jobs = org.ForUser(owner).Bots(), org.ForUser(owner).ScheduledJobs()
			}
			v := store.DefaultBotVersion()
			v.Charter = "Observe only"
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Scheduled", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "schedule"})
			if err != nil {
				t.Fatal(err)
			}
			backend.(store.BotActivationInstaller).SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			registry, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: "schedule", TeamSlug: "exact-team", Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "home", Revision: 1}
			if err := entstore.RegisterBotHomeBinding(registry, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
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
			// Ambient request routing is not trusted evidence for the schedule.
			auth = store.WithTeamSlug(auth, "ambient-wrong-team")
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
			list := jobs.List(ctx)
			if len(list) != 1 {
				t.Fatalf("jobs=%d", len(list))
			}
			job := list[0]
			if scope == store.BotScopePersonal {
				if job.TeamSlug != "exact-team" {
					t.Fatalf("new schedule lost authenticated team: %q", job.TeamSlug)
				}
				// An old row is repaired only by an explicit owner-authorized
				// trigger mutation, not by guessing a team during a daemon tick.
				job.TeamSlug = ""
				if err := jobs.Update(ctx, job); err != nil {
					t.Fatal(err)
				}
				b, err = triggers.ChangeTrigger(auth, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerDisable, Reason: "repair legacy routing"})
				if err != nil {
					t.Fatal(err)
				}
				b, err = triggers.ChangeTrigger(auth, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "reviewed"})
				if err != nil {
					t.Fatal(err)
				}
				job = jobs.Get(ctx, job.ID)
				if job.TeamSlug != "exact-team" {
					t.Fatalf("legacy schedule not repaired: %q", job.TeamSlug)
				}
			}
			// Make the persisted occurrence due without replacing any routing fields.
			due := time.Now().Add(-time.Second)
			job.NextRun = &due
			if err := jobs.Update(ctx, job); err != nil {
				t.Fatal(err)
			}
			job = jobs.Get(ctx, job.ID)
			executor := &scheduler.Executor{}
			mts := NewMultiTenantScheduler(backend, executor, nil, nil)
			mts.SetBotSchedulerCallbacks(w.SchedulerCallbacks())
			admission := executor.BotTrigger
			executor.BotTrigger = func(c context.Context, q scheduler.BotTriggerRequest) (scheduler.BotAdmission, error) {
				receipt, err := admission(c, q)
				if err != nil {
					return receipt, err
				}
				// An edit after transaction commit must survive outer scheduler bookkeeping.
				edited := jobs.Get(ctx, job.ID)
				edited.Enabled = false
				edited.Payload.BotVersion++
				future := time.Now().Add(24 * time.Hour).UTC()
				edited.NextRun = &future
				if err := jobs.Update(ctx, edited); err != nil {
					t.Fatal(err)
				}
				return receipt, nil
			}
			if scope == store.BotScopePersonal {
				mts.executePersonalJob(ctx, job, "schedule", org, org.ForUser(owner), jobs, owner)
			} else {
				mts.executeTeamJob(ctx, job, "schedule", "exact-team", org, org.ForTeam("exact-team"), jobs)
			}
			saved := jobs.Get(ctx, job.ID)
			if saved.Enabled || saved.Payload.BotVersion != job.Payload.BotVersion+1 || saved.NextRun == nil || !saved.NextRun.After(time.Now().Add(23*time.Hour)) {
				t.Fatal("outer acknowledgement overwrote concurrent edit")
			}
			runs, err := repo.ListRuns(auth, b.ID, store.BotPage{})
			if err != nil || len(runs) != 1 || saved.ConsecutiveFailures != 0 {
				t.Fatalf("production scheduler admission: runs=%d error=%v job error=%q team=%q", len(runs), err, saved.LastError, job.TeamSlug)
			}
			if scope == store.BotScopePersonal && job.TeamSlug != "exact-team" {
				t.Fatalf("lost trusted team: %+v", job)
			}
			req := scheduler.BotTriggerRequest{BotID: b.ID}
			// Neither an ambient authenticated viewer nor another lane can repair an
			// absent/wrong trusted route. Missing scope must continue to fail closed.
			for _, route := range []BotSchedulerScope{
				{"schedule", "", owner}, {"schedule", "not-a-member", owner},
				{"schedule", "exact-team", uuid.NewString()},
			} {
				if _, _, err := w.SchedulerCallbacks().Resolve(withBotSchedulerScope(auth, route), req); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("invalid route authorized: %+v %v", route, err)
				}
			}
			if scope == store.BotScopePersonal {
				if _, _, err := w.SchedulerCallbacks().Resolve(withBotSchedulerScope(auth, BotSchedulerScope{"schedule", "exact-team", ""}), req); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("personal Bot crossed team lane: %v", err)
				}
			}
			team, err := org.Teams().GetTeamBySlug(ctx, "exact-team")
			if err != nil {
				t.Fatal(err)
			}
			if err := org.Teams().RemoveMember(ctx, owner, team.ID); err != nil {
				t.Fatal(err)
			}
			route := BotSchedulerScope{OrgSlug: "schedule", TeamSlug: "exact-team"}
			if scope == store.BotScopePersonal {
				route.UserID = owner
			}
			if _, _, err := w.SchedulerCallbacks().Resolve(withBotSchedulerScope(auth, route), req); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked team membership authorized: %v", err)
			}
		})
	}
}
