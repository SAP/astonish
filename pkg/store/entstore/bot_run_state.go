package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"time"
)

// appendBotRunStateTx is called only after the durable status CAS, in the same
// root-serialized transaction. It never infers a transition from a setter.
func appendBotRunStateTx(ctx context.Context, tx botDraftDB, b store.Bot, run store.BotRun, sequence int64, at time.Time) error {
	return tx.AppendRunState(ctx, b, run, sequence, at)
}

func (d personalBotDB) AppendRunState(ctx context.Context, b store.Bot, run store.BotRun, sequence int64, at time.Time) error {
	event, err := store.NewBotRunStateEvent(b, run, sequence, at)
	if err != nil {
		return err
	}
	if err := d.appendRunTranscript(ctx, b, run, event); err != nil {
		return err
	}
	queueBotRunStateNotification(ctx, d.afterCommit, event.Actions.StateDelta[store.BotRunStateKey].(store.BotRunState))
	return nil
}
func (d teamBotDB) AppendRunState(ctx context.Context, b store.Bot, run store.BotRun, sequence int64, at time.Time) error {
	event, err := store.NewBotRunStateEvent(b, run, sequence, at)
	if err != nil {
		return err
	}
	if err := d.appendRunTranscript(ctx, b, run, event); err != nil {
		return err
	}
	queueBotRunStateNotification(ctx, d.afterCommit, event.Actions.StateDelta[store.BotRunStateKey].(store.BotRunState))
	return nil
}

// queueBotRunStateNotification retains transaction-local callbacks, including
// nested title admission. Nothing is published until the outer commit succeeds.
func queueBotRunStateNotification(ctx context.Context, callbacks *[]func(), state store.BotRunState) {
	if notify := store.BotRunStateNotifierFromContext(ctx); notify != nil && callbacks != nil {
		*callbacks = append(*callbacks, func() { notify(ctx, state) })
	}
}

// insertBotRunStateActivity is reserved for successful transition producers:
// admission, claim, commit, recovery, cancellation, quiescence and requeue.
// Read back SQL state rather than publishing speculative in-memory mutations.
func insertBotRunStateActivity(ctx context.Context, tx botDraftDB, a store.BotActivity, key string) error {
	if err := tx.InsertActivity(ctx, a, key); err != nil {
		return err
	}
	if a.RunID == nil {
		return store.ErrBotInvalid
	}
	rec, err := tx.GetBot(ctx, a.BotID)
	if err != nil {
		return err
	}
	saved, err := tx.ReadRun(ctx, a.BotID, *a.RunID)
	if err != nil {
		return err
	}
	return appendBotRunStateTx(ctx, tx, rec.Bot, saved.Run, a.Sequence, a.CreatedAt)
}
