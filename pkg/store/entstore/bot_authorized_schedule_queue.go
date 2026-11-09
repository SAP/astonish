package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	personalrun "github.com/SAP/astonish/ent/personal/botrun"
	teamrun "github.com/SAP/astonish/ent/team/botrun"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// This capability is used only after the schedule-version root CAS has acquired
// the transaction's write barrier. Claim/admission use that same root barrier.
type botScheduledQueueDB interface {
	saveScheduledQueueRun(context.Context, botStoredRun, int64) error
}

func (r *botRepository) reconcileScheduledQueueTx(ctx context.Context, tx botDraftDB, old, next store.BotVersion) error {
	after := uuid.Nil
	for {
		runs, err := tx.ReadRuns(ctx, next.BotID, 200, after)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.Status != store.RunQueued || run.Version >= next.Version || (run.Trigger != store.TriggerCron && run.Trigger != store.TriggerGoalDeadline) {
				continue
			}
			saved, err := tx.ReadRun(ctx, next.BotID, run.ID)
			if err != nil {
				return err
			}
			// Never repurpose an attempt that has started or held a lease, even if a
			// malformed/recovered record is marked queued.
			if saved.Run.StartedAt != nil || saved.Run.Lease.Fence != 0 || saved.Run.Lease.WorkerID != "" || !saved.Run.Lease.ExpiresAt.IsZero() {
				return store.ErrBotConflict
			}
			rebind := false
			if run.Version == old.Version {
				for _, before := range old.Triggers {
					if before.ID != run.TriggerID || before.Kind != run.Trigger || !before.Enabled || before.ScheduleID == nil || before.Instructions != saved.Instructions {
						continue
					}
					for _, current := range next.Triggers {
						if !reflect.DeepEqual(before, current) {
							continue
						}
						job := tx.(botTriggerDB).triggerScheduler().Get(ctx, current.ScheduleID.String())
						rebind = job != nil && job.Enabled && job.Payload.BotVersion == next.Version && job.Payload.BotID == next.BotID.String() && job.Payload.BotTriggerID == run.TriggerID.String()
					}
				}
			}
			if rebind {
				// Preserve the admitted occurrence, request hash, session, dispatch key,
				// mandate pin and caller ceiling. This is not admission or a new grant.
				saved.Run.Version = next.Version
			} else {
				now := r.clock()
				saved.Run.Status = store.RunCancelled
				saved.Run.FinishedAt = &now
				saved.Run.Report.Summary = "Queued occurrence cancelled because its schedule was superseded."
			}
			db, ok := tx.(botScheduledQueueDB)
			if !ok {
				return store.ErrBotUnsupported
			}
			if err = db.saveScheduledQueueRun(ctx, *saved, run.Version); err != nil {
				return err
			}
			rec, err := tx.GetBot(ctx, next.BotID)
			if err != nil {
				return err
			}
			if err = tx.AdvanceSequence(ctx, next.BotID, rec.LastSequence); err != nil {
				return err
			}
			payload, err := json.Marshal(map[string]any{"run_id": run.ID, "status": saved.Run.Status, "version": saved.Run.Version, "previous_version": run.Version, "schedule_reconciled": true})
			if err != nil {
				return err
			}
			if err = insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: next.BotID, RunID: &run.ID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityRun, Payload: payload, CreatedAt: r.clock()}, fmt.Sprintf("schedule-reconcile:%s:%d", run.ID, next.Version)); err != nil {
				return err
			}
		}
		if len(runs) < 200 {
			return nil
		}
		after = runs[len(runs)-1].ID
	}
}

func (d personalBotDB) saveScheduledQueueRun(ctx context.Context, saved botStoredRun, previous int64) error {
	data, err := botJSONMap(saved)
	if err != nil {
		return err
	}
	n, err := d.client.BotRun.Update().Where(personalrun.IDEQ(saved.Run.ID), personalrun.BotIDEQ(saved.Run.BotID), personalrun.VersionEQ(previous), personalrun.StatusEQ(personalrun.StatusQueued), personalrun.FenceEQ(0), personalrun.WorkerIDEQ(""), personalrun.LeaseExpiresAtIsNil()).SetVersion(saved.Run.Version).SetStatus(personalrun.Status(saved.Run.Status)).SetData(data).Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) saveScheduledQueueRun(ctx context.Context, saved botStoredRun, previous int64) error {
	data, err := botJSONMap(saved)
	if err != nil {
		return err
	}
	n, err := d.client.BotRun.Update().Where(teamrun.IDEQ(saved.Run.ID), teamrun.BotIDEQ(saved.Run.BotID), teamrun.VersionEQ(previous), teamrun.StatusEQ(teamrun.StatusQueued), teamrun.FenceEQ(0), teamrun.WorkerIDEQ(""), teamrun.LeaseExpiresAtIsNil()).SetVersion(saved.Run.Version).SetStatus(teamrun.Status(saved.Run.Status)).SetData(data).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
