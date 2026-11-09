package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// Priority pause is committed with capture, not left behind the source agent.
// The transition fences every active/queued lease, including the source. Once
// paused, autonomous admission is blocked; repeating pause must not cancel an
// explicitly admitted conversation. Exact-run cancel and Stop still revoke it.
func (s *botAuthorizedOperationRuntime) pauseRoutineTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, o *store.BotOperation) error {
	if rec.Bot.Status != store.BotActive && rec.Bot.Status != store.BotDraft && rec.Bot.Status != store.BotPaused {
		return store.ErrBotForbidden
	}
	if rec.Bot.Status == store.BotPaused {
		// Still enforce disabled schedules, but do not mutate lifecycle/fence,
		// approvals, or any queued/running owner conversation. Capture retains
		// the ordinary lease, mandate, provenance and authorization checks.
		if err := tx.DisableBotSchedules(ctx, rec.Bot.ID); err != nil {
			return err
		}
		o.Status = store.BotOperationApplied
		o.Revision++
		o.Receipt = &store.BotOperationReceipt{Status: o.Status, ObservedAt: s.repo.clock(), Postconditions: []store.BotOperationPostcondition{{Resource: RoutineControlResource(o.Payload.Operation), Revision: rec.Bot.Revision, Verified: true}}}
		return saveAuthorizedOperation(ctx, tx, rec, *o, s)
	}
	next := rec.Bot
	next.Status = store.BotPaused
	next.Revision++
	next.UpdatedAt = s.repo.clock()
	if err := tx.SetLifecycle(ctx, *rec, next); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"operation_id": o.ID, "status": next.Status, "cancel_run_id": rec.ActiveRunID})
	if err != nil {
		return err
	}
	if err = tx.InsertActivity(ctx, store.BotActivity{BotID: next.ID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: data, CreatedAt: s.repo.clock()}, "routine-pause:"+o.ID.String()); err != nil {
		return err
	}
	if err = tx.DisableBotSchedules(ctx, next.ID); err != nil {
		return err
	}
	runs, err := tx.QuiesceBotRuns(ctx, next.ID, false, s.repo.clock())
	if err != nil {
		return err
	}
	if err = tx.RevokeBotApprovals(ctx, next.ID, false); err != nil {
		return err
	}
	for _, run := range runs {
		fresh, e := tx.GetBot(ctx, next.ID)
		if e != nil {
			return e
		}
		if e = insertPublicationActivity(ctx, tx, fresh, run.ID, store.BotActivityReport, "routine-pause-result:"+o.ID.String()+":"+run.ID.String(), map[string]any{"run_id": run.ID, "status": run.Status, "report": run.Report, "intent": store.BotOutboxPublish}, s.repo.clock()); e != nil {
			return e
		}
	}
	o.Status = store.BotOperationApplied
	o.Revision++
	o.Receipt = &store.BotOperationReceipt{Status: o.Status, ObservedAt: s.repo.clock(), Postconditions: []store.BotOperationPostcondition{{Resource: RoutineControlResource(o.Payload.Operation), Revision: next.Revision, Verified: true}}}
	fresh, err := tx.GetBot(ctx, next.ID)
	if err != nil {
		return err
	}
	return saveAuthorizedOperation(ctx, tx, fresh, *o, s)
}

// Resume only the explicitly listed, current-version cron jobs. Never call the
// generic activation helper, which can enable unrelated historical schedules.
func (s *botAuthorizedOperationRuntime) resumeRoutineTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, o store.BotOperation) error {
	if rec.Bot.Status != store.BotPaused || o.Payload.ResumeTargets == nil {
		return store.ErrBotForbidden
	}
	d, ok := tx.(botTriggerDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	jobs := d.triggerScheduler()
	v, err := tx.ReadVersion(ctx, rec.Bot.ID, rec.Bot.Version)
	if err != nil {
		return err
	}
	if err = s.repo.validateControlScheduleBindings(ctx, jobs, rec.Bot, v.Triggers, v.Version); err != nil {
		return err
	}
	for _, id := range o.Payload.ResumeTargets.ScheduleIDs {
		found := false
		for _, tr := range v.Triggers {
			if tr.ScheduleID == nil || *tr.ScheduleID != id {
				continue
			}
			if !tr.Enabled || tr.Kind != store.TriggerCron {
				return store.ErrBotForbidden
			}
			// A resume never revives a legacy or expired task grant.
			raw, exists := rec.Metadata[scheduledTaskHead(tr.ID)]
			if !exists {
				return store.ErrBotForbidden
			}
			encoded, e := json.Marshal(raw)
			if e != nil {
				return e
			}
			var ref store.BotMandateRef
			if json.Unmarshal(encoded, &ref) != nil {
				return store.ErrBotForbidden
			}
			if _, _, e = s.verify(ctx, tx, ref, execution.BotMandatePurposeTask); e != nil {
				return e
			}
			job := jobs.Get(ctx, id.String())
			if job == nil {
				return store.ErrBotNotFound
			}
			// Existing mandate references are retained, not reissued or renewed.
			next, err := triggerNext(tr, s.repo.clock())
			if err != nil {
				return err
			}
			job.Enabled = true
			job.NextRun = &next
			if err = jobs.Update(ctx, job); err != nil {
				return err
			}
			found = true
		}
		if !found {
			return store.ErrBotNotFound
		}
	}
	next := rec.Bot
	next.Status = store.BotActive
	next.Revision++
	next.UpdatedAt = s.repo.clock()
	if err = tx.SetLifecycle(ctx, *rec, next); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"operation_id": o.ID, "status": next.Status, "schedule_ids": o.Payload.ResumeTargets.ScheduleIDs})
	if err != nil {
		return err
	}
	return tx.InsertActivity(ctx, store.BotActivity{BotID: next.ID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: data, CreatedAt: s.repo.clock()}, "routine-resume:"+o.ID.String())
}
