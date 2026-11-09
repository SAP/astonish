package entstore

import (
	"context"
	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

var _ store.BotPermanentDeleteStore = (*personalBotStore)(nil)
var _ store.BotPermanentDeleteStore = (*teamBotStore)(nil)

func (s *personalBotStore) BeginPermanentDelete(ctx context.Context, q store.BotPermanentDeleteRequest) (*store.Bot, error) {
	return s.repository().BeginPermanentDelete(ctx, q)
}
func (s *teamBotStore) BeginPermanentDelete(ctx context.Context, q store.BotPermanentDeleteRequest) (*store.Bot, error) {
	return s.repository().BeginPermanentDelete(ctx, q)
}
func (s *personalBotStore) PurgeDeleted(ctx context.Context, id store.BotID, revision int64) error {
	return s.repository().PurgeDeleted(ctx, id, revision)
}
func (s *teamBotStore) PurgeDeleted(ctx context.Context, id store.BotID, revision int64) error {
	return s.repository().PurgeDeleted(ctx, id, revision)
}

func (r *botRepository) BeginPermanentDelete(ctx context.Context, q store.BotPermanentDeleteRequest) (*store.Bot, error) {
	if !q.Confirm {
		return nil, store.ErrBotInvalid
	}
	return r.changeLifecycleIntent(ctx, store.BotLifecycleRequest{BotID: q.BotID, ActorID: q.ActorID, ExpectedRevision: q.ExpectedRevision, Reason: "Owner confirmed permanent deletion"}, botdomain.LifecycleDelete, true)
}

func (r *botRepository) PurgeDeleted(ctx context.Context, id store.BotID, revision int64) error {
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, id)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != rec.Bot.OwnerID {
			return store.ErrBotForbidden
		}
		now := r.clock()
		if rec.Bot.Revision != revision || rec.Bot.Status != store.BotDeleted || rec.ActiveRunID != nil || rec.LeaseExpiresAt != nil || rec.Bot.RestoreUntil == nil || rec.Bot.RestoreUntil.After(now) {
			return store.ErrBotConflict
		}
		if rec.Bot.Home == (store.BotHomeBinding{}) {
			tracked, _ := rec.Metadata[botProvisioningTracked].(bool)
			attempted, _ := rec.Metadata[botProvisioningAttempted].(bool)
			if !tracked || attempted || !rec.Bot.PermanentDelete {
				return store.ErrBotConflict
			}
			db, ok := tx.(botProvisioningDB)
			if !ok {
				return store.ErrBotUnsupported
			}
			if err := db.confirmUnprovisioned(ctx, *rec); err != nil {
				return err
			}
		}
		return tx.PurgeBot(ctx, *rec, now)
	})
}
