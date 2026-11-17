package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const progressRecordKind store.BotActivityKind = "progress_record"

// Only compact cursors live on the run. Immutable, call-bound records live in the
// existing scoped outbox and are removed by the existing Bot purge transaction.
type botProgressDispatch struct {
	Request    store.BotProgressBeginDispatchRequest
	Receipt    store.BotProgressDispatchReceipt
	Settlement *store.BotProgressSettleDispatchRequest
}

var _ store.BotProgressStore = (*personalBotStore)(nil)
var _ store.BotProgressStore = (*teamBotStore)(nil)

func (s *personalBotStore) CheckpointProgress(c context.Context, q store.BotProgressCheckpointRequest) (*store.BotProgressState, error) {
	return s.repository().CheckpointProgress(c, q)
}
func (s *teamBotStore) CheckpointProgress(c context.Context, q store.BotProgressCheckpointRequest) (*store.BotProgressState, error) {
	return s.repository().CheckpointProgress(c, q)
}
func (s *personalBotStore) BeginProgressDispatch(c context.Context, q store.BotProgressBeginDispatchRequest) (*store.BotProgressDispatchReceipt, error) {
	return s.repository().BeginProgressDispatch(c, q)
}
func (s *teamBotStore) BeginProgressDispatch(c context.Context, q store.BotProgressBeginDispatchRequest) (*store.BotProgressDispatchReceipt, error) {
	return s.repository().BeginProgressDispatch(c, q)
}
func (s *personalBotStore) SettleProgressDispatch(c context.Context, q store.BotProgressSettleDispatchRequest) (*store.BotProgressDispatchReceipt, error) {
	return s.repository().SettleProgressDispatch(c, q)
}
func (s *teamBotStore) SettleProgressDispatch(c context.Context, q store.BotProgressSettleDispatchRequest) (*store.BotProgressDispatchReceipt, error) {
	return s.repository().SettleProgressDispatch(c, q)
}
func (s *personalBotStore) ContinueProgress(c context.Context, q store.BotProgressContinueRequest) (*store.BotProgressState, error) {
	return s.repository().ContinueProgress(c, q)
}
func (s *teamBotStore) ContinueProgress(c context.Context, q store.BotProgressContinueRequest) (*store.BotProgressState, error) {
	return s.repository().ContinueProgress(c, q)
}

func progressBinding(s *botStoredRun) store.BotProgressBinding {
	var revision int64
	if s.Work != nil {
		revision = s.Work.Revision
	}
	return store.BotProgressBinding{BotID: s.Run.BotID, RunID: s.Run.ID, SessionID: s.Run.SessionID, Version: s.Run.Version, Task: s.Run.Task.Clone(), WorkRevision: revision}
}
func progressRevision(s *botStoredRun) int64 {
	if s.Progress == nil {
		return 0
	}
	return s.Progress.Checkpoint.Revision
}
func progressSafe(s *botStoredRun) bool {
	return s.Progress != nil && s.Progress.Validate() == nil && s.ProgressPending == 0 && !s.ProgressUncertain && s.Progress.Checkpoint.DispatchLedgerCursor == s.ProgressCursor && s.Progress.Checkpoint.Binding.Matches(progressBinding(s))
}
func (r *botRepository) progressMutation(ctx context.Context, tx botDraftDB, m store.BotProgressMutation) (*botStoredRecord, *botStoredRun, error) {
	now := r.clock()
	if err := m.Validate(now); err != nil {
		return nil, nil, err
	}
	// Take the root lock before reading either envelope or keyed receipt. All
	// writers (including lifecycle and transcript) share this lock/CAS barrier.
	locker, ok := tx.(botTranscriptDB)
	if !ok {
		return nil, nil, store.ErrBotUnsupported
	}
	if err := locker.fenceTranscript(ctx, m.Lease, now); err != nil {
		return nil, nil, err
	}
	rec, err := r.checkLease(ctx, tx, m.Lease, now)
	if err != nil {
		return nil, nil, err
	}
	saved, err := tx.ReadRun(ctx, m.Binding.BotID, m.Binding.RunID)
	if err != nil {
		return nil, nil, err
	}
	if !progressBinding(saved).Matches(m.Binding) || progressRevision(saved) != m.ExpectedRevision {
		return nil, nil, store.ErrBotConflict
	}
	v, err := tx.ReadVersion(ctx, m.Binding.BotID, m.Binding.Version)
	if err != nil {
		return nil, nil, err
	}
	if v.Budget.RuntimePolicy.EffectiveMode() != execution.BotRuntimeModeProgressV1 || v.Budget.RuntimePolicy.Validate() != nil {
		return nil, nil, store.ErrBotUnsupported
	}
	return rec, saved, nil
}
func progressUsageAtLeast(n, p store.BotUsage) bool {
	return n.RuntimeNanos >= p.RuntimeNanos && n.ToolCalls >= p.ToolCalls && n.InputTokens >= p.InputTokens && n.OutputTokens >= p.OutputTokens && (p.CostMicros == nil || n.CostMicros != nil && *n.CostMicros >= *p.CostMicros)
}
func progressAdd(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}
func progressCharge(s *botStoredRun, u store.BotUsage) {
	p := &s.Run.Usage
	p.ToolCalls = progressAdd(p.ToolCalls, u.ToolCalls)
	p.InputTokens = progressAdd(p.InputTokens, u.InputTokens)
	p.OutputTokens = progressAdd(p.OutputTokens, u.OutputTokens)
	p.RuntimeNanos = progressAdd(p.RuntimeNanos, u.RuntimeNanos)
	// Unknown pricing stays unknown, never silently becomes a known zero.
	if p.CostMicros != nil && u.CostMicros != nil {
		v := progressAdd(*p.CostMicros, *u.CostMicros)
		p.CostMicros = &v
	} else {
		p.CostMicros = nil
	}
}
func progressAccount(s *botStoredRun, now time.Time) {
	from := s.ProgressAccountedAt
	if from.IsZero() && s.Run.StartedAt != nil {
		from = *s.Run.StartedAt
	}
	if !from.IsZero() && now.After(from) {
		s.Run.Usage.RuntimeNanos = progressAdd(s.Run.Usage.RuntimeNanos, int64(now.Sub(from)))
	}
	s.ProgressAccountedAt = now
	s.Run.RuntimeAccountingRecorded = true
}
func progressKey(run, id uuid.UUID, settlement bool) string {
	suffix := "begin"
	if settlement {
		suffix = "settle"
	}
	return "progress-dispatch:" + run.String() + ":" + id.String() + ":" + suffix
}
func progressRead(ctx context.Context, tx botDraftDB, bot uuid.UUID, key string, v any) error {
	a, err := tx.FindActivity(ctx, bot, key)
	if err != nil {
		return err
	}
	if a.Kind != progressRecordKind {
		return store.ErrBotConflict
	}
	return json.Unmarshal(a.Payload, v)
}
func (r *botRepository) progressWrite(ctx context.Context, tx botDraftDB, rec *botStoredRecord, s *botStoredRun, m store.BotProgressMutation, key string, value any) error {
	if err := tx.AdvanceLeaseSequence(ctx, m.Lease, rec.LastSequence, r.clock()); err != nil {
		return err
	}
	if err := tx.(botWorkDB).updateWorkRun(ctx, *s, m.Lease, r.clock()); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return tx.InsertActivity(ctx, store.BotActivity{BotID: s.Run.BotID, RunID: &s.Run.ID, Sequence: rec.LastSequence + 1, Kind: progressRecordKind, Payload: payload, CreatedAt: r.clock()}, key)
}
func (r *botRepository) CheckpointProgress(ctx context.Context, q store.BotProgressCheckpointRequest) (*store.BotProgressState, error) {
	if err := q.Validate(r.clock()); err != nil {
		return nil, err
	}
	var result *store.BotProgressState
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, s, err := r.progressMutation(ctx, tx, q.Mutation)
		if err != nil {
			return err
		}
		next := q.State.Clone()
		cp := next.Checkpoint
		var prev *store.BotProgressCheckpoint
		if s.Progress != nil {
			prev = s.Progress.Checkpoint
		}
		if err := cp.ValidateSuccessor(prev); err != nil {
			return err
		}
		if s.ProgressPending != 0 || s.ProgressUncertain || cp.DispatchLedgerCursor != s.ProgressCursor || !progressUsageAtLeast(cp.Usage, s.Run.Usage) {
			return store.ErrBotConflict
		}
		// Account owned active time ourselves; caller usage cannot erase it.
		progressAccount(s, r.clock())
		if cp.Usage.RuntimeNanos < s.Run.Usage.RuntimeNanos {
			cp.Usage.RuntimeNanos = s.Run.Usage.RuntimeNanos
		}
		if s.ProgressCursor > 0 && s.Run.Usage.CostMicros == nil {
			cp.Usage.CostMicros = nil
		}
		s.Run.Usage = cp.Usage
		s.Progress = next
		if next.Phase == store.BotProgressSuspended {
			if err := suspendProgressQuota(ctx, tx, rec, s); err != nil {
				return err
			}
			s.Run.Status = store.RunQueued
			if err := tx.FinishRun(ctx, *rec, *s, q.Mutation.Lease, r.clock(), false); err != nil {
				return err
			}
		} else {
			if err := tx.AdvanceLeaseSequence(ctx, q.Mutation.Lease, rec.LastSequence, r.clock()); err != nil {
				return err
			}
			if err := tx.(botWorkDB).updateWorkRun(ctx, *s, q.Mutation.Lease, r.clock()); err != nil {
				return err
			}
		}
		payload, _ := json.Marshal(map[string]any{"run_id": s.Run.ID, "progress_revision": cp.Revision})
		if err := insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: s.Run.BotID, RunID: &s.Run.ID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityRun, Payload: payload, CreatedAt: r.clock()}, fmt.Sprintf("progress-checkpoint:%s:%d", s.Run.ID, cp.Revision)); err != nil {
			return err
		}
		result = next.Clone()
		return nil
	})
	return result, err
}
func (r *botRepository) BeginProgressDispatch(ctx context.Context, q store.BotProgressBeginDispatchRequest) (*store.BotProgressDispatchReceipt, error) {
	if err := q.Validate(r.clock()); err != nil {
		return nil, err
	}
	var result *store.BotProgressDispatchReceipt
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, s, err := r.progressMutation(ctx, tx, q.Mutation)
		if err != nil {
			return err
		}
		key := progressKey(s.Run.ID, q.DispatchID, false)
		var old botProgressDispatch
		if err := progressRead(ctx, tx, s.Run.BotID, key, &old); err == nil {
			if !reflect.DeepEqual(old.Request, q) {
				return store.ErrBotConflict
			}
			result = &old.Receipt
			return nil
		} else if err != store.ErrBotNotFound {
			return err
		}
		if s.ProgressQuota != nil || s.ProgressUncertain || s.ProgressPending == math.MaxInt64 || s.ProgressCursor == math.MaxInt64 {
			return store.ErrBotConflict
		}
		s.ProgressCursor++
		s.ProgressPending++
		progressCharge(s, q.ReservedUsage)
		receipt := store.BotProgressDispatchReceipt{DispatchID: q.DispatchID, LedgerCursor: s.ProgressCursor, Revision: progressRevision(s)}
		if err := r.progressWrite(ctx, tx, rec, s, q.Mutation, key, botProgressDispatch{Request: q, Receipt: receipt}); err != nil {
			return err
		}
		result = &receipt
		return nil
	})
	return result, err
}
func (r *botRepository) SettleProgressDispatch(ctx context.Context, q store.BotProgressSettleDispatchRequest) (*store.BotProgressDispatchReceipt, error) {
	if err := q.Validate(r.clock()); err != nil {
		return nil, err
	}
	var result *store.BotProgressDispatchReceipt
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, s, err := r.progressMutation(ctx, tx, q.Mutation)
		if err != nil {
			return err
		}
		key := progressKey(s.Run.ID, q.DispatchID, true)
		var old botProgressDispatch
		if err := progressRead(ctx, tx, s.Run.BotID, key, &old); err == nil {
			if !reflect.DeepEqual(old.Settlement, &q) {
				return store.ErrBotConflict
			}
			result = &old.Receipt
			return nil
		} else if err != store.ErrBotNotFound {
			return err
		}
		var begun botProgressDispatch
		if err := progressRead(ctx, tx, s.Run.BotID, progressKey(s.Run.ID, q.DispatchID, false), &begun); err != nil {
			return err
		}
		// Tool responses legitimately advance the work journal between begin and
		// settlement. progressMutation already fenced the current work revision;
		// preserve every identity field and reject rollback, not forward progress.
		begunBinding := begun.Request.Mutation.Binding
		if begunBinding.WorkRevision > q.Mutation.Binding.WorkRevision {
			return store.ErrBotConflict
		}
		begunBinding.WorkRevision = q.Mutation.Binding.WorkRevision
		if !begunBinding.Matches(q.Mutation.Binding) || begun.Request.Mutation.Lease.Fence != q.Mutation.Lease.Fence || s.ProgressPending < 1 {
			return store.ErrBotConflict
		}
		// A tool is conservatively mutation-capable. Success prose is not a receipt.
		if q.Outcome == store.BotProgressDispatchUncertain || begun.Request.Kind == store.BotProgressDispatchTool && q.EvidenceRef == "" {
			s.ProgressUncertain = true
		}
		reserved := begun.Request.ReservedUsage
		delta := q.Usage
		delta.ToolCalls = max(0, delta.ToolCalls-reserved.ToolCalls)
		delta.InputTokens = max(0, delta.InputTokens-reserved.InputTokens)
		delta.OutputTokens = max(0, delta.OutputTokens-reserved.OutputTokens)
		delta.RuntimeNanos = max(0, delta.RuntimeNanos-reserved.RuntimeNanos)
		if delta.CostMicros != nil && reserved.CostMicros != nil {
			v := max(0, *delta.CostMicros-*reserved.CostMicros)
			delta.CostMicros = &v
		}
		progressCharge(s, delta)
		s.ProgressPending--
		s.ProgressCursor++
		receipt := store.BotProgressDispatchReceipt{DispatchID: q.DispatchID, LedgerCursor: s.ProgressCursor, Revision: progressRevision(s)}
		begun.Receipt = receipt
		begun.Settlement = &q
		if err := r.progressWrite(ctx, tx, rec, s, q.Mutation, key, begun); err != nil {
			return err
		}
		result = &receipt
		return nil
	})
	return result, err
}
func (r *botRepository) ContinueProgress(ctx context.Context, q store.BotProgressContinueRequest) (*store.BotProgressState, error) {
	if err := q.Validate(r.clock()); err != nil {
		return nil, err
	}
	var result *store.BotProgressState
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, s, err := r.progressMutation(ctx, tx, q.Mutation)
		if err != nil {
			return err
		}
		if !progressSafe(s) || s.ProgressQuota != nil || s.Progress.Checkpoint.Revision != q.CheckpointRevision {
			return store.ErrBotConflict
		}
		s.Progress.Phase = store.BotProgressWorking
		s.Progress.Reason = q.Reason
		progressAccount(s, r.clock())
		if err := r.progressWrite(ctx, tx, rec, s, q.Mutation, fmt.Sprintf("progress-continue:%s:%d", s.Run.ID, q.Mutation.Lease.Fence), q); err != nil {
			return err
		}
		result = s.Progress.Clone()
		return nil
	})
	return result, err
}
