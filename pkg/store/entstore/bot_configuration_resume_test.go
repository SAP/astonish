package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestReviewedConfigurationRejectsStaleScheduleBinding(t *testing.T) {
	mutations := []struct {
		name  string
		apply func(*store.ScheduledJob)
	}{
		{"missing", func(j *store.ScheduledJob) {}},
		{"mode", func(j *store.ScheduledJob) { j.Mode = "routine" }},
		{"version", func(j *store.ScheduledJob) { j.Payload.BotVersion++ }},
		{"owner", func(j *store.ScheduledJob) { j.OwnerID = uuid.NewString() }},
		{"bot", func(j *store.ScheduledJob) { j.Payload.BotID = "different-bot" }},
		{"trigger", func(j *store.ScheduledJob) { j.Payload.BotTriggerID = "different-trigger" }},
		{"cron", func(j *store.ScheduledJob) { j.Schedule.Cron = "0 1 * * *" }},
		{"timezone", func(j *store.ScheduledJob) { j.Schedule.Timezone = "Europe/Berlin" }},
		{"flow", func(j *store.ScheduledJob) { j.Payload.Flow = "unreviewed-flow" }},
		{"instructions", func(j *store.ScheduledJob) { j.Payload.Instructions = "unreviewed instructions" }},
		{"params", func(j *store.ScheduledJob) { j.Payload.Params = map[string]string{"unreviewed": "true"} }},
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mutation := range mutations {
			t.Run(string(scope)+"/"+mutation.name, func(t *testing.T) {
				repo, ctx, jobs, seed := scheduledFixture(t, scope, store.TriggerCron, 20)
				b, err := repo.Get(ctx, seed.BotID)
				if err != nil {
					t.Fatal(err)
				}
				b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "review"})
				if err != nil {
					t.Fatal(err)
				}
				v, err := repo.GetVersion(ctx, b.ID, b.Version)
				if err != nil {
					t.Fatal(err)
				}
				job := jobs.Get(ctx, seed.JobID)
				mutation.apply(job)
				if mutation.name == "missing" {
					if err := jobs.Remove(ctx, job.ID); err != nil {
						t.Fatal(err)
					}
				} else if mutation.name == "owner" {
					// Scheduler Update intentionally cannot transfer immutable ownership.
					// Replace only this disposable fixture to test a foreign stored owner.
					if err := jobs.Remove(ctx, job.ID); err != nil {
						t.Fatal(err)
					}
					if err := jobs.Add(ctx, job); err != nil {
						t.Fatal(err)
					}
				} else if err := jobs.Update(ctx, job); err != nil {
					t.Fatal(err)
				}
				job = jobs.Get(ctx, job.ID)
				_, err = repo.ConfigureReviewed(ctx, store.BotConfigurationRequest{BotID: b.ID, ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "cannot repair stale authority", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
				if !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("stale schedule binding accepted: %v", err)
				}
				after, err := repo.Get(ctx, b.ID)
				if err != nil || after.Revision != b.Revision || after.Version != b.Version || after.Status != store.BotPaused {
					t.Fatalf("failed review leaked version/lifecycle: %+v %v", after, err)
				}
				if _, err := repo.GetVersion(ctx, b.ID, b.Version+1); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("failed review persisted an immutable version: %v", err)
				}
				if got := jobs.Get(ctx, seed.JobID); !reflect.DeepEqual(got, job) {
					t.Fatalf("failed review rewrote changed schedule authority: got=%+v want=%+v", got, job)
				}
			})
		}
	}
}

func TestReviewedConfigurationResumeKeepsSchedulesDisabled(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, jobs, seed := scheduledFixture(t, scope, store.TriggerCron, 20)
			b, err := repo.Get(ctx, seed.BotID)
			if err != nil {
				t.Fatal(err)
			}
			independent := *jobs.Get(ctx, seed.JobID)
			independent.ID = uuid.NewString()
			independent.Name = "independent owner schedule"
			independent.Mode = "routine"
			independent.Payload = store.JobPayload{Flow: "independent-flow"}
			if err := jobs.Add(ctx, &independent); err != nil {
				t.Fatal(err)
			}
			independentBefore := jobs.Get(ctx, independent.ID)
			b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "review delegated read"})
			if err != nil {
				t.Fatal(err)
			}
			v, err := repo.GetVersion(ctx, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			beforeJob := jobs.Get(ctx, seed.JobID)
			b, err = repo.ConfigureReviewed(ctx, store.BotConfigurationRequest{BotID: b.ID, ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "review restricted child", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
			if err != nil {
				t.Fatal(err)
			}
			beforeJob.Payload.BotVersion = b.Version
			if got := jobs.Get(ctx, seed.JobID); b.Status != store.BotPaused || !reflect.DeepEqual(got, beforeJob) {
				t.Fatalf("review must only rebind version, not grant recurrence: bot=%+v got=%+v want=%+v", b, got, beforeJob)
			}
			repo.activationCheck = func(_ context.Context, _ store.Bot, _ store.BotVersion) error { return nil }
			b, err = repo.Resume(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "resume interactive work only"})
			if err != nil {
				t.Fatalf("resume reviewed configuration: %v; schedule=%+v", err, jobs.Get(ctx, seed.JobID))
			}
			job := jobs.Get(ctx, seed.JobID)
			if b.Status != store.BotActive || job == nil || job.Enabled || job.NextRun != nil || job.Payload.BotVersion != b.Version {
				t.Fatalf("resume must retain disabled recurrence at current version: bot=%+v job=%+v", b, job)
			}
			if got := jobs.Get(ctx, independent.ID); !reflect.DeepEqual(got, independentBefore) {
				t.Fatalf("Bot review/resume changed independent schedule: got=%+v want=%+v", got, independentBefore)
			}
		})
	}
}
