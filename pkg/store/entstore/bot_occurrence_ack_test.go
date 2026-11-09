package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// forcedOccurrenceAckFailure injects a failure after run/outbox insertion.
type forcedOccurrenceAckFailure struct{ botDraftDB }

func (d forcedOccurrenceAckFailure) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(forcedOccurrenceAckFailure{tx}) })
}
func (d forcedOccurrenceAckFailure) advanceScheduledAdmission(ctx context.Context, rec botStoredRecord) error {
	return d.botDraftDB.(botScheduledDB).advanceScheduledAdmission(ctx, rec)
}
func (d forcedOccurrenceAckFailure) readBotSchedule(ctx context.Context, id store.BotID) (*store.ScheduledJob, error) {
	return d.botDraftDB.(botScheduledDB).readBotSchedule(ctx, id)
}
func (d forcedOccurrenceAckFailure) acknowledgeScheduledOccurrence(context.Context, store.BotScheduledOccurrence, time.Time, *time.Time) error {
	return store.ErrBotConflict
}

func TestBotOccurrenceAcknowledgementRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, jobs, q := scheduledFixture(t, scope, store.TriggerCron, 2)
			original := repo.db
			repo.db = forcedOccurrenceAckFailure{original}
			if _, err := repo.AdmitScheduledRun(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("expected injected conflict: %v", err)
			}
			repo.db = original
			runs, err := repo.ListRuns(ctx, q.BotID, store.BotPage{})
			if err != nil || len(runs) != 0 {
				t.Fatalf("run survived rollback: %v %v", runs, err)
			}
			saved := jobs.Get(ctx, q.JobID)
			if saved.LastRun != nil || saved.NextRun == nil || !saved.NextRun.Equal(q.OccurrenceAt) {
				t.Fatal("schedule survived rollback")
			}
			receipt, err := repo.AdmitScheduledRun(ctx, q)
			if err != nil || receipt.Duplicate || !receipt.Acknowledged {
				t.Fatalf("retry: %+v %v", receipt, err)
			}
		})
	}
}

func TestBotOccurrenceAtomicAcknowledgement(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []store.TriggerKind{store.TriggerCron, store.TriggerGoalDeadline} {
			t.Run(string(scope)+"/"+string(kind), func(t *testing.T) {
				repo, ctx, jobs, q := scheduledFixture(t, scope, kind, 2)
				receipt, err := repo.AdmitScheduledRun(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				saved := jobs.Get(ctx, q.JobID)
				if !receipt.Acknowledged || saved.LastRun == nil {
					t.Fatal("missing atomic acknowledgement")
				}
				if kind == store.TriggerCron && (saved.NextRun == nil || !saved.NextRun.After(q.OccurrenceAt)) {
					t.Fatal("due occurrence not advanced")
				}
				if kind == store.TriggerGoalDeadline && saved.NextRun != nil {
					t.Fatal("deadline repeated")
				}
				saved.Enabled = false
				saved.Payload.BotVersion++
				if err := jobs.Update(ctx, saved); err != nil {
					t.Fatal(err)
				}
				replay, err := repo.AdmitScheduledRun(ctx, q)
				if err != nil || !replay.Duplicate || replay.Run.ID != receipt.Run.ID {
					t.Fatalf("replay after rebind: %+v %v", replay, err)
				}
				after := jobs.Get(ctx, q.JobID)
				if after.Enabled || after.Payload.BotVersion != saved.Payload.BotVersion {
					t.Fatal("replay overwrote edit")
				}
				runs, err := repo.ListRuns(ctx, q.BotID, store.BotPage{})
				if err != nil || len(runs) != 1 {
					t.Fatal("duplicate run", err)
				}
			})
		}
	}
}
