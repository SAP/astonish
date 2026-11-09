package entstore

import (
	"context"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// AuthorizeBotModelConfiguration uses the same live scope and owner/admin
// policy as reviewed Bot configuration. It does not authorize generic Chat.
func AuthorizeBotModelConfiguration(ctx context.Context, bots store.BotStore, id store.BotID) (*store.Bot, error) {
	var r *botRepository
	switch s := bots.(type) {
	case *personalBotStore:
		r = s.repository()
	case *teamBotStore:
		r = s.repository()
	default:
		return nil, store.ErrBotUnsupported
	}
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		// Authorize scope before accessing the record.
		return nil, err
	}
	rec, err := r.db.GetBot(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
		return nil, err
	}
	switch rec.Bot.Status {
	case store.BotDraft, store.BotPaused, store.BotStopped:
	default:
		return nil, store.ErrBotConflict
	}
	if rec.ActiveRunID != nil || rec.LeaseExpiresAt != nil {
		return nil, store.ErrBotConflict
	}
	return &rec.Bot, nil
}
