package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

type authorizedFailDB struct{ botDraftDB }

func (d authorizedFailDB) Transaction(ctx context.Context, f func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return f(authorizedFailTx{tx}) })
}

type authorizedFailTx struct{ botDraftDB }

func (d authorizedFailTx) triggerScheduler() store.SchedulerStore {
	return d.botDraftDB.(botTriggerDB).triggerScheduler()
}
func (d authorizedFailTx) saveTriggerVersion(c context.Context, r botStoredRecord, v int64, n time.Time) error {
	return d.botDraftDB.(botTriggerDB).saveTriggerVersion(c, r, v, n)
}
func (d authorizedFailTx) updateControlVersion(c context.Context, r botStoredRecord, v int64, n time.Time) error {
	return d.botDraftDB.(botControlVersionDB).updateControlVersion(c, r, v, n)
}
func (d authorizedFailTx) saveMandateMetadata(context.Context, *botStoredRecord) error {
	return errors.New("injected receipt persistence failure")
}

func TestBotAuthorizedScheduleAtomicRollbackAndSourceDigest(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, _, worker, q := authorizedScheduleFixture(t, scope)
			o, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "queued"}}); err != nil {
				t.Fatal(err)
			}
			before, _ := r.db.GetBot(context.Background(), q.MandateRef.BotID)
			original := s.repo.db
			s.repo.db = authorizedFailDB{original}
			if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID); err == nil {
				t.Fatal("receipt failure applied")
			}
			s.repo.db = original
			after, _ := r.db.GetBot(context.Background(), q.MandateRef.BotID)
			if after.Bot.Version != before.Bot.Version || after.Bot.Revision != before.Bot.Revision || after.LastSequence != before.LastSequence {
				t.Fatal("root writes escaped transaction")
			}
			id := uuid.NewSHA1(o.ID, []byte("schedule"))
			if r.db.(botTriggerDB).triggerScheduler().Get(context.Background(), id.String()) != nil {
				t.Fatal("job survived rollback")
			}
			pending, err := s.ReadAuthorizedOperation(context.Background(), q.MandateRef, o.ID)
			if err != nil || pending.Status != store.BotOperationPending {
				t.Fatalf("queue lost %v", err)
			}
			s.repo.db = authorizedTamperDB{original}
			if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID); err == nil {
				t.Fatal("source digest change accepted")
			}
			s.repo.db = original
			if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID); err != nil {
				t.Fatal("rollback not retryable", err)
			}
		})
	}
}

type authorizedTamperDB struct{ botDraftDB }

func (d authorizedTamperDB) ReadRun(c context.Context, b, r uuid.UUID) (*botStoredRun, error) {
	run, err := d.botDraftDB.ReadRun(c, b, r)
	if err == nil {
		run.Instructions = "untrusted replacement"
	}
	return run, err
}
