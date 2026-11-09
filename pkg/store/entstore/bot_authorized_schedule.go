package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"strings"
	"time"
)

func (s *botAuthorizedOperationRuntime) scheduleCandidate(ctx context.Context, tx botDraftDB, rec *botStoredRecord, o store.BotOperation, limits store.BotMandateLimits) (*store.BotVersion, store.TriggerSpec, error) {
	v, err := tx.ReadVersion(ctx, rec.Bot.ID, rec.Bot.Version)
	if err != nil {
		return nil, store.TriggerSpec{}, err
	}
	// Copy through the immutable version JSON contract before changing slices.
	vcopy := *v
	vcopy.Triggers = append([]store.TriggerSpec(nil), v.Triggers...)
	v = &vcopy
	q := o.Payload.Schedule
	if q == nil {
		return nil, store.TriggerSpec{}, store.ErrBotInvalid
	}
	tr := q.Trigger
	index := -1
	for i, old := range v.Triggers {
		if old.ID == tr.ID {
			index = i
			break
		}
	}
	if o.Payload.Operation == store.BotControlScheduleCreate {
		if index >= 0 || tr.ID != uuid.Nil || tr.ScheduleID != nil {
			return nil, tr, store.ErrBotConflict
		}
		tr.ID = uuid.NewSHA1(o.ID, []byte("trigger"))
		id := uuid.NewSHA1(o.ID, []byte("schedule"))
		tr.ScheduleID = &id
	} else {
		if index < 0 {
			return nil, tr, store.ErrBotNotFound
		}
		old := v.Triggers[index]
		if old.Kind != store.TriggerCron || old.ScheduleID == nil || tr.ScheduleID == nil || *tr.ScheduleID != *old.ScheduleID {
			return nil, tr, store.ErrBotConflict
		}
	}
	switch o.Payload.Operation {
	case store.BotControlScheduleCreate, store.BotControlScheduleEdit:
		if tr.Kind != store.TriggerCron || strings.TrimSpace(tr.Instructions) == "" || len(tr.Instructions) > 65536 || tr.GoalID != nil || tr.Poll != nil || tr.CredentialRef != "" || tr.ChannelBinding != nil || tr.HookStatus != "" || tr.HookWatermark != "" {
			return nil, tr, store.ErrBotForbidden
		}
		if err = boundedSchedule(tr, s.repo.clock(), limits); err != nil {
			return nil, tr, err
		}
		tr.Fingerprint = triggerFingerprint(tr)
		if index < 0 {
			v.Triggers = append(v.Triggers, tr)
		} else {
			v.Triggers[index] = tr
		}
	case store.BotControlSchedulePause:
		tr = v.Triggers[index]
		tr.Enabled = false
		v.Triggers[index] = tr
	case store.BotControlScheduleRemove:
		tr = v.Triggers[index]
		v.Triggers = append(v.Triggers[:index], v.Triggers[index+1:]...)
	default:
		return nil, tr, store.ErrBotForbidden
	}
	count := int64(0)
	for _, t := range v.Triggers {
		if t.ScheduleID != nil {
			count++
		}
	}
	if count > limits.MaxSchedules {
		return nil, tr, store.ErrBotForbidden
	}
	v.Version++
	v.CreatedBy = rec.Bot.OwnerID
	v.CreatedAt = s.repo.clock()
	v.ChangeReason = o.Payload.Reason
	if err = botdomain.ValidateVersion(*v); err != nil {
		return nil, tr, err
	}
	return v, tr, nil
}

// Check a full leap-year horizon, including cron unions and timezone/DST changes.
// Daily and minimum-interval limits are admission ceilings; execution quota is
// separately enforced by the mandate runtime at each dispatch.
func boundedSchedule(tr store.TriggerSpec, now time.Time, l store.BotMandateLimits) error {
	if l.Validate() != nil {
		return store.ErrBotForbidden
	}
	until := now.AddDate(1, 0, 1)
	prev, err := triggerNext(tr, now)
	if err != nil {
		return err
	}
	day := ""
	count := int64(0)
	for i := 0; i < 600000; i++ {
		if prev.After(until) {
			return nil
		}
		d := prev.UTC().Format("2006-01-02")
		if d != day {
			day = d
			count = 0
		}
		count++
		if count > l.MaxDailyRuns {
			return store.ErrBotForbidden
		}
		next, err := triggerNext(tr, prev)
		if err != nil {
			return err
		}
		if !next.After(prev) || next.Sub(prev).Seconds() < float64(l.MinScheduleIntervalSeconds) {
			return store.ErrBotForbidden
		}
		prev = next
	}
	return store.ErrBotForbidden
}

func (s *botAuthorizedOperationRuntime) ApplyAuthorizedOperation(ctx context.Context, ref store.BotMandateRef, id uuid.UUID) (*store.BotOperation, error) {
	before, err := s.repo.mandateScope(ctx, s.repo.db, ref)
	if err != nil {
		return nil, err
	}
	op, err := readMandateOperation(ctx, s.repo.db, before, ref, id)
	if err != nil {
		return nil, err
	}
	if routineOperation(op.Payload.Operation) {
		return s.applyRoutineOperation(ctx, ref, id)
	}
	if err = verifyAuthorizedGrant(ctx, s.repo.db, *op); err != nil {
		return nil, err
	}
	if op.Status == store.BotOperationApplied {
		return op, nil
	}
	if op.Status != store.BotOperationPending {
		return op, store.ErrBotConflict
	}
	m, _, source, p, err := s.source(ctx, s.repo.db, ref, op.Payload)
	if err != nil {
		return nil, err
	}
	if source.Run.ID != op.Provenance.SourceRunID || p.SourceDigest != op.Provenance.SourceDigest {
		return nil, store.ErrBotForbidden
	}
	if before.ActiveRunID != nil || !source.Run.Status.Terminal() {
		return op, store.ErrBotBusy
	}
	if before.Bot.Version != op.Provenance.Version || before.Bot.Revision != op.Provenance.ExpectedRevision {
		return op, store.ErrBotConflict
	}
	candidate, target, err := s.scheduleCandidate(ctx, s.repo.db, before, *op, m.Mandate.Limits)
	if err != nil {
		return nil, err
	}
	if err = s.readiness(ctx, before.Bot, *candidate); err != nil {
		return op, err
	}
	var result *store.BotOperation
	err = s.repo.db.Transaction(ctx, func(tx botDraftDB) error {
		current, rec, run, prov, err := s.source(ctx, tx, ref, op.Payload)
		if err != nil {
			return err
		}
		saved, err := readMandateOperation(ctx, tx, rec, ref, id)
		if err != nil {
			return err
		}
		if err = verifyAuthorizedGrant(ctx, tx, *saved); err != nil {
			return err
		}
		if saved.Status == store.BotOperationApplied {
			result = saved
			return nil
		}
		if !reflect.DeepEqual(saved, op) || !reflect.DeepEqual(rec.Bot, before.Bot) || !reflect.DeepEqual(current, m) || prov != p {
			return store.ErrBotConflict
		}
		if rec.ActiveRunID != nil || !run.Run.Status.Terminal() {
			return store.ErrBotBusy
		}
		next, tr, err := s.scheduleCandidate(ctx, tx, rec, *saved, current.Mandate.Limits)
		if err != nil {
			return err
		}
		// Time is metadata, not configuration; readiness is fenced on all other fields.
		next.CreatedAt = candidate.CreatedAt
		if !reflect.DeepEqual(next, candidate) || !reflect.DeepEqual(target, tr) {
			return store.ErrBotConflict
		}
		if err = s.applyAuthorizedScheduleTx(ctx, tx, rec, *saved, *next, tr); err != nil {
			return err
		}
		rec, err = tx.GetBot(ctx, ref.BotID)
		if err != nil {
			return err
		}
		if err = s.issueScheduledTask(ctx, tx, rec, *saved, tr, *current); err != nil {
			return err
		}
		receipt := store.BotOperationScheduleReceipt{TriggerID: tr.ID, ScheduleID: *tr.ScheduleID, Version: next.Version, Removed: saved.Payload.Operation == store.BotControlScheduleRemove, SourceRunID: saved.Provenance.SourceRunID, UserEventID: saved.Provenance.UserEventID, SourceDigest: saved.Provenance.SourceDigest}
		jobs := tx.(botTriggerDB).triggerScheduler()
		job := jobs.Get(ctx, tr.ScheduleID.String())
		if receipt.Removed {
			if job != nil {
				return store.ErrBotConflict
			}
		} else {
			if job == nil || job.Payload.BotVersion != next.Version {
				return store.ErrBotConflict
			}
			receipt.Enabled = job.Enabled
			receipt.NextRun = job.NextRun
		}
		saved.Status = store.BotOperationApplied
		saved.Revision++
		saved.Receipt = &store.BotOperationReceipt{Status: store.BotOperationApplied, ObservedAt: s.repo.clock(), Schedule: &receipt, Postconditions: []store.BotOperationPostcondition{{Resource: "schedule:" + receipt.ScheduleID.String(), Revision: next.Version, Verified: true}}}
		if err = saved.Validate(); err != nil {
			return err
		}
		if err = saveAuthorizedOperation(ctx, tx, rec, *saved, s); err != nil {
			return err
		}
		// Existing publication pipeline observes this intent only after the transaction
		// commits and deduplicates by source run + sequence. No external delivery added.
		summary := fmt.Sprintf("Schedule %s applied (trigger %s, version %d; enabled=%t, removed=%t).", saved.Payload.Operation, receipt.TriggerID, receipt.Version, receipt.Enabled, receipt.Removed)
		report := store.BotReport{Summary: summary}
		if err = insertPublicationActivity(ctx, tx, rec, run.Run.ID, store.BotActivityReport, "authorized-schedule-receipt:"+id.String(), map[string]any{"run_id": run.Run.ID, "status": run.Run.Status, "report": report, "operation_id": id, "operation_revision": saved.Revision, "intent": store.BotOutboxPublish}, s.repo.clock()); err != nil {
			return err
		}
		result = saved
		return nil
	})
	return result, err
}

func (s *botAuthorizedOperationRuntime) applyAuthorizedScheduleTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, o store.BotOperation, v store.BotVersion, target store.TriggerSpec) error {
	schedules, ok := tx.(botTriggerDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	jobs := schedules.triggerScheduler()
	old, err := tx.ReadVersion(ctx, rec.Bot.ID, rec.Bot.Version)
	if err != nil {
		return err
	}
	// Reserve the scoped root before any deterministic scheduler insert/update.
	// Concurrent PostgreSQL readers may both see a missing job; the CAS loser
	// must return ErrBotConflict before it can collide with the winner's insert.
	cas, ok := tx.(botControlVersionDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	hash, err := botHash(v)
	if err != nil {
		return err
	}
	if err = cas.updateControlVersion(ctx, *rec, v.Version, v.CreatedAt); err != nil {
		return err
	}
	// Never reconcile unrelated jobs: only rebind their version. In particular do
	// not recompute NextRun or derive Enabled from the declarative trigger flag.
	for _, tr := range old.Triggers {
		if tr.ScheduleID == nil {
			continue
		}
		job := jobs.Get(ctx, tr.ScheduleID.String())
		if job == nil || job.Mode != "bot_trigger" || job.OwnerID != o.MandateRef.OwnerID || job.TeamSlug != o.MandateRef.TeamSlug || job.Payload.BotID != rec.Bot.ID.String() || job.Payload.BotTriggerID != tr.ID.String() || job.Payload.BotVersion != rec.Bot.Version {
			return store.ErrBotConflict
		}
		if tr.ID == target.ID {
			continue
		}
		job.Payload.BotVersion = v.Version
		if err = jobs.Update(ctx, job); err != nil {
			return err
		}
	}
	if o.Payload.Operation == store.BotControlScheduleRemove {
		if err = jobs.Remove(ctx, target.ScheduleID.String()); err != nil {
			return err
		}
	} else {
		job := jobs.Get(ctx, target.ScheduleID.String())
		exists := job != nil
		if o.Payload.Operation == store.BotControlScheduleCreate && exists {
			return store.ErrBotConflict
		}
		if !exists {
			job = &store.ScheduledJob{ID: target.ScheduleID.String(), Name: "bot-" + rec.Bot.ID.String() + "-" + target.ID.String(), Mode: "bot_trigger", OwnerID: o.MandateRef.OwnerID, TeamSlug: o.MandateRef.TeamSlug}
		}
		job.Payload = store.JobPayload{BotID: rec.Bot.ID.String(), BotVersion: v.Version, BotTriggerID: target.ID.String()}
		job.Schedule = store.JobSchedule{Cron: target.Cron, Timezone: target.Timezone}
		// A draft may activate only for this explicitly authorized enabled target.
		active := rec.Bot.Status == store.BotActive || rec.Bot.Status == store.BotDraft
		job.Enabled = target.Enabled && active
		job.NextRun = nil
		if job.Enabled {
			next, err := triggerNext(target, s.repo.clock())
			if err != nil {
				return err
			}
			job.NextRun = &next
		}
		if !exists {
			if err = jobs.Add(ctx, job); err != nil {
				return err
			}
		}
		if err = jobs.Update(ctx, job); err != nil {
			return err
		}
	}
	if err = tx.InsertVersion(ctx, v, hash); err != nil {
		return err
	}
	// The version CAS reserved a sequence. Record it before further root mutations.
	data, err := json.Marshal(map[string]any{"operation_id": o.ID, "version": v.Version})
	if err != nil {
		return err
	}
	if err = tx.InsertActivity(ctx, store.BotActivity{BotID: rec.Bot.ID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: data, CreatedAt: s.repo.clock()}, "authorized-schedule-version:"+o.ID.String()); err != nil {
		return err
	}
	if rec.Bot.Status == store.BotDraft && target.Enabled && o.Payload.Operation != store.BotControlScheduleRemove {
		updated, err := tx.GetBot(ctx, rec.Bot.ID)
		if err != nil {
			return err
		}
		next := updated.Bot
		next.Status = store.BotActive
		next.Revision++
		next.UpdatedAt = s.repo.clock()
		if err = tx.SetLifecycle(ctx, *updated, next); err != nil {
			return err
		}
		if err = tx.InsertActivity(ctx, store.BotActivity{BotID: rec.Bot.ID, Sequence: updated.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: data, CreatedAt: s.repo.clock()}, "authorized-schedule-activation:"+o.ID.String()); err != nil {
			return err
		}
	}
	if err = s.repo.reconcileScheduledQueueTx(ctx, tx, *old, v); err != nil {
		return err
	}
	return tx.RevokeBotApprovals(ctx, rec.Bot.ID, true)
}
