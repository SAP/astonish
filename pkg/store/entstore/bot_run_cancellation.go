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
	"github.com/SAP/astonish/pkg/store"
)

var _ store.BotRunCancellationStore = (*personalBotStore)(nil)
var _ store.BotRunCancellationStore = (*teamBotStore)(nil)

func (s *personalBotStore) CancelRun(ctx context.Context, id store.BotID, rid store.RunID) (*store.BotRun, error) {
	return s.repository().CancelRun(ctx, id, rid)
}
func (s *teamBotStore) CancelRun(ctx context.Context, id store.BotID, rid store.RunID) (*store.BotRun, error) {
	return s.repository().CancelRun(ctx, id, rid)
}

type botRunCancellationDB interface {
	SaveRunCancellation(context.Context, botStoredRecord, botStoredRun, store.RunStatus, time.Time) error
}

// CancelRun shares RequestRun's live member/run authorization. The transaction
// fences only a running target and retains its active-run barrier. Other queued
// work, approvals, lifecycle state and schedules are deliberately untouched.
func (r *botRepository) CancelRun(ctx context.Context, id store.BotID, rid store.RunID) (*store.BotRun, error) {
	if _, err := r.auth.authorize(ctx, store.BotID{}, true); err != nil {
		return nil, err
	}
	var result *store.BotRun
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, id)
		if err != nil {
			return err
		}
		if _, err = r.auth.authorize(ctx, rec.Bot.OwnerID, false); err != nil {
			return err
		}
		saved, err := tx.ReadRun(ctx, id, rid)
		if err != nil {
			return err
		}
		if err = r.cancelRunTx(ctx, tx, rec, saved); err != nil {
			return err
		}
		result = &saved.Run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// QuiesceSingleAutonomousRun preserves the active barrier until verified teardown.
// The caller must authenticate the owner and use the admission transaction.
func (r *botRepository) QuiesceSingleAutonomousRun(ctx context.Context, tx botDraftDB, rec *botStoredRecord) error {
	if rec.ActiveRunID == nil {
		return nil
	}
	saved, err := tx.ReadRun(ctx, rec.Bot.ID, *rec.ActiveRunID)
	if err != nil {
		return err
	}
	if saved.Run.Status != store.RunRunning || (saved.Run.Presentation == store.BotRunPresentationHomeInteractive && saved.Run.Trigger != store.TriggerCron) {
		return nil
	}
	if err = r.cancelRunTx(ctx, tx, rec, saved); err != nil {
		return err
	}
	rec.LastSequence++
	rec.Fence++
	return nil
}

func (r *botRepository) cancelRunTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, saved *botStoredRun) error {
	id, rid := rec.Bot.ID, saved.Run.ID
	previous := saved.Run.Status
	if previous.Terminal() || previous == store.RunInterrupted {
		return nil
	}
	switch previous {
	case store.RunQueued, store.RunWaitingApproval, store.RunRunning:
	default:
		return store.ErrBotConflict
	}
	if previous == store.RunRunning && (rec.ActiveRunID == nil || *rec.ActiveRunID != rid || rec.Fence != saved.Run.Lease.Fence) {
		return store.ErrBotConflict
	}
	now := r.clock()
	saved.Run.Status = store.RunCancelled
	saved.Run.FinishedAt = &now
	if previous == store.RunRunning {
		saved.Run.Status = store.RunInterrupted
		saved.Run.FinishedAt = nil
	}
	db, ok := tx.(botRunCancellationDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	if err := db.SaveRunCancellation(ctx, *rec, *saved, previous, now); err != nil {
		return err
	}
	if err := tx.RevokeRunApprovals(ctx, id, rid); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{"run_id": rid, "status": saved.Run.Status, "cancellation_requested": true})
	if err != nil {
		return err
	}
	if err = insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: id, RunID: &rid, Sequence: rec.LastSequence + 1, Kind: store.BotActivityRun, Payload: payload, CreatedAt: now}, fmt.Sprintf("cancel:%s", rid)); err != nil {
		return err
	}
	return nil
}

func (d personalBotDB) SaveRunCancellation(ctx context.Context, rec botStoredRecord, saved botStoredRun, previous store.RunStatus, now time.Time) error {
	root := d.client.Bot.Update().Where(personalbot.IDEQ(rec.Bot.ID), personalbot.RevisionEQ(rec.Bot.Revision), personalbot.FenceEQ(rec.Fence), personalbot.LastSeqEQ(rec.LastSequence)).AddLastSeq(1).SetUpdatedAt(now)
	if previous == store.RunRunning {
		root.Where(personalbot.ActiveRunIDEQ(saved.Run.ID)).AddFence(1)
	}
	n, err := root.Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	data, err := botJSONMap(saved)
	if err != nil {
		return err
	}
	n, err = d.client.BotRun.Update().Where(personalrun.IDEQ(saved.Run.ID), personalrun.BotIDEQ(rec.Bot.ID), personalrun.StatusEQ(personalrun.Status(previous)), personalrun.FenceEQ(saved.Run.Lease.Fence)).SetStatus(personalrun.Status(saved.Run.Status)).SetData(data).ClearLeaseExpiresAt().Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) SaveRunCancellation(ctx context.Context, rec botStoredRecord, saved botStoredRun, previous store.RunStatus, now time.Time) error {
	root := d.client.Bot.Update().Where(teambot.IDEQ(rec.Bot.ID), teambot.RevisionEQ(rec.Bot.Revision), teambot.FenceEQ(rec.Fence), teambot.LastSeqEQ(rec.LastSequence)).AddLastSeq(1).SetUpdatedAt(now)
	if previous == store.RunRunning {
		root.Where(teambot.ActiveRunIDEQ(saved.Run.ID)).AddFence(1)
	}
	n, err := root.Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	data, err := botJSONMap(saved)
	if err != nil {
		return err
	}
	n, err = d.client.BotRun.Update().Where(teamrun.IDEQ(saved.Run.ID), teamrun.BotIDEQ(rec.Bot.ID), teamrun.StatusEQ(teamrun.Status(previous)), teamrun.FenceEQ(saved.Run.Lease.Fence)).SetStatus(teamrun.Status(saved.Run.Status)).SetData(data).ClearLeaseExpiresAt().Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
