package entstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"testing"
	"time"
)

func TestBotScheduledMandateExactAdmission(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			// Keep the real first occurrence inside the finite source mandate lifetime.
			q.Payload.Schedule.Trigger.Cron = "* * * * *"
			s.policy = func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, e := authorizedSchedulePolicy(c, ref)
				p.Limits.MaxDailyRuns = 2000
				return p, e
			}
			// Existing source ceiling is intentionally lower; use daily schedule and move
			// the test clock close to its next occurrence instead of renewing authority.
			q.Payload.Schedule.Trigger.Cron = "0 9 * * *"
			now := time.Now().UTC()
			r.now = func() time.Time { return now }
			op, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "queued")}); err != nil {
				t.Fatal(err)
			}
			applied, err := s.ApplyAuthorizedOperation(owner, q.MandateRef, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := applied.Receipt.Schedule
			// Scheduler persistence is exercised at a due occurrence without changing its
			// cron definition. Mandate expiry remains real; test clock chooses current time.
			jobs := r.db.(botTriggerDB).triggerScheduler()
			job := jobs.Get(owner, receipt.ScheduleID.String())
			due := now
			job.NextRun = &due
			if err = jobs.Update(owner, job); err != nil {
				t.Fatal(err)
			}
			identity := fmt.Sprintf("%s/%d/%s/%s", q.MandateRef.BotID, receipt.Version, receipt.TriggerID, due.Format(time.RFC3339Nano))
			occurrence := store.BotScheduledOccurrence{BotID: q.MandateRef.BotID, Version: receipt.Version, TriggerID: receipt.TriggerID, JobID: receipt.ScheduleID.String(), OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))}
			admission, err := r.AdmitScheduledRun(owner, occurrence)
			if err != nil {
				t.Fatal(err)
			}
			if admission.Run.MandateRef == nil || *admission.Run.MandateRef == q.MandateRef {
				t.Fatal("missing separate scheduled task pin")
			}
			ref := *admission.Run.MandateRef
			if _, err = s.VerifyBotMandate(context.Background(), ref, execution.BotMandatePurposeTask); err != nil {
				t.Fatal(err)
			}
			if err = s.ReserveBotMandateRun(context.Background(), ref, admission.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err = s.ReserveBotMandateRun(context.Background(), ref, q.Lease.RunID); err == nil {
				t.Fatal("source run borrowed future pin")
			}
			again, err := r.AdmitScheduledRun(owner, occurrence)
			if err != nil || !again.Duplicate || again.Run.ID != admission.Run.ID {
				t.Fatalf("duplicate: %+v %v", again, err)
			}
			if err = s.RevokeBotMandate(owner, q.MandateRef); err != nil {
				t.Fatal(err)
			}
			if _, err = s.VerifyBotMandate(context.Background(), ref, execution.BotMandatePurposeTask); err == nil {
				t.Fatal("parent revocation ignored")
			}
		})
	}
}
func TestBotAuthorizedPendingBackoff(t *testing.T) {
	r, s, owner, worker, q := authorizedScheduleFixture(t, store.BotScopePersonal)
	op, err := s.EnqueueAuthorizedOperation(worker, q)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileAuthorizedOperations(owner, q.MandateRef.BotID, 1); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.ReadAuthorizedOperation(owner, q.MandateRef, op.ID)
	if err != nil || blocked.Status != store.BotOperationBlocked {
		t.Fatalf("blocked %+v %v", blocked, err)
	}
	if err = s.ReconcileAuthorizedOperations(owner, q.MandateRef.BotID, 1); err != nil {
		t.Fatal(err)
	}
	same, _ := s.ReadAuthorizedOperation(owner, q.MandateRef, op.ID)
	if same.Revision != blocked.Revision {
		t.Fatal("backoff ignored")
	}
	if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "queued")}); err != nil {
		t.Fatal(err)
	}
	now := r.clock().Add(61 * time.Second)
	r.now = func() time.Time { return now }
	s.repo.now = r.now
	if err = s.ReconcileAuthorizedOperations(owner, q.MandateRef.BotID, 1); err != nil {
		t.Fatal(err)
	}
	done, _ := s.ReadAuthorizedOperation(owner, q.MandateRef, op.ID)
	if done.Status != store.BotOperationApplied {
		t.Fatalf("not applied: %+v", done)
	}
}
