package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

type transactionCleanupFailure struct{ closed bool }

func (r *transactionCleanupFailure) Close() error {
	r.closed = true
	return errors.New("transaction-helper-cleanup-sentinel")
}
func TestBotTransactionHelperCleanupRollsBack(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, exact, backend := selectionFixture(t, scope)
			if err := r.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatal(err)
			}
			resource := &transactionCleanupFailure{}
			wrap := func(c *botContentCodec) *botContentCodec {
				return &botContentCodec{OpenIntake: c.OpenIntake, Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
					operation := sandbox.BotHomeFileOperationFromContext(ctx)
					if operation == nil {
						t.Fatal("SQL callback content lost transaction scope")
					}
					_, _, release, err := operation.Borrow(ctx, "helper", func(context.Context) (sandbox.BotHomeFileResource, error) { return resource, nil })
					if err != nil {
						return nil, err
					}
					if err = release(true); err != nil {
						return nil, err
					}
					return c.Open(ctx, id)
				}}
			}
			switch d := r.db.(type) {
			case personalBotDB:
				d.content = wrap(d.content)
				r.db = d
			case teamBotDB:
				d.content = wrap(d.content)
				r.db = d
			}
			before, err := r.db.GetBot(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			err = r.db.Transaction(ctx, func(tx botDraftDB) error {
				if _, err := tx.ReadVersion(ctx, b.ID, 1); err != nil {
					return err
				}
				if err := tx.AdvanceSequence(ctx, b.ID, before.LastSequence); err != nil {
					t.Fatal(err)
				}
				changed, err := tx.GetBot(ctx, b.ID)
				if err != nil || changed.LastSequence != before.LastSequence+1 {
					t.Fatalf("callback did not write SQL: %v %+v", err, changed)
				}
				return nil
			})
			if err == nil || !resource.closed {
				t.Fatalf("cleanup failure discarded: %v closed=%v", err, resource.closed)
			}
			row, err := r.db.GetBot(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if row.LastSequence != before.LastSequence {
				t.Fatal("SQL committed despite helper cleanup failure")
			}
		})
	}
}
