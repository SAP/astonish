package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

type authorizedCASRejectDB struct {
	botDraftDB
	writes *int
}

func (d authorizedCASRejectDB) Transaction(ctx context.Context, f func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error {
		return f(authorizedCASRejectTx{authorizedFailTx{tx}, d.writes})
	})
}

type authorizedCASRejectTx struct {
	authorizedFailTx
	writes *int
}

func (d authorizedCASRejectTx) updateControlVersion(context.Context, botStoredRecord, int64, time.Time) error {
	return store.ErrBotConflict
}
func (d authorizedCASRejectTx) triggerScheduler() store.SchedulerStore {
	return authorizedWriteCounter{d.authorizedFailTx.triggerScheduler(), d.writes}
}

type authorizedWriteCounter struct {
	store.SchedulerStore
	writes *int
}

func (s authorizedWriteCounter) Add(ctx context.Context, j *store.ScheduledJob) error {
	*s.writes++
	return s.SchedulerStore.Add(ctx, j)
}
func (s authorizedWriteCounter) Update(ctx context.Context, j *store.ScheduledJob) error {
	*s.writes++
	return s.SchedulerStore.Update(ctx, j)
}
func (s authorizedWriteCounter) Remove(ctx context.Context, id string) error {
	*s.writes++
	return s.SchedulerStore.Remove(ctx, id)
}

func TestBotAuthorizedScheduleCASPrecedesSchedulerWrites(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			op, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "queued"}}); err != nil {
				t.Fatal(err)
			}
			original := s.repo.db
			writes := 0
			s.repo.db = authorizedCASRejectDB{original, &writes}
			if _, err = s.ApplyAuthorizedOperation(owner, q.MandateRef, op.ID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("CAS rejection: %v", err)
			}
			if writes != 0 {
				t.Fatalf("%d scheduler mutations before root CAS", writes)
			}
			s.repo.db = original
			applied, err := s.ApplyAuthorizedOperation(owner, q.MandateRef, op.ID)
			if err != nil || applied.Status != store.BotOperationApplied {
				t.Fatalf("retry: %+v %v", applied, err)
			}
			replay, err := s.ApplyAuthorizedOperation(owner, q.MandateRef, op.ID)
			if err != nil || replay.ID != applied.ID || replay.Revision != applied.Revision {
				t.Fatalf("replay: %+v %v", replay, err)
			}
		})
	}
}
