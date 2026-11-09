package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
)

// All helpers consume the caller's scoped transaction; none opens another one.
func (r *botRepository) saveControlVersionTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, p store.BotControlProposal, v store.BotVersion, actor uuid.UUID) error {
	old, err := tx.ReadVersion(ctx, p.BotID, p.Version)
	if err != nil {
		return err
	}
	v.Version = p.Version + 1
	v.CreatedBy, v.CreatedAt, v.ChangeReason = actor, r.clock(), p.Payload.Reason
	if err = botdomain.ValidateVersion(v); err != nil {
		return err
	}
	db, ok := tx.(botControlVersionDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	schedules, ok := tx.(botTriggerDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	if err = r.validateControlScheduleBindings(ctx, schedules.triggerScheduler(), rec.Bot, old.Triggers, p.Version); err != nil {
		return err
	}
	hash, err := botHash(v)
	if err != nil {
		return err
	}
	if err = db.updateControlVersion(ctx, *rec, v.Version, v.CreatedAt); err != nil {
		return err
	}
	if err = tx.InsertVersion(ctx, v, hash); err != nil {
		return err
	}
	jobs := schedules.triggerScheduler()
	unchanged := map[string]*store.ScheduledJob{}
	for _, previous := range old.Triggers {
		if previous.ScheduleID == nil {
			continue
		}
		for _, next := range v.Triggers {
			if next.ID == previous.ID && next.Enabled == previous.Enabled && triggerFingerprint(next) == triggerFingerprint(previous) {
				unchanged[previous.ScheduleID.String()] = jobs.Get(ctx, previous.ScheduleID.String())
			}
		}
	}
	if err = r.syncTriggerSchedules(ctx, tx, jobs, rec.Bot, v, old.Triggers); err != nil {
		return err
	}
	// Version-only reconciliation must not skip a due occurrence or rearm an
	// already consumed one-shot. Cron edits alone recompute their next run.
	for id, previous := range unchanged {
		current := jobs.Get(ctx, id)
		if previous != nil && current != nil && previous.Enabled == current.Enabled {
			current.NextRun = previous.NextRun
			if err = jobs.Update(ctx, current); err != nil {
				return err
			}
		}
	}
	if err = tx.RevokeBotApprovals(ctx, p.BotID, true); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"operation": p.Payload.Operation, "proposal_id": p.ID, "version": v.Version, "actor_id": actor})
	if err != nil {
		return err
	}
	return tx.InsertActivity(ctx, store.BotActivity{BotID: p.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: data, CreatedAt: v.CreatedAt}, fmt.Sprintf("control-version:%s", p.ID))
}

// Never repair or overwrite an unrelated scheduler job during proposal review.
func (r *botRepository) validateControlScheduleBindings(ctx context.Context, jobs store.SchedulerStore, b store.Bot, triggers []store.TriggerSpec, version int64) error {
	for _, tr := range triggers {
		if tr.ScheduleID == nil {
			continue
		}
		// Personal repositories are org/user scoped and have no fixed team.
		// Match the same authenticated routing evidence used when binding a
		// schedule; never substitute ambient tenant context or skip this fence.
		teamSlug := r.auth.teamSlug
		if r.auth.scope == store.BotScopePersonal {
			principal, ok := execution.PrincipalFromContext(ctx)
			if !ok || principal.OrgSlug != r.auth.orgSlug || principal.TeamSlug == "" {
				return store.ErrBotForbidden
			}
			teamSlug = principal.TeamSlug
		}
		job := jobs.Get(ctx, tr.ScheduleID.String())
		if job == nil || job.Mode != "bot_trigger" || job.OwnerID != b.OwnerID.String() || job.TeamSlug == "" || job.TeamSlug != teamSlug || job.Payload.BotID != b.ID.String() || job.Payload.BotTriggerID != tr.ID.String() || job.Payload.BotVersion != version {
			return store.ErrBotConflict
		}
	}
	return nil
}

func (r *botRepository) applyControlScheduleTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, p store.BotControlProposal, actor uuid.UUID) error {
	q := p.Payload.Schedule
	if q == nil || q.ExpectedRevision != rec.Bot.Revision {
		return store.ErrBotConflict
	}
	v, err := tx.ReadVersion(ctx, p.BotID, p.Version)
	if err != nil {
		return err
	}
	tr := q.Trigger
	index := -1
	for i, old := range v.Triggers {
		if old.ID == tr.ID {
			index = i
			break
		}
	}
	if p.Payload.Operation == store.BotControlScheduleCreate {
		if index >= 0 {
			return store.ErrBotConflict
		}
		if tr.ID == uuid.Nil {
			tr.ID = uuid.NewSHA1(p.ID, []byte("trigger"))
		}
	} else if index < 0 {
		return store.ErrBotNotFound
	}
	if index >= 0 {
		old := v.Triggers[index]
		if old.Kind != store.TriggerCron {
			return store.ErrBotUnsupported
		}
		if tr.ScheduleID == nil || old.ScheduleID == nil || *tr.ScheduleID != *old.ScheduleID {
			return store.ErrBotConflict
		}
	}
	switch p.Payload.Operation {
	case store.BotControlScheduleCreate, store.BotControlScheduleEdit:
		if tr.Kind != store.TriggerCron {
			return store.ErrBotUnsupported
		}
		if strings.TrimSpace(tr.Instructions) == "" || len(tr.Instructions) > 65536 {
			return store.ErrBotInvalid
		}
		if tr.GoalID != nil || tr.Poll != nil || tr.CredentialRef != "" || tr.ChannelBinding != nil || tr.HookStatus != "" || tr.HookWatermark != "" {
			return store.ErrBotForbidden
		}
		if _, err = triggerNext(tr, r.clock()); err != nil {
			return err
		}
		if index < 0 {
			if tr.ScheduleID != nil {
				return store.ErrBotForbidden
			}
			id := uuid.NewSHA1(p.ID, []byte("schedule"))
			schedules, ok := tx.(botTriggerDB)
			if !ok {
				return store.ErrBotUnsupported
			}
			if schedules.triggerScheduler().Get(ctx, id.String()) != nil {
				return store.ErrBotConflict
			}
			tr.ScheduleID = &id
		}
		tr.Fingerprint = triggerFingerprint(tr)
		if index < 0 {
			v.Triggers = append(v.Triggers, tr)
		} else {
			v.Triggers[index] = tr
		}
	case store.BotControlSchedulePause:
		v.Triggers[index].Enabled = false
	case store.BotControlScheduleRemove:
		v.Triggers = append(v.Triggers[:index], v.Triggers[index+1:]...)
	default:
		return store.ErrBotUnsupported
	}
	return r.saveControlVersionTx(ctx, tx, rec, p, *v, actor)
}

func (r *botRepository) applyControlGoalTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, p store.BotControlProposal, actor uuid.UUID) error {
	q := p.Payload.Goal
	if q == nil {
		return store.ErrBotInvalid
	}
	g := q.Goal
	deadlineChanged := p.Payload.Operation == store.BotControlGoalCreate
	if g.BotID != p.BotID || strings.TrimSpace(g.Title) == "" || strings.TrimSpace(g.CompletionCriteria) == "" {
		return store.ErrBotInvalid
	}
	if g.CompletedAt != nil || len(g.Evidence) > 0 || (g.Status != "" && g.Status != store.BotGoalActive) {
		return store.ErrBotForbidden
	}
	if p.Payload.Operation == store.BotControlGoalCreate {
		if q.ExpectedRevision != 0 || g.ID != uuid.Nil {
			return store.ErrBotInvalid
		}
		g.ID = uuid.NewSHA1(p.ID, []byte("goal"))
		g.Revision = 1
		g.Status = store.BotGoalActive
		g.WorkspacePath = "goals/" + g.ID.String()
	} else {
		old, err := tx.ReadGoal(ctx, p.BotID, g.ID)
		if err != nil {
			return err
		}
		if old.Revision != q.ExpectedRevision {
			return store.ErrBotConflict
		}
		if old.Status != store.BotGoalActive {
			return store.ErrBotForbidden
		}
		deadlineChanged = (old.DueAt == nil) != (g.DueAt == nil) || (old.DueAt != nil && g.DueAt != nil && !old.DueAt.Equal(*g.DueAt))
		g.Revision = old.Revision + 1
		g.WorkspacePath = old.WorkspacePath
		g.Status = old.Status
	}
	// Root CAS also protects the no-active-run review barrier and serializes decisions.
	db, ok := tx.(botControlVersionDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	if err := db.updateControlVersion(ctx, *rec, rec.Bot.Version, r.clock()); err != nil {
		return err
	}
	var err error
	if p.Payload.Operation == store.BotControlGoalCreate {
		err = tx.InsertGoal(ctx, g)
	} else {
		err = tx.UpdateGoal(ctx, g, q.ExpectedRevision)
	}
	if err != nil {
		return err
	}
	v, err := tx.ReadVersion(ctx, p.BotID, p.Version)
	if err != nil {
		return err
	}
	schedules, ok := tx.(botTriggerDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	if err = r.validateControlScheduleBindings(ctx, schedules.triggerScheduler(), rec.Bot, v.Triggers, p.Version); err != nil {
		return err
	}
	// Reconcile only deadline jobs for this goal; unrelated next-run times stay intact.
	deadline := *v
	deadline.Triggers = nil
	for _, tr := range v.Triggers {
		if deadlineChanged && tr.Kind == store.TriggerGoalDeadline && tr.GoalID != nil && *tr.GoalID == g.ID {
			deadline.Triggers = append(deadline.Triggers, tr)
		}
	}
	if err = r.syncTriggerSchedules(ctx, tx, schedules.triggerScheduler(), rec.Bot, deadline, nil); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"operation": p.Payload.Operation, "proposal_id": p.ID, "goal_id": g.ID, "revision": g.Revision, "actor_id": actor})
	if err != nil {
		return err
	}
	return tx.InsertActivity(ctx, store.BotActivity{BotID: p.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityCorrection, Payload: data, CreatedAt: r.clock()}, "control-goal:"+p.ID.String())
}
