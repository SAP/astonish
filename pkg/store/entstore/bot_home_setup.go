package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// AuthorizeBotHomeSetup reuses the scoped repository's live owner/admin edit
// authorization before any external provisioning. It grants no service authority.
func AuthorizeBotHomeSetup(ctx context.Context, bots store.BotStore, id store.BotID, revision int64) (*store.Bot, error) {
	var r *botRepository
	switch s := bots.(type) {
	case *personalBotStore:
		r = s.repository()
	case *teamBotStore:
		r = s.repository()
	default:
		return nil, store.ErrBotUnsupported
	}
	if id == uuid.Nil || revision < 1 {
		return nil, store.ErrBotInvalid
	}
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	rec, err := r.db.GetBot(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err = r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
		return nil, err
	}
	if rec.Bot.Status != store.BotDraft || rec.ActiveRunID != nil || rec.LeaseExpiresAt != nil {
		return nil, store.ErrBotConflict
	}
	// Exactly one revision may have been consumed by a successful lost reply.
	if rec.Bot.Revision != revision && !(rec.Bot.Home.ID != uuid.Nil && rec.Bot.Revision == revision+1) {
		return nil, store.ErrBotConflict
	}
	return &rec.Bot, nil
}
