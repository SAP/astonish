package entstore

import (
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func (s *Store) checkBotDelivery(ctx context.Context, owner uuid.UUID, target store.BotChannelBinding) error {
	return store.CheckBotDeliveryIdentity(ctx, s, owner, target)
}
func (r *botRepository) authorizeDeliveryIdentity(ctx context.Context, owner uuid.UUID, target store.BotChannelBinding) error {
	if r.auth.checkDelivery == nil {
		return fmt.Errorf("%w: notification destination verifier unavailable", store.ErrBotForbidden)
	}
	return r.auth.checkDelivery(ctx, owner, target)
}

func (s *personalBotStore) AuthorizeBotDelivery(ctx context.Context, id store.BotID, target store.BotChannelBinding) error {
	return s.repository().AuthorizeBotDelivery(ctx, id, target)
}
func (s *teamBotStore) AuthorizeBotDelivery(ctx context.Context, id store.BotID, target store.BotChannelBinding) error {
	return s.repository().AuthorizeBotDelivery(ctx, id, target)
}

// AuthorizeBotDelivery is repeated at the transport boundary. Neither an earlier
// claim nor an approved JSON snapshot substitutes for live destination authority.
func (r *botRepository) AuthorizeBotDelivery(ctx context.Context, id store.BotID, target store.BotChannelBinding) error {
	rec, err := r.publicationOwner(ctx, r.db, id)
	if err != nil {
		return err
	}
	if rec.Bot.Status != store.BotActive {
		return store.ErrBotForbidden
	}
	version, err := r.db.ReadVersion(ctx, id, rec.Bot.Version)
	if err != nil {
		return err
	}
	for _, binding := range version.Delivery.Bindings {
		if binding == target {
			return r.authorizeDeliveryIdentity(ctx, rec.Bot.OwnerID, target)
		}
	}
	return fmt.Errorf("%w: notification target is not in the approved version", store.ErrBotForbidden)
}
