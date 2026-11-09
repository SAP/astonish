package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const botHomeCleanupClaimKey = "home_cleanup_claim"

var _ store.BotHomeCleanupStore = (*personalBotStore)(nil)
var _ store.BotHomeCleanupStore = (*teamBotStore)(nil)

func (s *personalBotStore) ReserveHomeCleanup(ctx context.Context, home store.BotHomeBinding, revision int64) (*store.BotHomeCleanupClaim, error) {
	return s.repository().ReserveHomeCleanup(ctx, home, revision)
}
func (s *teamBotStore) ReserveHomeCleanup(ctx context.Context, home store.BotHomeBinding, revision int64) (*store.BotHomeCleanupClaim, error) {
	return s.repository().ReserveHomeCleanup(ctx, home, revision)
}

type botHomeCleanupDB interface {
	SaveHomeCleanupClaim(context.Context, botStoredRecord, map[string]any, time.Time) error
}

func (r *botRepository) ReserveHomeCleanup(ctx context.Context, home store.BotHomeBinding, revision int64) (*store.BotHomeCleanupClaim, error) {
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindService || p.Subject != "" || p.Surface != execution.SurfaceCode || !p.HasScope(store.BotCleanupScope) || r.auth.orgSlug == "" || p.OrgSlug != r.auth.orgSlug || r.auth.teamSlug != "" && p.TeamSlug != r.auth.teamSlug {
		return nil, store.ErrBotForbidden
	}
	if home.ID == uuid.Nil || home.BotID == uuid.Nil || home.Scope != r.auth.scope || home.Revision < 1 || revision < 1 {
		return nil, store.ErrBotInvalid
	}
	var result *store.BotHomeCleanupClaim
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		db, ok := tx.(botHomeCleanupDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		rec, err := tx.GetBot(ctx, home.BotID)
		if err != nil {
			return err
		}
		q := store.BotCleanupRequest{Kind: store.BotCleanupHome, BotID: home.BotID, ExpectedRevision: revision, Fence: rec.Fence, Home: home}
		if _, err := r.checkCleanup(ctx, tx, *rec, q); err != nil {
			return err
		}
		claim := store.BotHomeCleanupClaim{Request: q, OwnerID: rec.Bot.OwnerID.String(), OrgSlug: r.auth.orgSlug, TeamSlug: r.auth.teamSlug}
		// Personal stores are user-routed but home bindings also include the trusted
		// team used at provisioning. Prefer the routed team when available.
		if claim.TeamSlug == "" {
			claim.TeamSlug = p.TeamSlug
		}
		if saved, exists := rec.Metadata[botHomeCleanupClaimKey]; exists {
			raw, ok := saved.(string)
			if !ok {
				return store.ErrBotInvalid
			}
			var previous store.BotHomeCleanupClaim
			if err := json.Unmarshal([]byte(raw), &previous); err != nil {
				return store.ErrBotInvalid
			}
			if previous != claim {
				return store.ErrBotConflict
			}
			result = &previous
			return nil
		}
		raw, err := json.Marshal(claim)
		if err != nil {
			return err
		}
		data := make(map[string]any, len(rec.Metadata)+1)
		for k, v := range rec.Metadata {
			data[k] = v
		}
		data[botHomeCleanupClaimKey] = string(raw)
		now := r.clock()
		if err := db.SaveHomeCleanupClaim(ctx, *rec, data, now); err != nil {
			return err
		}
		if err := tx.InsertActivity(ctx, store.BotActivity{BotID: home.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: raw, CreatedAt: now}, fmt.Sprintf("home-cleanup-claimed:%d:%d", revision, rec.Fence)); err != nil {
			return err
		}
		result = &claim
		return nil
	})
	return result, err
}

func (d personalBotDB) SaveHomeCleanupClaim(ctx context.Context, rec botStoredRecord, data map[string]any, now time.Time) error {
	n, err := d.client.Bot.Update().Where(personalbot.IDEQ(rec.Bot.ID), personalbot.RevisionEQ(rec.Bot.Revision), personalbot.FenceEQ(rec.Fence), personalbot.LastSeqEQ(rec.LastSequence), personalbot.StatusEQ(personalbot.StatusDeleted), personalbot.PurgeAfterLTE(now), personalbot.ActiveRunIDIsNil(), personalbot.LeaseExpiresAtIsNil()).SetData(data).AddLastSeq(1).SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) SaveHomeCleanupClaim(ctx context.Context, rec botStoredRecord, data map[string]any, now time.Time) error {
	n, err := d.client.Bot.Update().Where(teambot.IDEQ(rec.Bot.ID), teambot.RevisionEQ(rec.Bot.Revision), teambot.FenceEQ(rec.Fence), teambot.LastSeqEQ(rec.LastSequence), teambot.StatusEQ(teambot.StatusDeleted), teambot.PurgeAfterLTE(now), teambot.ActiveRunIDIsNil(), teambot.LeaseExpiresAtIsNil()).SetData(data).AddLastSeq(1).SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
