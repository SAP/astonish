package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	prun "github.com/SAP/astonish/ent/personal/botrun"
	psession "github.com/SAP/astonish/ent/personal/session"
	pevent "github.com/SAP/astonish/ent/personal/sessionevent"
	trun "github.com/SAP/astonish/ent/team/botrun"
	tsession "github.com/SAP/astonish/ent/team/session"
	tevent "github.com/SAP/astonish/ent/team/sessionevent"
	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

var _ store.BotApprovalResumeStore = (*personalBotStore)(nil)
var _ store.BotApprovalResumeStore = (*teamBotStore)(nil)

func (s *personalBotStore) RequeueApprovedRun(ctx context.Context, b, r, a uuid.UUID) error {
	return s.repository().RequeueApprovedRun(ctx, b, r, a)
}
func (s *teamBotStore) RequeueApprovedRun(ctx context.Context, b, r, a uuid.UUID) error {
	return s.repository().RequeueApprovedRun(ctx, b, r, a)
}
func (s *personalBotStore) LoadResumeApproval(ctx context.Context, l store.BotLease) (*store.BotApproval, error) {
	return s.repository().LoadResumeApproval(ctx, l)
}
func (s *teamBotStore) LoadResumeApproval(ctx context.Context, l store.BotLease) (*store.BotApproval, error) {
	return s.repository().LoadResumeApproval(ctx, l)
}

var _ store.BotApproveContinueStore = (*personalBotStore)(nil)
var _ store.BotApproveContinueStore = (*teamBotStore)(nil)

func (s *personalBotStore) DecideAndRequeueApprovedRun(ctx context.Context, q store.BotApproveContinueRequest) (*store.BotApproveContinueResult, error) {
	return s.repository().DecideAndRequeueApprovedRun(ctx, q)
}
func (s *teamBotStore) DecideAndRequeueApprovedRun(ctx context.Context, q store.BotApproveContinueRequest) (*store.BotApproveContinueResult, error) {
	return s.repository().DecideAndRequeueApprovedRun(ctx, q)
}

// DecideAndRequeueApprovedRun never activates a Bot and never accepts replacement
// execution input. Decision, queue CAS, and activity records commit together.
func (r *botRepository) DecideAndRequeueApprovedRun(ctx context.Context, q store.BotApproveContinueRequest) (*store.BotApproveContinueResult, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return nil, err
	}
	var result *store.BotApproveContinueResult
	var registered *execution.BotRunIdentity
	var continuation store.BotContinuationAdmission
	err := r.db.Transaction(ctx, func(tx botDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != rec.Bot.OwnerID || actor != q.ActorID {
			return store.ErrBotForbidden
		}
		a, err := tx.ReadApproval(ctx, q.BotID, q.ApprovalID)
		if err != nil {
			return err
		}
		saved, err := tx.ReadRun(ctx, q.BotID, a.RunID)
		if err != nil {
			return err
		}
		if titleAnswerReplay(saved, a, q.Response) && a.ActionDigest == q.ActionDigest {
			result = &store.BotApproveContinueResult{Approval: *a, RunID: a.RunID, Continuation: store.BotContinuationQueued}
			return nil
		}
		now := r.clock()
		if !now.Before(a.ExpiresAt) {
			return store.ErrBotExpired
		}
		if a.ActionDigest != q.ActionDigest || a.Version != rec.Bot.Version || rec.Bot.Status == store.BotStopped || rec.Bot.Status == store.BotDeleted {
			return store.ErrBotConflict
		}
		if (saved.Run.ControlOnly() && !saved.Run.IsInteractiveChat()) || saved.Run.Version != rec.Bot.Version || rec.ActiveRunID != nil {
			return store.ErrBotConflict
		}
		paused := rec.Bot.Status == store.BotPaused && !saved.Run.IsInteractiveChat()
		if !paused && !interactiveStatus(saved.Run, rec.Bot.Status) {
			return store.ErrBotConflict
		}
		if saved.Run.Status != store.RunWaitingApproval && saved.Run.Status != store.RunQueued {
			return store.ErrBotConflict
		}
		c, err := botdomain.DecodeApprovalCheckpoint(saved.Run.Checkpoint, saved.Run)
		if err != nil || !c.Matches(*a) || c.Fence > saved.Run.Lease.Fence || (c.Fence != saved.Run.Lease.Fence && a.Status != store.ApprovalApproved) {
			return store.ErrBotConflict
		}
		if a.Interaction != nil {
			if a.Interaction.ValidateResponse(q.Response) != nil {
				return store.ErrBotInvalid
			}
		} else if q.Response != nil {
			return store.ErrBotInvalid
		}
		initialTitle := saved.Run.TitleConfirmation != nil && saved.Run.TitleConfirmation.State != store.BotTitleConfirmationConfirmed
		question := saved.Run.IsInteractiveChat() && !initialTitle && a.Interaction != nil && a.Interaction.Kind == store.BotInteractionQuestion && c.Format == botdomain.InteractionCheckpointFormat
		if question && saved.Run.MandateRef != nil {
			if err := r.authorizeQuestionContinuation(ctx, tx, rec, saved); err != nil {
				return err
			}
		} else if saved.Run.IsInteractiveChat() && !initialTitle && !question {
			if r.interactiveAnswerAuthority(ctx, saved.Run) != nil {
				return store.ErrBotForbidden
			}
			if a.Interaction == nil {
				if err := privateWorkerApprovalBinding(ctx, tx, rec.Bot, saved.Run, *a); err != nil {
					return err
				}
			}
		}
		// Only a validated question or the existing initial-title path can
		// recover lost registry authority from this authenticated submission.
		if saved.Run.MandateRef == nil && (initialTitle || question) && saved.Run.IsInteractiveChat() && r.interactiveAnswerAuthority(ctx, saved.Run) != nil {
			hook, ok := store.BotMessageAdmissionFromContext(ctx)
			if !ok {
				return store.ErrBotForbidden
			}
			continuation, ok = hook.(store.BotContinuationAdmission)
			if !ok || saved.Run.CallerCeiling == nil {
				return store.ErrBotForbidden
			}
			id := execution.BotRunIdentity{BotID: saved.Run.BotID, RunID: saved.Run.ID, Version: saved.Run.Version, ActorID: actor.String(), OrgSlug: r.auth.orgSlug, TeamSlug: saved.Run.CallerCeiling.Principal.TeamSlug}
			authority, err := continuation.RegisterContinuation(ctx, id, *saved.Run.CallerCeiling)
			if err != nil {
				return store.ErrBotForbidden
			}
			registered = &id
			if authority.Identity != id {
				return store.ErrBotForbidden
			}
			ceiling := authority.CallerCeiling()
			saved.Run.CallerCeiling = &ceiling
		}
		switch a.Status {
		case store.ApprovalPending:
			if saved.Run.Status != store.RunWaitingApproval {
				return store.ErrBotConflict
			}
			if err := tx.AdvanceSequence(ctx, q.BotID, rec.LastSequence); err != nil {
				return err
			}
			a.Status = store.ApprovalApproved
			a.Response = q.Response
			a.DecidedAt = &now
			a.DecidedBy = &actor
			if err := tx.UpdateApproval(ctx, *a, store.ApprovalPending, now); err != nil {
				return err
			}
			rec.LastSequence++
			if err := botApprovalActivity(ctx, tx, *a, rec.LastSequence, now); err != nil {
				return err
			}
		case store.ApprovalApproved:
			if a.DecidedBy == nil || *a.DecidedBy != actor || !reflect.DeepEqual(a.Response, q.Response) {
				return store.ErrBotConflict
			}
		default:
			return store.ErrBotConflict
		}
		state := store.BotContinuationQueued
		if paused {
			if saved.Run.Status != store.RunWaitingApproval {
				return store.ErrBotConflict
			}
			state = store.BotContinuationBlockedByPause
		} else if saved.Run.Status == store.RunWaitingApproval {
			if question {
				saved.Run.ContinuationReason = ""
			}
			if err := r.confirmTitleAssignment(ctx, tx, rec, saved, *a); err != nil {
				return err
			}
			if err := r.queueApprovedCheckpoint(ctx, tx, rec, saved, saved.Run.Lease.Fence, a.ID); err != nil {
				return err
			}
		}
		result = &store.BotApproveContinueResult{Approval: *a, RunID: a.RunID, Continuation: state}
		return nil
	})
	if err != nil {
		if registered != nil {
			continuation.Cancel(*registered)
		}
		return nil, err
	}
	return result, nil
}

// privateWorkerApprovalBinding reads only the server-owned, scoped run session.
// Its content-free ledger is written under a live lease by the private Chat runner;
// neither owner/model text nor the worker marker alone grants continuation.
func privateWorkerApprovalBinding(ctx context.Context, tx botDB, b store.Bot, run store.BotRun, a store.BotApproval) error {
	if run.MandateRef != nil || run.Report.EffectState == store.BotReportEffectsUncertain || a.Status == store.ApprovalConsumed {
		return store.ErrBotForbidden
	}
	digest, err := a.Action.Digest()
	if err != nil || digest != a.ActionDigest || a.BotID != run.BotID || a.RunID != run.ID || a.Version != run.Version {
		return store.ErrBotForbidden
	}
	var events []*session.Event
	switch db := tx.(type) {
	case personalBotDB:
		ok, err := db.client.Session.Query().Where(psession.IDEQ(run.SessionID.String()), psession.BotIDEQ(b.ID), psession.BotRunIDEQ(run.ID), psession.UserIDEQ(b.OwnerID), psession.PurposeEQ(psession.PurposeBotRun)).Exist(ctx)
		if err != nil || !ok {
			return store.ErrBotForbidden
		}
		rows, err := db.client.SessionEvent.Query().Where(pevent.SessionIDEQ(run.SessionID.String())).Order(pevent.ByID()).All(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			e, err := db.contentCodec().decodeSessionEvent(ctx, b.ID, run.SessionID.String(), row.EventData)
			if err != nil {
				return err
			}
			events = append(events, e)
		}
	case teamBotDB:
		ok, err := db.client.Session.Query().Where(tsession.IDEQ(run.SessionID.String()), tsession.BotIDEQ(b.ID), tsession.BotRunIDEQ(run.ID), tsession.UserIDEQ(b.OwnerID), tsession.PurposeEQ(tsession.PurposeBotRun)).Exist(ctx)
		if err != nil || !ok {
			return store.ErrBotForbidden
		}
		rows, err := db.client.SessionEvent.Query().Where(tevent.SessionIDEQ(run.SessionID.String())).Order(tevent.ByID()).All(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			e, err := db.contentCodec().decodeSessionEvent(ctx, b.ID, run.SessionID.String(), row.EventData)
			if err != nil {
				return err
			}
			events = append(events, e)
		}
	default:
		return store.ErrBotForbidden
	}
	return matchPrivateWorkerApproval(events, a)
}

type privateWorkerPhase string

const (
	privateWorkerStarted  privateWorkerPhase = "started"
	privateWorkerEvent    privateWorkerPhase = "event"
	privateWorkerApproval privateWorkerPhase = "approval"
	privateWorkerResuming privateWorkerPhase = "resuming"
	privateWorkerSettled  privateWorkerPhase = "settled"
)

// Match the persisted v1 envelope without importing API (which depends on entstore).
func matchPrivateWorkerApproval(events []*session.Event, a store.BotApproval) error {
	type trace struct {
		WorkerID     string             `json:"worker_id"`
		DispatchID   string             `json:"dispatch_id"`
		Phase        privateWorkerPhase `json:"phase"`
		Event        *session.Event     `json:"event,omitempty"`
		ApprovalID   uuid.UUID          `json:"approval_id,omitempty"`
		ActionDigest string             `json:"action_digest,omitempty"`
	}
	workers := map[string]trace{}
	for _, e := range events {
		if e == nil || e.Author != "bot_runtime" || e.Content != nil {
			continue
		}
		value, ok := e.Actions.StateDelta["bot_private_worker_v1"]
		if !ok {
			continue
		}
		raw, err := json.Marshal(value)
		var t trace
		if err != nil || json.Unmarshal(raw, &t) != nil || t.WorkerID == "" || t.DispatchID == "" {
			return store.ErrBotForbidden
		}
		w := workers[t.WorkerID]
		switch t.Phase {
		case privateWorkerStarted:
			if w.DispatchID != "" {
				return store.ErrBotForbidden
			}
			w = t
		case privateWorkerEvent:
			if w.DispatchID != t.DispatchID || t.Event == nil {
				return store.ErrBotForbidden
			}
		case privateWorkerApproval:
			if w.DispatchID != t.DispatchID || w.ApprovalID != uuid.Nil || t.ApprovalID == uuid.Nil || t.ActionDigest == "" {
				return store.ErrBotForbidden
			}
			w.ApprovalID, w.ActionDigest = t.ApprovalID, t.ActionDigest
		case privateWorkerResuming:
			if w.DispatchID != t.DispatchID || w.ApprovalID != t.ApprovalID || w.ActionDigest != t.ActionDigest {
				return store.ErrBotForbidden
			}
			w.ApprovalID, w.ActionDigest = uuid.Nil, ""
		case privateWorkerSettled:
			if w.DispatchID != t.DispatchID {
				return store.ErrBotForbidden
			}
			w = trace{}
		default:
			return store.ErrBotForbidden
		}
		workers[t.WorkerID] = w
	}
	matches := 0
	for _, w := range workers {
		if w.DispatchID != "" && w.ApprovalID == a.ID && w.ActionDigest == a.ActionDigest {
			matches++
		}
	}
	if matches != 1 {
		return store.ErrBotForbidden
	}
	return nil
}

func resumeApproval(ctx context.Context, tx botDB, run store.BotRun) (*store.BotApproval, error) {
	c, err := botdomain.DecodeApprovalCheckpoint(run.Checkpoint, run)
	if err != nil {
		return nil, err
	}
	a, err := tx.ReadApproval(ctx, run.BotID, c.ApprovalID)
	if err != nil {
		return nil, err
	}
	if !c.Matches(*a) || a.Status != store.ApprovalApproved {
		return nil, store.ErrBotConflict
	}
	return a, nil
}
func (r *botRepository) RequeueApprovedRun(ctx context.Context, id, runID, approvalID uuid.UUID) error {
	if _, err := r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return err
	}
	return r.db.Transaction(ctx, func(tx botDB) error {
		rec, err := tx.GetBot(ctx, id)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != rec.Bot.OwnerID {
			return store.ErrBotForbidden
		}
		saved, err := tx.ReadRun(ctx, id, runID)
		if err != nil {
			return err
		}
		if saved.Run.TitleConfirmation != nil && saved.Run.TitleConfirmation.State != store.BotTitleConfirmationConfirmed {
			return store.ErrBotForbidden
		}
		if !interactiveStatus(saved.Run, rec.Bot.Status) || (saved.Run.ControlOnly() && !saved.Run.IsInteractiveChat()) || rec.Bot.Version != saved.Run.Version || rec.ActiveRunID != nil {
			return store.ErrBotConflict
		}
		a, err := resumeApproval(ctx, tx, saved.Run)
		if err != nil {
			return err
		}
		if saved.Run.IsInteractiveChat() && (saved.Run.TitleConfirmation == nil || saved.Run.TitleConfirmation.State == store.BotTitleConfirmationConfirmed) && (a.Interaction == nil || r.interactiveAnswerAuthority(ctx, saved.Run) != nil) {
			return store.ErrBotForbidden
		}
		if a.ID != approvalID {
			return store.ErrBotConflict
		}
		if !r.clock().Before(a.ExpiresAt) {
			return store.ErrBotExpired
		}
		if saved.Run.Status == store.RunQueued {
			return nil
		}
		if saved.Run.Status != store.RunWaitingApproval {
			return store.ErrBotConflict
		}
		c, _ := botdomain.DecodeApprovalCheckpoint(saved.Run.Checkpoint, saved.Run)
		if c.Fence > saved.Run.Lease.Fence {
			return store.ErrBotConflict
		}
		// Denied resumes retain the original checkpoint bytes; queue CAS still
		// binds the latest completed lease, never the historical approval fence.
		return r.queueApprovedCheckpoint(ctx, tx, rec, saved, saved.Run.Lease.Fence, approvalID)
	})
}

// queueApprovedCheckpoint runs inside the caller's decision transaction.
func (r *botRepository) queueApprovedCheckpoint(ctx context.Context, tx botDB, rec *botStoredRecord, saved *botStoredRun, fence int64, approvalID uuid.UUID) error {
	id, runID := saved.Run.BotID, saved.Run.ID
	if err := tx.AdvanceSequence(ctx, id, rec.LastSequence); err != nil {
		return err
	}
	saved.Run.Status = store.RunQueued
	saved.CommitHash = ""
	data, err := encodeBotRunData(ctx, tx, *saved)
	if err != nil {
		return err
	}
	var n int
	switch db := tx.(type) {
	case personalBotDB:
		n, err = db.client.BotRun.Update().Where(prun.IDEQ(runID), prun.BotIDEQ(id), prun.StatusEQ(prun.StatusWaitingApproval), prun.FenceEQ(fence)).SetStatus(prun.StatusQueued).SetData(data).Save(ctx)
	case teamBotDB:
		n, err = db.client.BotRun.Update().Where(trun.IDEQ(runID), trun.BotIDEQ(id), trun.StatusEQ(trun.StatusWaitingApproval), trun.FenceEQ(fence)).SetStatus(trun.StatusQueued).SetData(data).Save(ctx)
	default:
		return store.ErrBotUnsupported
	}
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	payload, _ := json.Marshal(map[string]any{"run_id": runID, "status": store.RunQueued, "approval_id": approvalID})
	return insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: id, RunID: &runID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityRun, Payload: payload, CreatedAt: r.clock()}, "approval-resume:"+approvalID.String()+":"+fmt.Sprint(fence))
}
func (r *botRepository) LoadResumeApproval(ctx context.Context, l store.BotLease) (*store.BotApproval, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	var a *store.BotApproval
	err := r.db.Transaction(ctx, func(tx botDB) error {
		if _, err := r.checkLease(ctx, tx, l, r.clock()); err != nil {
			return err
		}
		saved, err := tx.ReadRun(ctx, l.BotID, l.RunID)
		if err != nil {
			return err
		}
		c, err := botdomain.DecodeApprovalCheckpoint(saved.Run.Checkpoint, saved.Run)
		if err != nil {
			return err
		}
		if c.Fence >= l.Fence {
			return store.ErrBotConflict
		}
		a, err = resumeApproval(ctx, tx, saved.Run)
		if err != nil {
			return err
		}
		if !r.clock().Before(a.ExpiresAt) {
			return store.ErrBotExpired
		}
		return nil
	})
	return a, err
}
