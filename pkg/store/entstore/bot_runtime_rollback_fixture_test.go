package entstore

import (
	"context"
	"errors"
)

// Fail after all scoped writes have executed, so SQL rollback must undo both
// runtime state and transcript/approval changes. Workspace failures no longer
// exercise database-backed runtime persistence.
type failRuntimeTransaction struct{ botDraftDB }

func (d failRuntimeTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errors.New("injected runtime SQL commit failure")
	})
}
