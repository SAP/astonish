package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotConfigurationStore = (*personalBotStore)(nil)
var _ store.BotConfigurationStore = (*teamBotStore)(nil)

func (s *personalBotStore) ConfigureReviewed(ctx context.Context, q store.BotConfigurationRequest) (*store.Bot, error) {
	return s.repository().ConfigureReviewed(ctx, q)
}
func (s *teamBotStore) ConfigureReviewed(ctx context.Context, q store.BotConfigurationRequest) (*store.Bot, error) {
	return s.repository().ConfigureReviewed(ctx, q)
}

// ConfigureReviewed is deliberately separate from ApplyVersion: prose edits must
// never become a policy update backdoor. Saving policy is never activation.
func (r *botRepository) ConfigureReviewed(ctx context.Context, q store.BotConfigurationRequest) (*store.Bot, error) {
	if !q.Reviewed {
		return nil, store.ErrBotForbidden
	}
	if q.BotID == uuid.Nil || q.ExpectedRevision < 1 || q.ExpectedVersion < 1 || strings.TrimSpace(q.Reason) == "" {
		return nil, store.ErrBotInvalid
	}
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	seen := map[uuid.UUID]bool{}
	for _, b := range q.Delivery.Bindings {
		if b.ID == uuid.Nil || seen[b.ID] || strings.TrimSpace(b.Channel) == "" || strings.TrimSpace(b.ChatID) == "" {
			return nil, store.ErrBotInvalid
		}
		seen[b.ID] = true
	}
	var result *store.Bot
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		var err error
		result, err = r.configureReviewedTx(ctx, tx, q)
		return err
	})
	return result, err
}

// configureReviewedTx shares the reviewed policy transaction with durable proposal receipts.
func (r *botRepository) configureReviewedTx(ctx context.Context, tx botDraftDB, q store.BotConfigurationRequest) (*store.Bot, error) {
	var result *store.Bot
	err := func() error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if rec.Bot.Revision != q.ExpectedRevision || rec.Bot.Version != q.ExpectedVersion {
			return store.ErrBotConflict
		}
		switch rec.Bot.Status {
		case store.BotDraft, store.BotPaused, store.BotStopped:
		default:
			return store.ErrBotConflict
		}
		for _, binding := range q.Delivery.Bindings {
			if err := r.authorizeDeliveryIdentity(ctx, rec.Bot.OwnerID, binding); err != nil {
				return err
			}
		}
		current, err := tx.ReadVersion(ctx, q.BotID, rec.Bot.Version)
		if err != nil {
			return err
		}
		v := *current
		v.Autonomy = q.Autonomy
		v.Budget = q.Budget
		v.Delivery = q.Delivery
		v.ChangeReason = q.Reason
		v.Version++
		v.CreatedBy = actor
		v.CreatedAt = r.clock()
		if err := botdomain.ValidateVersion(v); err != nil {
			return err
		}
		if (v.Autonomy.MaxSpendPerRunMinor > 0 || v.Autonomy.MaxSpendPerMonthMinor > 0) && strings.TrimSpace(v.Autonomy.SpendCurrency) == "" {
			return store.ErrBotInvalid
		}
		hash, err := botHash(v)
		if err != nil {
			return err
		}
		if err := tx.UpdateVersion(ctx, q.BotID, q.ExpectedRevision, v.Version, rec.LastSequence+1, v.CreatedAt); err != nil {
			return err
		}
		if err := tx.InsertVersion(ctx, v, hash); err != nil {
			return err
		}
		// Revoke pending and approved old-version action grants atomically. Retain
		// lifecycle state, fence, active shutdown barrier and disabled schedules.
		if err := tx.RevokeBotApprovals(ctx, q.BotID, true); err != nil {
			return err
		}
		if err := tx.DisableBotSchedules(ctx, q.BotID); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"operation": "reviewed_configuration", "version": v.Version, "actor_id": actor, "reason": q.Reason})
		if err != nil {
			return err
		}
		if err := tx.InsertActivity(ctx, store.BotActivity{BotID: q.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: payload, CreatedAt: v.CreatedAt}, fmt.Sprintf("configuration:%d", v.Version)); err != nil {
			return err
		}
		rec.Bot.Version = v.Version
		rec.Bot.Revision++
		rec.Bot.UpdatedAt = v.CreatedAt
		rec.Bot.Scope = r.auth.scope
		result = &rec.Bot
		return nil
	}()
	if err != nil {
		return nil, err
	}
	return result, nil
}
