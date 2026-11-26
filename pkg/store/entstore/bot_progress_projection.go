package entstore

import (
	"context"

	prun "github.com/SAP/astonish/ent/personal/botrun"
	trun "github.com/SAP/astonish/ent/team/botrun"
	"github.com/SAP/astonish/pkg/store"
)

// saveProgressEnvelope requires the caller's root sequence/lease CAS in the same
// transaction. It changes no lease or scheduling columns.
func saveProgressEnvelope(ctx context.Context, tx botDB, saved *botStoredRun) error {
	data, err := encodeBotRunData(ctx, tx, *saved)
	if err != nil {
		return err
	}
	switch db := tx.(type) {
	case personalBotDB:
		_, err = db.client.BotRun.Update().Where(prun.IDEQ(saved.Run.ID), prun.BotIDEQ(saved.Run.BotID)).SetData(data).Save(ctx)
	case teamBotDB:
		_, err = db.client.BotRun.Update().Where(trun.IDEQ(saved.Run.ID), trun.BotIDEQ(saved.Run.BotID)).SetData(data).Save(ctx)
	default:
		return store.ErrBotUnsupported
	}
	return err
}

func progressProjection(saved *botStoredRun) *store.BotProgressProjection {
	p := store.BuildBotProgressProjection(saved.Progress, saved.Run)
	if p != nil {
		p.CoalescedOccurrences = saved.CoalescedOccurrences
	}
	return p
}
