package entstore

import (
	"context"
	prun "github.com/SAP/astonish/ent/personal/botrun"
	trun "github.com/SAP/astonish/ent/team/botrun"
	"github.com/SAP/astonish/pkg/store"
	"time"
)

func appendBotRunStateTx(ctx context.Context, tx botDraftDB, b store.Bot, run store.BotRun, sequence int64, at time.Time) error {
	return tx.AppendRunState(ctx, b, run, sequence, at)
}

// Always read authoritative state; only an exact already-persisted sequence/time
// permits coalescing the envelope write. Transcript and callbacks are never skipped.
func (d personalBotDB) AppendRunState(ctx context.Context, b store.Bot, run store.BotRun, sequence int64, at time.Time) error {
	saved, err := d.ReadRun(ctx, b.ID, run.ID)
	if err != nil {
		return err
	}
	if saved.Run.StateSequence != sequence || !saved.Run.StateCommittedAt.Equal(at) {
		saved.Run.StateSequence, saved.Run.StateCommittedAt = sequence, at
		data, err := d.contentCodec().encodeRun(ctx, *saved)
		if err != nil {
			return err
		}
		if _, err = d.client.BotRun.Update().Where(prun.IDEQ(run.ID), prun.BotIDEQ(b.ID)).SetData(data).Save(ctx); err != nil {
			return err
		}
	}
	run = saved.Run
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
	saved, err := d.ReadRun(ctx, b.ID, run.ID)
	if err != nil {
		return err
	}
	if saved.Run.StateSequence != sequence || !saved.Run.StateCommittedAt.Equal(at) {
		saved.Run.StateSequence, saved.Run.StateCommittedAt = sequence, at
		data, err := d.contentCodec().encodeRun(ctx, *saved)
		if err != nil {
			return err
		}
		if _, err = d.client.BotRun.Update().Where(trun.IDEQ(run.ID), trun.BotIDEQ(b.ID)).SetData(data).Save(ctx); err != nil {
			return err
		}
	}
	run = saved.Run
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

func queueBotRunStateNotification(ctx context.Context, callbacks *[]func(), state store.BotRunState) {
	if notify := store.BotRunStateNotifierFromContext(ctx); notify != nil && callbacks != nil {
		*callbacks = append(*callbacks, func() { notify(ctx, state) })
	}
}

// The caller already saved state but AppendRunState still verifies SQL itself.
func insertSavedBotRunStateActivity(ctx context.Context, tx botDraftDB, b store.Bot, run store.BotRun, a store.BotActivity, key string) error {
	if a.RunID == nil || *a.RunID != run.ID || a.BotID != b.ID || run.BotID != b.ID {
		return store.ErrBotInvalid
	}
	if err := tx.InsertActivity(ctx, a, key); err != nil {
		return err
	}
	return appendBotRunStateTx(ctx, tx, b, run, a.Sequence, a.CreatedAt)
}

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
