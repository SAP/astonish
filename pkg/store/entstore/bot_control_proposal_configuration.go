package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
)

// Configuration consent includes pausing an active Bot. Never implicitly resume.
func (r *botRepository) applyControlConfigurationTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, p store.BotControlProposal, actor uuid.UUID) error {
	c := p.Payload.Configuration
	if c == nil {
		return store.ErrBotInvalid
	}
	seen := map[uuid.UUID]bool{}
	for _, b := range c.Delivery.Bindings {
		if b.ID == uuid.Nil || seen[b.ID] || strings.TrimSpace(b.Channel) == "" || strings.TrimSpace(b.ChatID) == "" {
			return store.ErrBotInvalid
		}
		seen[b.ID] = true
		if err := r.authorizeDeliveryIdentity(ctx, rec.Bot.OwnerID, b); err != nil {
			return err
		}
	}
	if (c.Autonomy.MaxSpendPerRunMinor > 0 || c.Autonomy.MaxSpendPerMonthMinor > 0) && strings.TrimSpace(c.Autonomy.SpendCurrency) == "" {
		return store.ErrBotInvalid
	}
	if rec.Bot.Status == store.BotActive {
		if !c.Pause {
			return store.ErrBotForbidden
		}
		next := rec.Bot
		next.Status = store.BotPaused
		next.Revision++
		next.UpdatedAt = r.clock()
		if err := tx.SetLifecycle(ctx, *rec, next); err != nil {
			return err
		}
		// SetLifecycle reserves one activity sequence.
		data, err := json.Marshal(map[string]any{"operation": "reviewed_configuration_pause", "proposal_id": p.ID, "actor_id": actor, "status": store.BotPaused})
		if err != nil {
			return err
		}
		if err = tx.InsertActivity(ctx, store.BotActivity{BotID: p.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: data, CreatedAt: r.clock()}, "control-pause:"+p.ID.String()); err != nil {
			return err
		}
		if err = tx.DisableBotSchedules(ctx, p.BotID); err != nil {
			return err
		}
		runs, err := tx.QuiesceBotRuns(ctx, p.BotID, false, r.clock())
		if err != nil {
			return err
		}
		for _, run := range runs {
			current, err := tx.GetBot(ctx, p.BotID)
			if err != nil {
				return err
			}
			if err = insertPublicationActivity(ctx, tx, current, run.ID, store.BotActivityReport, "control-pause-result:"+p.ID.String()+":"+run.ID.String(), map[string]any{"run_id": run.ID, "status": run.Status, "report": run.Report, "intent": store.BotOutboxPublish}, r.clock()); err != nil {
				return err
			}
			if err = appendBotRunStateTx(ctx, tx, current.Bot, run, current.LastSequence+1, r.clock()); err != nil {
				return err
			}
		}
		var err2 error
		rec, err2 = tx.GetBot(ctx, p.BotID)
		if err2 != nil {
			return err2
		}
	}
	switch rec.Bot.Status {
	case store.BotDraft, store.BotPaused, store.BotStopped:
	default:
		return store.ErrBotConflict
	}
	v, err := tx.ReadVersion(ctx, p.BotID, p.Version)
	if err != nil {
		return err
	}
	v.Autonomy = c.Autonomy
	v.Budget = c.Budget
	v.Delivery = c.Delivery
	return r.saveControlVersionTx(ctx, tx, rec, p, *v, actor)
}
