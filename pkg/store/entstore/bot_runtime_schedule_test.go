package entstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func scheduledFixture(t *testing.T, scope store.BotScope, kind store.TriggerKind, cap int64) (*botRepository, context.Context, store.SchedulerStore, store.BotScheduledOccurrence) {
	t.Helper()
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}
	var db botDraftDB
	var jobs store.SchedulerStore
	var activate func(uuid.UUID) error
	if scope == store.BotScopeTeam {
		c := newTeamClient(t)
		db = teamBotDB{client: c}
		jobs = &teamSchedulerStore{client: c}
		activate = func(id uuid.UUID) error { return c.Bot.UpdateOneID(id).SetStatus("active").Exec(ctx) }
	} else {
		_, c := setupPersonalStore(t)
		db = personalBotDB{client: c}
		jobs = &personalSchedulerStore{client: c}
		activate = func(id uuid.UUID) error { return c.Bot.UpdateOneID(id).SetStatus("active").Exec(ctx) }
	}
	repo := &botRepository{auth: auth, db: db}
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	jobID, triggerID, goalID := uuid.New(), uuid.New(), uuid.New()
	v := store.DefaultBotVersion()
	v.Charter = "Observe scheduler"
	v.Budget.MaxRunsPerDay = &cap
	trigger := store.TriggerSpec{ID: triggerID, Kind: kind, ScheduleID: &jobID, Instructions: "Saved scheduled instructions", Cron: "* * * * *", Timezone: "UTC", Enabled: true}
	var goals []store.BotGoal
	if kind == store.TriggerGoalDeadline {
		trigger.GoalID = &goalID
		goals = []store.BotGoal{{ID: goalID, Title: "Due goal", Status: store.BotGoalActive, DueAt: &due}}
	}
	v.Triggers = nil
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Scheduled", OwnerID: owner, Version: v, IdempotencyKey: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	// Controlled fixture only: production activation remains runtime-attested.
	v.BotID = b.ID
	v.Version = 2
	v.CreatedBy = owner
	v.CreatedAt = time.Now().UTC()
	v.Triggers = []store.TriggerSpec{trigger}
	if err := repo.db.InsertVersion(ctx, v, "fixture-v2"); err != nil {
		t.Fatal(err)
	}
	rec, err := repo.db.GetBot(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.db.UpdateVersion(ctx, b.ID, b.Revision, 2, rec.LastSequence+1, time.Now()); err != nil {
		t.Fatal(err)
	}
	b.Version = 2
	for _, g := range goals {
		g.BotID = b.ID
		g.Revision = 1
		if err := repo.db.InsertGoal(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	if err := activate(b.ID); err != nil {
		t.Fatal(err)
	}
	job := &store.ScheduledJob{ID: jobID.String(), Name: "Scheduled", Mode: string(scheduler.ModeBotTrigger), OwnerID: owner.String(), Enabled: true, NextRun: &due, Schedule: store.JobSchedule{Cron: trigger.Cron, Timezone: trigger.Timezone}, Payload: store.JobPayload{BotID: b.ID.String(), BotVersion: b.Version, BotTriggerID: triggerID.String()}}
	if err := jobs.Add(ctx, job); err != nil {
		t.Fatal(err)
	}
	identity := fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, triggerID, due.Format(time.RFC3339Nano))
	return repo, ctx, jobs, store.BotScheduledOccurrence{JobID: jobID.String(), BotID: b.ID, Version: b.Version, TriggerID: triggerID, OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))}
}

func TestBotScheduledAdmission(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []store.TriggerKind{store.TriggerCron, store.TriggerGoalDeadline} {
			t.Run(string(scope)+"/"+string(kind), func(t *testing.T) {
				repo, ctx, jobs, q := scheduledFixture(t, scope, kind, 1)
				if _, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: q.BotID, Version: q.Version, Trigger: kind, Instructions: "forged", IdempotencyKey: "forged"}); !errors.Is(err, store.ErrBotUnsupported) {
					t.Fatalf("public trigger accepted: %v", err)
				}
				receipt, err := repo.AdmitScheduledRun(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				if receipt.Duplicate || receipt.Run.Status != store.RunQueued || receipt.Run.Trigger != kind {
					t.Fatalf("bad admission: %+v", receipt)
				}
				input, err := repo.LoadRunInput(ctx, q.BotID, receipt.Run.ID)
				if err != nil || input.Instructions != "Saved scheduled instructions" {
					t.Fatalf("input: %+v %v", input, err)
				}
				// Scheduler acknowledgment may already have advanced NextRun before replay.
				job := jobs.Get(ctx, q.JobID)
				next := q.OccurrenceAt.Add(time.Hour)
				job.NextRun = &next
				if err := jobs.Update(ctx, job); err != nil {
					t.Fatal(err)
				}
				replay, err := repo.AdmitScheduledRun(ctx, q)
				if err != nil || !replay.Duplicate || replay.Run.ID != receipt.Run.ID {
					t.Fatalf("replay: %+v %v", replay, err)
				}
				rows, err := repo.ListRuns(ctx, q.BotID, store.BotPage{})
				if err != nil || len(rows) != 1 {
					t.Fatalf("runs: %+v %v", rows, err)
				}
				activity, err := repo.ListActivity(ctx, q.BotID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				dispatch := 0
				for _, a := range activity {
					if a.RunID != nil {
						dispatch++
					}
				}
				if dispatch != 1 {
					t.Fatalf("dispatch intents %d", dispatch)
				}
			})
		}
	}
}

func TestBotScheduledAdmissionRejectsUntrustedBindings(t *testing.T) {
	for _, mode := range []string{"viewer", "removed", "wrong-owner", "wrong-tenant", "forged-key", "future", "disabled", "wrong-binding", "payload", "stale-version", "budget", "paused"} {
		t.Run(mode, func(t *testing.T) {
			cap := int64(2)
			if mode == "budget" {
				cap = 0
			}
			repo, ctx, jobs, q := scheduledFixture(t, store.BotScopeTeam, store.TriggerCron, cap)
			switch mode {
			case "viewer":
				repo.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
			case "removed":
				repo.auth.check = func(context.Context, string) (string, error) { return "", nil }
			case "wrong-owner", "wrong-tenant":
				p, _ := execution.PrincipalFromContext(ctx)
				if mode == "wrong-owner" {
					p.Subject = uuid.New().String()
				} else {
					p.OrgSlug = "other"
				}
				ctx, _ = execution.WithPrincipal(ctx, p)
			case "forged-key":
				q.IdempotencyKey = "forged"
			case "future":
				q.OccurrenceAt = time.Now().Add(time.Hour)
			case "stale-version":
				q.Version++
			case "paused":
				if err := repo.db.(teamBotDB).client.Bot.UpdateOneID(q.BotID).SetStatus("paused").Exec(ctx); err != nil {
					t.Fatal(err)
				}
			case "disabled", "wrong-binding", "payload":
				job := jobs.Get(ctx, q.JobID)
				if mode == "disabled" {
					job.Enabled = false
				}
				if mode == "wrong-binding" {
					job.Payload.BotTriggerID = uuid.New().String()
				}
				if mode == "payload" {
					job.Payload.Instructions = "untrusted"
				}
				if err := jobs.Update(ctx, job); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := repo.AdmitScheduledRun(ctx, q); err == nil {
				t.Fatal("untrusted admission accepted")
			}
			if n, err := repo.db.(teamBotDB).client.BotRun.Query().Count(context.Background()); err != nil || n != 0 {
				t.Fatalf("run written: %d %v", n, err)
			}
		})
	}
}

// This exercises the real scheduler's 30-second tick and the real scoped SQLite
// transaction. It does not install a Runtime or manufacture execution success.
func TestBotScheduledTickSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("real scheduler tick takes 30 seconds")
	}
	repo, ctx, jobs, q := scheduledFixture(t, store.BotScopePersonal, store.TriggerCron, 2)
	due := q.OccurrenceAt
	js := &scheduledTickStore{jobs: jobs}
	admitted := make(chan scheduler.BotAdmission, 2)
	executor := &scheduler.Executor{BotTrigger: func(c context.Context, r scheduler.BotTriggerRequest) (scheduler.BotAdmission, error) {
		out, err := repo.AdmitScheduledRun(ctx, store.BotScheduledOccurrence{JobID: r.JobID, BotID: r.BotID, Version: r.Version, TriggerID: r.TriggerID, OccurrenceAt: r.OccurrenceAt, IdempotencyKey: r.IdempotencyKey})
		if err != nil {
			return scheduler.BotAdmission{}, err
		}
		receipt := scheduler.BotAdmission{Status: scheduler.BotAdmissionAdmitted, RunID: out.Run.ID}
		admitted <- receipt
		return receipt, nil
	}}
	engine := scheduler.New(js, executor.Execute, scheduler.WithoutBotTriggerDelivery(func(context.Context, *scheduler.Job, string, error) error {
		t.Error("legacy delivery invoked")
		return nil
	}), nil)
	engine.Start(ctx)
	defer engine.Stop()
	select {
	case receipt := <-admitted:
		engine.Stop() // wait for scheduler acknowledgment before replaying
		run, err := repo.GetRun(ctx, q.BotID, receipt.RunID)
		if err != nil || run.Status != store.RunQueued || run.FinishedAt != nil {
			t.Fatalf("not queued: %+v %v", run, err)
		}
		replay, err := repo.AdmitScheduledRun(ctx, q)
		if err != nil || !replay.Duplicate || replay.Run.ID != run.ID {
			t.Fatalf("replay: %+v %v", replay, err)
		}
	case <-time.After(35 * time.Second):
		t.Fatalf("no durable tick admission for %v", due)
	}
}

type scheduledTickStore struct{ jobs store.SchedulerStore }

func (s *scheduledTickStore) List() []*scheduler.Job {
	var out []*scheduler.Job
	for _, j := range s.jobs.List(context.Background()) {
		out = append(out, tickJob(j))
	}
	return out
}
func (s *scheduledTickStore) Get(id string) *scheduler.Job {
	return tickJob(s.jobs.Get(context.Background(), id))
}
func (s *scheduledTickStore) GetByName(name string) *scheduler.Job {
	return tickJob(s.jobs.GetByName(context.Background(), name))
}
func (s *scheduledTickStore) Add(*scheduler.Job) error { return store.ErrBotUnsupported }
func (s *scheduledTickStore) Remove(id string) error   { return s.jobs.Remove(context.Background(), id) }
func (s *scheduledTickStore) Update(j *scheduler.Job) error {
	v := s.jobs.Get(context.Background(), j.ID)
	v.NextRun = j.NextRun
	v.LastRun = j.LastRun
	v.LastStatus = string(j.LastStatus)
	return s.jobs.Update(context.Background(), v)
}
func tickJob(j *store.ScheduledJob) *scheduler.Job {
	if j == nil {
		return nil
	}
	return &scheduler.Job{ID: j.ID, Name: j.Name, Mode: scheduler.JobMode(j.Mode), Enabled: j.Enabled, OwnerID: j.OwnerID, NextRun: j.NextRun, Schedule: scheduler.JobSchedule{Cron: j.Schedule.Cron, Timezone: j.Schedule.Timezone}, Payload: scheduler.JobPayload{BotID: j.Payload.BotID, BotVersion: j.Payload.BotVersion, BotTriggerID: j.Payload.BotTriggerID}}
}
