package entstore

import (
	"context"
	"encoding/json"
	"fmt"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

var _ store.BotInteractiveContinuationStore = (*personalBotStore)(nil)
var _ store.BotInteractiveContinuationStore = (*teamBotStore)(nil)

func (s *personalBotStore) RequireInteractiveContinuation(ctx context.Context, id store.BotID, rid store.RunID) (*store.BotRun, error) {
	return s.repository().RequireInteractiveContinuation(ctx, id, rid)
}
func (s *teamBotStore) RequireInteractiveContinuation(ctx context.Context, id store.BotID, rid store.RunID) (*store.BotRun, error) {
	return s.repository().RequireInteractiveContinuation(ctx, id, rid)
}

func safeInteractiveQueue(saved botStoredRun) bool {
	run := saved.Run
	return run.StartedAt == nil && run.Lease.Fence == 0 && run.Lease.WorkerID == "" && len(run.Checkpoint) == 0 && run.FinishedAt == nil && saved.CommitHash == "" && len(run.Report.Changed) == 0 && len(run.Report.Artifacts) == 0 && len(run.Report.Unverified) == 0
}

// RequireInteractiveContinuation is a pre-claim transition, never a lease release.
// Running work and active-run barriers must be recovered through normal fencing.
func (r *botRepository) RequireInteractiveContinuation(ctx context.Context, id store.BotID, rid store.RunID) (*store.BotRun, error) {
	var result *store.BotRun
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, id)
		if err != nil {
			return err
		}
		if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, false); err != nil {
			return err
		}
		saved, err := tx.ReadRun(ctx, id, rid)
		if err != nil {
			return err
		}
		run := &saved.Run
		if !run.IsInteractiveChat() || run.CallerCeiling == nil {
			return store.ErrBotForbidden
		}
		if rec.ActiveRunID != nil || rec.Bot.Version != run.Version || !interactiveStatus(*run, rec.Bot.Status) {
			return store.ErrBotConflict
		}
		if run.ContinuationReason == store.BotContinuationRequired && (run.Status == store.RunInterrupted || run.Status == store.RunWaitingApproval) {
			result = run
			return nil
		}
		previous := run.Status
		switch previous {
		case store.RunQueued:
			if !safeInteractiveQueue(*saved) {
				return store.ErrBotConflict
			}
			run.Status = store.RunInterrupted
		case store.RunWaitingApproval:
			c, err := botdomain.DecodeApprovalCheckpoint(run.Checkpoint, *run)
			if err != nil || c.Format != botdomain.InteractionCheckpointFormat || c.Fence != run.Lease.Fence {
				return store.ErrBotConflict
			}
		default:
			return store.ErrBotConflict
		}
		run.ContinuationReason = store.BotContinuationRequired
		db, ok := tx.(botRunCancellationDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		now := r.clock()
		if err := db.SaveRunCancellation(ctx, *rec, *saved, previous, now); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"run_id": rid, "status": run.Status, "reason": store.BotContinuationRequired})
		if err != nil {
			return err
		}
		if err := insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: id, RunID: &rid, Sequence: rec.LastSequence + 1, Kind: store.BotActivityRun, Payload: payload, CreatedAt: now}, fmt.Sprintf("continuation-required:%s:%d", rid, rec.LastSequence+1)); err != nil {
			return err
		}
		result = run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
