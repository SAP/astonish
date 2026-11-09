package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestEmployeeRoutineResumeTargets(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			o, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "queued"}}); err != nil {
				t.Fatal(err)
			}
			o, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID)
			if err != nil {
				t.Fatal(err)
			}
			scheduleID := o.Receipt.Schedule.ScheduleID
			pause := store.BotOperation{IdempotencyKey: "pause-test", ID: uuid.New(), MandateRef: q.MandateRef, Payload: store.BotControlPayload{Operation: store.BotControlPriorityPause, Reason: "stop"}, Decision: store.BotOperationExecute, Status: store.BotOperationApplying, Revision: 1}
			err = r.db.Transaction(owner, func(tx botDraftDB) error {
				rec, e := tx.GetBot(owner, q.MandateRef.BotID)
				if e != nil {
					return e
				}
				return s.pauseRoutineTx(owner, tx, rec, &pause)
			})
			if err != nil {
				t.Fatal(err)
			}
			job := r.db.(botTriggerDB).triggerScheduler().Get(owner, scheduleID.String())
			if job == nil || job.Enabled {
				t.Fatal("pause left schedule enabled")
			}
			resume := store.BotOperation{ID: uuid.New(), Payload: store.BotControlPayload{Operation: store.BotControlResumeTasks, Reason: "resume only task", ResumeTargets: &store.BotControlResumeTargets{ScheduleIDs: []uuid.UUID{uuid.New()}}}}
			apply := func() error {
				return r.db.Transaction(owner, func(tx botDraftDB) error {
					rec, e := tx.GetBot(owner, q.MandateRef.BotID)
					if e != nil {
						return e
					}
					return s.resumeRoutineTx(owner, tx, rec, resume)
				})
			}
			if err = apply(); err == nil {
				t.Fatal("unknown schedule resumed")
			}
			resume.Payload.ResumeTargets.ScheduleIDs = []uuid.UUID{scheduleID}
			if err = apply(); err != nil {
				t.Fatal(err)
			}
			job = r.db.(botTriggerDB).triggerScheduler().Get(owner, scheduleID.String())
			if job == nil || !job.Enabled {
				t.Fatal("selected schedule not resumed")
			}
			// Pause again, then expire the original task grant. No refresh on resume.
			pause.ID = uuid.New()
			err = r.db.Transaction(owner, func(tx botDraftDB) error {
				rec, e := tx.GetBot(owner, q.MandateRef.BotID)
				if e != nil {
					return e
				}
				return s.pauseRoutineTx(owner, tx, rec, &pause)
			})
			if err != nil {
				t.Fatal(err)
			}
			old := r.now
			future := r.clock().Add(48 * time.Hour)
			r.now = func() time.Time { return future }
			defer func() { r.now = old }()
			if err = apply(); err == nil {
				t.Fatal("expired grant renewed by resume")
			}
		})
	}
}
