package entstore

import (
	"context"
	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/store"
)

func (d personalBotDB) saveMandateMetadata(ctx context.Context, rec *botStoredRecord) error {
	n, err := d.client.Bot.Update().Where(personalbot.IDEQ(rec.Bot.ID), personalbot.LastSeqEQ(rec.LastSequence)).SetData(rec.Metadata).Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) saveMandateMetadata(ctx context.Context, rec *botStoredRecord) error {
	n, err := d.client.Bot.Update().Where(teambot.IDEQ(rec.Bot.ID), teambot.LastSeqEQ(rec.LastSequence)).SetData(rec.Metadata).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
