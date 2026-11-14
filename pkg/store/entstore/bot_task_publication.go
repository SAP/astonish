package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// WithBotTaskWorkspaceCommit serializes the physical mutation with run cancellation
// and fencing through the same repository transaction/row lock used by workers.
// The callback must be bounded and commit only the already staged confined bytes.
// A database failure after physical commit remains uncertain, never replay-safe.
func WithBotTaskWorkspaceCommit(c context.Context, s store.BotStore, m store.BotTaskMutation, commit func() error) error {
	return WithBotTaskWorkspaceCommitGuard(c, s, m, func(_ func(context.Context) error) error { return commit() })
}

// WithBotTaskWorkspaceCommitGuard provides the helper's final live check on the
// locked transaction. Reading through the outer store here would deadlock a
// single-connection SQLite store and miss the transaction's fenced view.
func WithBotTaskWorkspaceCommitGuard(c context.Context, s store.BotStore, m store.BotTaskMutation, commit func(func(context.Context) error) error) error {
	var r *botRepository
	switch v := s.(type) {
	case *personalBotStore:
		r = v.taskRepository()
	case *teamBotStore:
		r = v.taskRepository()
	default:
		return store.ErrBotUnsupported
	}
	return r.withTaskWorkspaceCommitGuard(c, m, commit)
}

func (r *botRepository) withTaskWorkspaceCommit(c context.Context, m store.BotTaskMutation, commit func() error) error {
	return r.withTaskWorkspaceCommitGuard(c, m, func(_ func(context.Context) error) error { return commit() })
}

func (r *botRepository) withTaskWorkspaceCommitGuard(c context.Context, m store.BotTaskMutation, commit func(func(context.Context) error) error) error {
	return r.db.Transaction(c, func(tx botDraftDB) error {
		_, e := r.taskMutation(c, tx, m)
		if e != nil {
			return e
		}
		if e := c.Err(); e != nil {
			return e
		}
		return commit(func(live context.Context) error {
			if err := live.Err(); err != nil {
				return err
			}
			_, err := r.skillMutation(live, tx, store.BotSkillMutation{BotID: m.BotID, Lease: m.Lease, ActorID: m.ActorID, IdempotencyKey: m.IdempotencyKey}, m.Lease.RunID != uuid.Nil)
			return err
		})
	})
}
