package entstore

import (
	"context"
	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const botProvisioningTracked = "home_provisioning_tracked"
const botProvisioningAttempted = "home_provisioning_attempted"

type botProvisioningDB interface {
	markProvisioning(context.Context, botStoredRecord) error
	confirmUnprovisioned(context.Context, botStoredRecord) error
}

// BeginBotHomeProvisioning MUST commit before allocation. It serializes with
// permanent admission and marks ambiguous allocation failures durably. An empty
// registry alone is never sufficient to claim storage is absent.
func BeginBotHomeProvisioning(ctx context.Context, bots store.BotStore, id store.BotID, revision int64) error {
	var r *botRepository
	switch s := bots.(type) {
	case *personalBotStore:
		r = s.repository()
	case *teamBotStore:
		r = s.repository()
	default:
		return store.ErrBotUnsupported
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, id)
		if err != nil {
			return err
		}
		if _, err = r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
			return err
		}
		if rec.Bot.Status != store.BotDraft || rec.Bot.PermanentDelete || (rec.Bot.Revision != revision && !(rec.Bot.Home.ID != uuid.Nil && rec.Bot.Revision == revision+1)) {
			return store.ErrBotConflict
		}
		db, ok := tx.(botProvisioningDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		return db.markProvisioning(ctx, *rec)
	})
}
func provisioningMetadata(rec botStoredRecord) map[string]any {
	data := make(map[string]any, len(rec.Metadata)+1)
	for k, v := range rec.Metadata {
		data[k] = v
	}
	data[botProvisioningAttempted] = true
	return data
}
func (d personalBotDB) markProvisioning(ctx context.Context, rec botStoredRecord) error {
	n, err := d.client.Bot.Update().Where(personalbot.IDEQ(rec.Bot.ID), personalbot.RevisionEQ(rec.Bot.Revision), personalbot.LastSeqEQ(rec.LastSequence), personalbot.StatusEQ(personalbot.StatusDraft)).SetData(provisioningMetadata(rec)).AddLastSeq(1).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) markProvisioning(ctx context.Context, rec botStoredRecord) error {
	n, err := d.client.Bot.Update().Where(teambot.IDEQ(rec.Bot.ID), teambot.RevisionEQ(rec.Bot.Revision), teambot.LastSeqEQ(rec.LastSequence), teambot.StatusEQ(teambot.StatusDraft)).SetData(provisioningMetadata(rec)).AddLastSeq(1).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d personalBotDB) confirmUnprovisioned(ctx context.Context, rec botStoredRecord) error {
	n, err := d.client.Bot.Update().Where(personalbot.IDEQ(rec.Bot.ID), personalbot.RevisionEQ(rec.Bot.Revision), personalbot.LastSeqEQ(rec.LastSequence), personalbot.StatusEQ(personalbot.StatusDeleted), personalbot.HomeBindingIsNil(), personalbot.ActiveRunIDIsNil(), personalbot.LeaseExpiresAtIsNil()).SetCleanupComplete(true).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) confirmUnprovisioned(ctx context.Context, rec botStoredRecord) error {
	n, err := d.client.Bot.Update().Where(teambot.IDEQ(rec.Bot.ID), teambot.RevisionEQ(rec.Bot.Revision), teambot.LastSeqEQ(rec.LastSequence), teambot.StatusEQ(teambot.StatusDeleted), teambot.HomeBindingIsNil(), teambot.ActiveRunIDIsNil(), teambot.LeaseExpiresAtIsNil()).SetCleanupComplete(true).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
