package entstore

import (
	"context"
	"fmt"
	personalrun "github.com/SAP/astonish/ent/personal/botrun"
	teamrun "github.com/SAP/astonish/ent/team/botrun"
	"github.com/SAP/astonish/pkg/store"
	"time"
)

// The publication transaction has resolved actual speech eligibility. No report
// prose crosses this boundary, and the receipt's sequence fences replay.
func appendNeutralTerminalPublication(ctx context.Context, tx botDB, rec *botStoredRecord, source store.BotRun, at time.Time) error {
	if source.Status != store.RunPartial && source.Status != store.RunFailed {
		return nil
	}
	saved, err := tx.ReadRun(ctx, source.BotID, source.ID)
	if err != nil {
		return err
	}
	// An older interrupted/waiting outcome must never overwrite a resumed run.
	if saved.Run.Status != source.Status || saved.Run.SessionID != source.SessionID {
		return nil
	}
	saved.Run.TerminalReason = store.BotTerminalNoAnswer
	data, err := encodeBotRunData(ctx, tx, *saved)
	if err != nil {
		return err
	}
	switch d := tx.(type) {
	case personalBotDB:
		_, err = d.client.BotRun.Update().Where(personalrun.IDEQ(source.ID), personalrun.BotIDEQ(source.BotID)).SetData(data).Save(ctx)
	case teamBotDB:
		_, err = d.client.BotRun.Update().Where(teamrun.IDEQ(source.ID), teamrun.BotIDEQ(source.BotID)).SetData(data).Save(ctx)
	default:
		return store.ErrBotUnsupported
	}
	if err != nil {
		return err
	}
	if err := tx.AppendRunState(ctx, rec.Bot, saved.Run, rec.LastSequence, at); err != nil {
		return fmt.Errorf("neutral state append: %w", err)
	}
	return nil
}
