package entstore

import (
	"context"
	"encoding/json"
	"time"

	personalrun "github.com/SAP/astonish/ent/personal/botrun"
	teamrun "github.com/SAP/astonish/ent/team/botrun"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotRuntimeStore = (*personalBotStore)(nil)
var _ store.BotRuntimeStore = (*teamBotStore)(nil)
var _ store.BotWorkStore = (*personalBotStore)(nil)
var _ store.BotWorkStore = (*teamBotStore)(nil)

func (s *personalBotStore) LoadWork(ctx context.Context, l store.BotLease) (*store.BotWorkState, error) {
	return s.repository().LoadWork(ctx, l)
}
func (s *teamBotStore) LoadWork(ctx context.Context, l store.BotLease) (*store.BotWorkState, error) {
	return s.repository().LoadWork(ctx, l)
}
func (s *personalBotStore) SaveWork(ctx context.Context, q store.BotWorkUpdate) (*store.BotWorkState, error) {
	return s.repository().SaveWork(ctx, q)
}
func (s *teamBotStore) SaveWork(ctx context.Context, q store.BotWorkUpdate) (*store.BotWorkState, error) {
	return s.repository().SaveWork(ctx, q)
}

func (r *botRepository) LoadWork(ctx context.Context, l store.BotLease) (*store.BotWorkState, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	var result *store.BotWorkState
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		if _, err := r.checkLease(ctx, tx, l, r.clock()); err != nil {
			return err
		}
		run, err := tx.ReadRun(ctx, l.BotID, l.RunID)
		if err != nil {
			return err
		}
		if run.Work != nil {
			if err := run.Work.Validate(); err != nil {
				return err
			}
		}
		result = run.Work.Clone()
		return nil
	})
	return result, err
}

// updateWorkRun changes only the private envelope, never lease/lifecycle fields.
// The caller holds the same root write lock as transcript and lifecycle writes.
type botWorkDB interface {
	updateWorkRun(context.Context, botStoredRun, store.BotLease, time.Time) error
}

func (d personalBotDB) updateWorkRun(ctx context.Context, run botStoredRun, l store.BotLease, now time.Time) error {
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	n, err := d.client.BotRun.Update().Where(personalrun.IDEQ(l.RunID), personalrun.BotIDEQ(l.BotID), personalrun.StatusEQ(personalrun.StatusRunning), personalrun.FenceEQ(l.Fence), personalrun.WorkerIDEQ(l.WorkerID), personalrun.LeaseExpiresAtGT(now)).SetData(data).Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) updateWorkRun(ctx context.Context, run botStoredRun, l store.BotLease, now time.Time) error {
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	n, err := d.client.BotRun.Update().Where(teamrun.IDEQ(l.RunID), teamrun.BotIDEQ(l.BotID), teamrun.StatusEQ(teamrun.StatusRunning), teamrun.FenceEQ(l.Fence), teamrun.WorkerIDEQ(l.WorkerID), teamrun.LeaseExpiresAtGT(now)).SetData(data).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

func (r *botRepository) SaveWork(ctx context.Context, q store.BotWorkUpdate) (*store.BotWorkState, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	if q.ExpectedRevision < 1 {
		return nil, store.ErrBotInvalid
	}
	next := q.State.Clone()
	var result *store.BotWorkState
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		locker, ok := tx.(botTranscriptDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		writer, ok := tx.(botWorkDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		now := r.clock()
		if err := locker.fenceTranscript(ctx, q.Lease, now); err != nil {
			return err
		}
		if _, err := r.checkLease(ctx, tx, q.Lease, now); err != nil {
			return err
		}
		run, err := tx.ReadRun(ctx, q.Lease.BotID, q.Lease.RunID)
		if err != nil {
			return err
		}
		if run.Work == nil {
			return store.ErrBotUnsupported
		}
		if run.Work.Revision != q.ExpectedRevision {
			return store.ErrBotConflict
		}
		if next.Contract.BotID != q.Lease.BotID || next.Contract.RunID != q.Lease.RunID {
			return store.ErrBotForbidden
		}
		if err := store.ValidateBotWorkUpdate(run.Work, next); err != nil {
			return err
		}
		next.Revision++
		run.Work = next
		if err := writer.updateWorkRun(ctx, *run, q.Lease, now); err != nil {
			return err
		}
		result = next.Clone()
		return nil
	})
	return result, err
}

func (s *personalBotStore) LoadRunInput(ctx context.Context, id store.BotID, runID store.RunID) (*store.BotRunInput, error) {
	return s.repository().LoadRunInput(ctx, id, runID)
}

func (s *teamBotStore) LoadRunInput(ctx context.Context, id store.BotID, runID store.RunID) (*store.BotRunInput, error) {
	return s.repository().LoadRunInput(ctx, id, runID)
}

// LoadRunInput intentionally does not reuse viewer-authorized Get reads. The
// first runtime slice executes only as the authenticated accountable owner; it
// does not mint an owner principal from a viewer, dispatch ID, or service token.
func (r *botRepository) LoadRunInput(ctx context.Context, id store.BotID, runID store.RunID) (*store.BotRunInput, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return nil, err
	}
	var input store.BotRunInput
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, id)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		p, ok := execution.PrincipalFromContext(ctx)
		if !ok || actor != rec.Bot.OwnerID || p.Subject != rec.Bot.OwnerID.String() || rec.Bot.Scope != r.auth.scope {
			return store.ErrBotForbidden
		}
		saved, err := tx.ReadRun(ctx, id, runID)
		if err != nil {
			return err
		}
		switch saved.Run.Trigger {
		case store.TriggerManual, store.TriggerCron, store.TriggerGoalDeadline:
		case store.TriggerHook, store.TriggerWebhook, store.TriggerChannel:
			if !saved.ExternalAdmissionValidated || saved.Run.TriggerID == uuid.Nil {
				return store.ErrBotForbidden
			}
		default:
			return store.ErrBotUnsupported
		}
		if !saved.Run.Intent.AllowsStatus(rec.Bot.Status) || saved.Run.Status != store.RunQueued || saved.Run.Version != rec.Bot.Version {
			return store.ErrBotConflict
		}
		version, err := tx.ReadVersion(ctx, id, saved.Run.Version)
		if err != nil {
			return err
		}
		goals, err := tx.ReadGoals(ctx, id)
		if err != nil {
			return err
		}
		input = store.BotRunInput{Work: saved.Work.Clone(), Bot: rec.Bot, Version: *version, Run: saved.Run, Goals: goals, Instructions: saved.Instructions, Attachments: saved.Attachments, ExternalAdmissionValidated: saved.ExternalAdmissionValidated}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &input, nil
}
