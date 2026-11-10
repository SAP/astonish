//go:build integration

package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// This suite must use real routed private and team PostgreSQL schemas. Missing
// disposable-server consent is a failure, never a skipped acceptance selector.
func TestIntegrationPostgresBotEmployeeOperations(t *testing.T) {
	requireDraftActivationPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newPostgresBotServicesFixture(t, scope, 100, 100)
		if err := f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: f.lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, f.worker, f.lease, "fixture complete")}); err != nil {
			t.Fatal(err)
		}
		f.owner = store.WithBotMandateAdmission(mandateOwnerContext(t, f.owner), authorizedSchedulePolicy)
		a, err := f.repo.AdmitMessage(f.owner, store.BotMessageRequest{BotID: f.run.BotID, Version: 2, Instructions: "Every day at nine, read the report. Keep other schedules unchanged.", IdempotencyKey: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		ref := *a.Run.MandateRef
		b, err := f.repo.Get(f.owner, ref.BotID)
		if err != nil {
			t.Fatal(err)
		}
		worker := execution.WithBotRunIdentity(f.owner, execution.BotRunIdentity{BotID: b.ID, RunID: a.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
		lease, err := f.repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: a.Run.ID, WorkerID: "pg-employee", ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		runtime := func() *botAuthorizedOperationRuntime {
			t.Helper()
			rt, e := NewBotAuthorizedOperationRuntime(f.bots, authorizedSchedulePolicy, func(context.Context, store.BotScheduleAuthorization) error { return nil }, func(context.Context, store.Bot, store.BotVersion) error { return nil })
			if e != nil {
				t.Fatal(e)
			}
			return rt.(*botAuthorizedOperationRuntime)
		}
		s := runtime()
		q := store.BotAuthorizedOperationRequest{Lease: lease, MandateRef: ref, IdempotencyKey: "pg-employee", Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "daily report", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "read the report", Enabled: true}}}}
		o, err := s.EnqueueAuthorizedOperation(worker, q)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, worker, lease, "captured")}); err != nil {
			t.Fatal(err)
		}
		before, err := f.repo.db.GetBot(f.owner, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		original := s.repo.db
		s.repo.db = authorizedFailDB{original}
		if _, err = s.ApplyAuthorizedOperation(f.owner, ref, o.ID); err == nil || err.Error() != "injected receipt persistence failure" {
			t.Fatalf("receipt rollback injection: %v", err)
		}
		s.repo.db = original
		f.reopenReviewed(t)
		s = runtime()
		after, err := f.repo.db.GetBot(f.owner, b.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("rollback after reopen: %v", err)
		}
		if f.repo.db.(botTriggerDB).triggerScheduler().Get(f.owner, uuid.NewSHA1(o.ID, []byte("schedule")).String()) != nil {
			t.Fatal("schedule escaped rollback")
		}
		postgresReviewedRace(t, func() error { _, e := s.ApplyAuthorizedOperation(f.owner, ref, o.ID); return e })
		f.reopenReviewed(t)
		s = runtime()
		applied, err := s.ReadAuthorizedOperation(f.owner, ref, o.ID)
		if err != nil || applied.Status != store.BotOperationApplied || applied.Receipt.Schedule == nil {
			t.Fatalf("durable operation: %+v %v", applied, err)
		}
		current, err := f.repo.Get(f.owner, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		version, err := f.repo.GetVersion(f.owner, b.ID, current.Version)
		if err != nil || len(version.Triggers) != 1 {
			t.Fatalf("single schedule: %v", err)
		}
		job := f.repo.db.(botTriggerDB).triggerScheduler().Get(f.owner, version.Triggers[0].ScheduleID.String())
		if job == nil || job.NextRun == nil {
			t.Fatal("missing due schedule")
		}
		due := time.Now().UTC()
		// Keep this persisted due occurrence inside the finite mandate lifetime.
		job.NextRun = &due
		if err = f.repo.db.(botTriggerDB).triggerScheduler().Update(f.owner, job); err != nil {
			t.Fatal(err)
		}
		job = f.repo.db.(botTriggerDB).triggerScheduler().Get(f.owner, job.ID)
		due = *job.NextRun
		f.repo.now = func() time.Time { return due }
		occurrence := draftActivationOccurrence(current, version.Triggers[0], due)
		old := f.repo.db
		f.repo.db = forcedOccurrenceAckFailure{old}
		if _, err = f.repo.AdmitScheduledRun(f.owner, occurrence); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("occurrence rollback: %v", err)
		}
		f.repo.db = old
		unchanged := f.repo.db.(botTriggerDB).triggerScheduler().Get(f.owner, job.ID)
		if !reflect.DeepEqual(job, unchanged) {
			t.Fatal("ack escaped rollback")
		}
		// The reopened client remains in use for revocation and deletion below;
		// register its cleanup with the owning scope, not the occurrence subtest.
		scopeTest := t
		t.Run("occurrence", func(t *testing.T) {
			postgresReviewedRace(t, func() error { _, e := f.repo.AdmitScheduledRun(f.owner, occurrence); return e })
			f.reopenReviewed(scopeTest)
			s = runtime()
			replay, err := f.repo.AdmitScheduledRun(f.owner, occurrence)
			if err != nil || !replay.Duplicate || !replay.Acknowledged {
				t.Fatalf("occurrence reopen replay: %+v %v", replay, err)
			}
			if replay.Run.MandateRef == nil {
				t.Fatal("scheduled task mandate missing after reopen")
			}
			taskRef := *replay.Run.MandateRef
			if err = s.ReserveBotMandateRun(f.owner, taskRef, replay.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err = s.ConsumeBotMandateQuota(f.owner, taskRef, replay.Run.ID, 1, 1, 0); err != nil {
				t.Fatal(err)
			}
			f.reopenReviewed(scopeTest)
			s = runtime()
			if err = s.ReserveBotMandateRun(f.owner, taskRef, replay.Run.ID); err != nil {
				t.Fatalf("reservation replay after reopen: %v", err)
			}
			if err = s.ConsumeBotMandateQuota(f.owner, taskRef, replay.Run.ID, 1<<62, 0, 0); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("quota ceiling after reopen: %v", err)
			}
			if err = s.ReleaseBotMandateRun(f.owner, taskRef, replay.Run.ID); err != nil {
				t.Fatal(err)
			}
			// Exercise the terminal (nil-next) typed update and exact-due guard on
			// PostgreSQL too, without weakening the preceding admission race.
			jobs := f.repo.db.(botTriggerDB).triggerScheduler()
			advanced := jobs.Get(f.owner, occurrence.JobID)
			if advanced == nil || advanced.NextRun == nil || !advanced.NextRun.After(due) {
				t.Fatal("recurrence did not advance")
			}
			ack := occurrence
			ack.OccurrenceAt = advanced.NextRun.Add(time.Second)
			db := f.repo.db.(botScheduledDB)
			if err = db.acknowledgeScheduledOccurrence(f.owner, ack, due, nil); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale due accepted: %v", err)
			}
			ack.OccurrenceAt = *advanced.NextRun
			if err = db.acknowledgeScheduledOccurrence(f.owner, ack, due, nil); err != nil {
				t.Fatalf("clear terminal occurrence: %v", err)
			}
			cleared := jobs.Get(f.owner, occurrence.JobID)
			if cleared == nil || cleared.NextRun != nil || cleared.Enabled != advanced.Enabled || !reflect.DeepEqual(cleared.Payload, advanced.Payload) {
				t.Fatal("terminal acknowledgement changed schedule configuration")
			}
		})
		if err = s.RevokeBotMandate(f.owner, ref); err != nil {
			t.Fatal(err)
		}
		f.reopenReviewed(t)
		s = runtime()
		// A revoked mandate cannot authorize a new operation, even with its old lease.
		q.IdempotencyKey = "revoked"
		if _, err = s.EnqueueAuthorizedOperation(worker, q); err == nil {
			t.Fatal("revoked mandate admitted work")
		}
		current, err = f.repo.Get(f.owner, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.repo.BeginPermanentDelete(f.owner, store.BotPermanentDeleteRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: current.Revision, Confirm: true}); err != nil {
			t.Fatal(err)
		}
		f.reopenReviewed(t)
		rec, err := f.repo.db.GetBot(f.owner, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := rec.Metadata[mandateHeadKey]; ok {
			t.Fatal("purge retained mandate head")
		}
		if _, ok := rec.Metadata["authorized-pending:"+o.ID.String()]; ok {
			t.Fatal("purge retained pending operation")
		}
	})
}
