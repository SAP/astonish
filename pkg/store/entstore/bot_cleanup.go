package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	personalbot "github.com/SAP/astonish/ent/personal/bot"
	personalrun "github.com/SAP/astonish/ent/personal/botrun"
	teambot "github.com/SAP/astonish/ent/team/bot"
	teamrun "github.com/SAP/astonish/ent/team/botrun"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotCleanupStore = (*personalBotStore)(nil)
var _ store.BotCleanupStore = (*teamBotStore)(nil)

func (s *personalBotStore) ConfirmCleanup(ctx context.Context, q store.BotCleanupRequest) error {
	return s.repository().ConfirmCleanup(ctx, q)
}

func (s *teamBotStore) ConfirmCleanup(ctx context.Context, q store.BotCleanupRequest) error {
	return s.repository().ConfirmCleanup(ctx, q)
}

// Optional DB extension keeps owner APIs and the shared transaction contract
// unchanged. Implementations below use only transaction-bound generated clients.
type botCleanupDB interface {
	CleanupHasBusyRuns(context.Context, uuid.UUID) (bool, error)
	SaveCleanup(context.Context, botStoredRecord, store.BotCleanupRequest, map[string]any, time.Time) error
}

func (r *botRepository) ConfirmCleanup(ctx context.Context, q store.BotCleanupRequest) error {
	p, ok := execution.PrincipalFromContext(ctx)
	verify, verifiedContext := store.BotCleanupVerifierFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindService || p.Subject != "" || p.Surface != execution.SurfaceCode || !p.HasScope(store.BotCleanupScope) || r.auth.orgSlug == "" || p.OrgSlug != r.auth.orgSlug || r.auth.teamSlug != "" && p.TeamSlug != r.auth.teamSlug || !verifiedContext {
		return store.ErrBotForbidden
	}
	if q.BotID == uuid.Nil || q.ExpectedRevision < 1 || q.Fence < 0 || q.Home.ID == uuid.Nil || q.Home.BotID != q.BotID || q.Home.Scope != r.auth.scope || q.Home.Revision < 1 {
		return store.ErrBotInvalid
	}
	switch q.Kind {
	case store.BotCleanupRunTeardown:
		if q.RunID == uuid.Nil || q.RunFence < 1 || q.RunFence >= q.Fence {
			return store.ErrBotInvalid
		}
	case store.BotCleanupHome:
		if q.RunID != uuid.Nil || q.RunFence != 0 {
			return store.ErrBotInvalid
		}
	default:
		return store.ErrBotInvalid
	}
	// No external callback while holding a DB transaction/lock. It attests
	// already-completed work; both preflight and postflight fail closed.
	rec, err := r.db.GetBot(ctx, q.BotID)
	if err != nil {
		return err
	}
	replay, err := r.checkCleanup(ctx, r.db, *rec, q)
	if err != nil || replay {
		return err
	}
	if err := verify(ctx, q); err != nil {
		return fmt.Errorf("verify bot cleanup: %w", err)
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		replay, err := r.checkCleanup(ctx, tx, *rec, q)
		if err != nil || replay {
			return err
		}
		data := make(map[string]any, len(rec.Metadata)+1)
		for k, v := range rec.Metadata {
			data[k] = v
		}
		receipt, err := botJSONMap(q)
		if err != nil {
			return err
		}
		data[botCleanupReceiptKey(q.Kind)] = receipt
		now := r.clock()
		if err := tx.(botCleanupDB).SaveCleanup(ctx, *rec, q, data, now); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"cleanup_confirmation": q, "runtime_client_id": p.ClientID, "physical_deletion_verified_by_store": false})
		if err != nil {
			return err
		}
		return tx.InsertActivity(ctx, store.BotActivity{BotID: q.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: payload, CreatedAt: now}, fmt.Sprintf("cleanup-confirmed:%s:%d:%d", q.Kind, q.ExpectedRevision, q.Fence))
	})
}

func botCleanupReceiptKey(kind store.BotCleanupKind) string {
	return "cleanup_receipt_" + string(kind)
}

func (r *botRepository) checkCleanup(ctx context.Context, db botDraftDB, rec botStoredRecord, q store.BotCleanupRequest) (bool, error) {
	cleanup, ok := db.(botCleanupDB)
	if !ok {
		return false, store.ErrBotUnsupported
	}
	if r.auth.scope == store.BotScopePersonal && (r.auth.userID == "" || rec.Bot.OwnerID.String() != r.auth.userID) {
		return false, store.ErrBotForbidden
	}
	// Restore and re-delete preserve historical metadata, but change revision and
	// fence and reset cleanup_complete. Historical receipts never bypass this.
	if rec.Bot.Revision != q.ExpectedRevision || rec.Fence != q.Fence || rec.Bot.Home != q.Home {
		return false, store.ErrBotConflict
	}
	switch rec.Bot.Status {
	case store.BotPaused, store.BotStopped, store.BotDeleted:
	case store.BotActive, store.BotDraft:
		// An active Bot may release only an exact cancellation barrier, never
		// retire its home or clear an unrelated interrupted attempt.
		if q.Kind != store.BotCleanupRunTeardown || q.Fence != q.RunFence+1 {
			return false, store.ErrBotConflict
		}
		if _, err := db.FindActivity(ctx, q.BotID, fmt.Sprintf("cancel:%s", q.RunID)); err != nil {
			return false, store.ErrBotConflict
		}
	default:
		return false, store.ErrBotConflict
	}
	if q.Kind == store.BotCleanupHome {
		if rec.Bot.Status != store.BotDeleted || rec.Bot.RestoreUntil == nil || rec.Bot.DeletedAt == nil || rec.Bot.RestoreUntil.After(r.clock()) {
			return false, store.ErrBotConflict
		}
		if rec.ActiveRunID != nil || rec.LeaseExpiresAt != nil {
			return false, store.ErrBotConflict
		}
	}
	if saved, ok := rec.Metadata[botCleanupReceiptKey(q.Kind)]; ok {
		var receipt store.BotCleanupRequest
		encoded, ok := saved.(map[string]any)
		if !ok {
			return false, store.ErrBotInvalid
		}
		if err := botDecode(encoded, &receipt); err != nil {
			return false, err
		}
		if receipt == q && rec.ActiveRunID == nil && rec.LeaseExpiresAt == nil {
			return true, nil
		}
	}
	if q.Kind == store.BotCleanupRunTeardown {
		if rec.ActiveRunID == nil || *rec.ActiveRunID != q.RunID {
			return false, store.ErrBotConflict
		}
		run, err := db.ReadRun(ctx, q.BotID, q.RunID)
		if err != nil {
			return false, err
		}
		if run.Run.Lease.Fence != q.RunFence || !(run.Run.Status.Terminal() || run.Run.Status == store.RunInterrupted) {
			return false, store.ErrBotConflict
		}
	}
	// Queued/waiting work cannot own physical compute, including a paused
	// conversation. The exact active-run CAS above is the admission barrier.
	// Stopped/deleted cleanup and home retirement retain their busy-work gates.
	if q.Kind == store.BotCleanupRunTeardown && (rec.Bot.Status == store.BotActive || rec.Bot.Status == store.BotDraft || rec.Bot.Status == store.BotPaused) {
		return false, nil
	}
	busy, err := cleanup.CleanupHasBusyRuns(ctx, q.BotID)
	if err != nil {
		return false, err
	}
	if busy {
		return false, store.ErrBotConflict
	}
	return false, nil
}

func (d personalBotDB) CleanupHasBusyRuns(ctx context.Context, id uuid.UUID) (bool, error) {
	return d.client.BotRun.Query().Where(personalrun.BotIDEQ(id), personalrun.StatusIn(personalrun.StatusQueued, personalrun.StatusRunning, personalrun.StatusWaitingApproval)).Exist(ctx)
}

func (d teamBotDB) CleanupHasBusyRuns(ctx context.Context, id uuid.UUID) (bool, error) {
	return d.client.BotRun.Query().Where(teamrun.BotIDEQ(id), teamrun.StatusIn(teamrun.StatusQueued, teamrun.StatusRunning, teamrun.StatusWaitingApproval)).Exist(ctx)
}

func (d personalBotDB) SaveCleanup(ctx context.Context, rec botStoredRecord, q store.BotCleanupRequest, data map[string]any, now time.Time) error {
	u := d.client.Bot.Update().Where(personalbot.IDEQ(q.BotID), personalbot.RevisionEQ(q.ExpectedRevision), personalbot.FenceEQ(q.Fence), personalbot.LastSeqEQ(rec.LastSequence), personalbot.StatusEQ(personalbot.Status(rec.Bot.Status))).SetData(data).AddLastSeq(1).SetUpdatedAt(now)
	if q.Kind == store.BotCleanupRunTeardown {
		u.Where(personalbot.ActiveRunIDEQ(q.RunID)).ClearActiveRunID().ClearLeaseExpiresAt().SetCleanupComplete(false)
	} else {
		u.Where(personalbot.StatusEQ(personalbot.StatusDeleted), personalbot.PurgeAfterLTE(now), personalbot.ActiveRunIDIsNil(), personalbot.LeaseExpiresAtIsNil()).SetCleanupComplete(true)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

func (d teamBotDB) SaveCleanup(ctx context.Context, rec botStoredRecord, q store.BotCleanupRequest, data map[string]any, now time.Time) error {
	u := d.client.Bot.Update().Where(teambot.IDEQ(q.BotID), teambot.RevisionEQ(q.ExpectedRevision), teambot.FenceEQ(q.Fence), teambot.LastSeqEQ(rec.LastSequence), teambot.StatusEQ(teambot.Status(rec.Bot.Status))).SetData(data).AddLastSeq(1).SetUpdatedAt(now)
	if q.Kind == store.BotCleanupRunTeardown {
		u.Where(teambot.ActiveRunIDEQ(q.RunID)).ClearActiveRunID().ClearLeaseExpiresAt().SetCleanupComplete(false)
	} else {
		u.Where(teambot.StatusEQ(teambot.StatusDeleted), teambot.PurgeAfterLTE(now), teambot.ActiveRunIDIsNil(), teambot.LeaseExpiresAtIsNil()).SetCleanupComplete(true)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
