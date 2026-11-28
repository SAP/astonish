package entstore

import (
	"context"
	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/store"
	"time"
)

// Proposal-only CAS: the terminal-source/no-barrier check must remain true at the write.
type botControlVersionDB interface {
	updateControlVersion(context.Context, botStoredRecord, int64, time.Time) error
}

func (d personalBotDB) updateControlVersion(ctx context.Context, rec botStoredRecord, version int64, now time.Time) error {
	n, err := d.client.Bot.Update().Where(personalbot.IDEQ(rec.Bot.ID), personalbot.RevisionEQ(rec.Bot.Revision), personalbot.VersionEQ(rec.Bot.Version), personalbot.LastSeqEQ(rec.LastSequence), personalbot.ActiveRunIDIsNil(), personalbot.StatusNEQ(personalbot.StatusDeleted)).AddRevision(1).SetVersion(version).AddLastSeq(1).SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) updateControlVersion(ctx context.Context, rec botStoredRecord, version int64, now time.Time) error {
	n, err := d.client.Bot.Update().Where(teambot.IDEQ(rec.Bot.ID), teambot.RevisionEQ(rec.Bot.Revision), teambot.VersionEQ(rec.Bot.Version), teambot.LastSeqEQ(rec.LastSequence), teambot.ActiveRunIDIsNil(), teambot.StatusNEQ(teambot.StatusDeleted)).AddRevision(1).SetVersion(version).AddLastSeq(1).SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
