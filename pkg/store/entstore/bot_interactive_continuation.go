package entstore

import (
	"context"
	"strings"

	prun "github.com/SAP/astonish/ent/personal/botrun"
	trun "github.com/SAP/astonish/ent/team/botrun"
	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func (s *personalBotStore) ContinueInteractive(ctx context.Context, q store.BotContinueInteractiveRequest) (*store.BotRun, error) {
	return s.repository().ContinueInteractive(ctx, q)
}
func (s *teamBotStore) ContinueInteractive(ctx context.Context, q store.BotContinueInteractiveRequest) (*store.BotRun, error) {
	return s.repository().ContinueInteractive(ctx, q)
}

func interactiveStatus(run store.BotRun, status store.BotStatus) bool {
	if run.IsInteractiveChat() {
		return status == store.BotDraft || status == store.BotPaused || status == store.BotActive
	}
	return run.Intent.AllowsStatus(status)
}
func (r *botRepository) interactiveAnswerAuthority(ctx context.Context, run store.BotRun) error {
	if !run.IsInteractiveChat() {
		return nil
	}
	if run.CallerCeiling == nil {
		return store.ErrBotForbidden
	}
	var registry *execution.BotTurnAuthorizationRegistry
	if svc := store.FromContext(ctx); svc != nil {
		registry = svc.BotTurnRegistry
	}
	if registry == nil {
		if hook, ok := store.BotMessageAdmissionFromContext(ctx); ok {
			registry, _ = hook.(*execution.BotTurnAuthorizationRegistry)
		}
	}
	if registry == nil {
		return store.ErrBotForbidden
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok {
		return store.ErrBotForbidden
	}
	id := execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: run.CallerCeiling.Principal.Subject, OrgSlug: r.auth.orgSlug, TeamSlug: run.CallerCeiling.Principal.TeamSlug}
	auth, err := registry.Authorizer(id, *run.CallerCeiling, func(context.Context, execution.Principal) (execution.Principal, error) { return p, nil })
	if err != nil {
		return store.ErrBotForbidden
	}
	if auth.Authorize(ctx, execution.CapabilityChat) != nil {
		return store.ErrBotForbidden
	}
	return nil
}
func (r *botRepository) ContinueInteractive(ctx context.Context, q store.BotContinueInteractiveRequest) (*store.BotRun, error) {
	if strings.TrimSpace(q.IdempotencyKey) == "" || len(q.IdempotencyKey) > 200 || (q.Kind != "" && q.Kind != store.BotContinuationProgress) || (q.Kind == "" && q.CheckpointRevision != 0) {
		return nil, store.ErrBotInvalid
	}
	hook, ok := store.BotMessageAdmissionFromContext(ctx)
	if !ok {
		return nil, store.ErrBotForbidden
	}
	admission, _ := hook.(store.BotContinuationAdmission)
	var registered *execution.BotRunIdentity
	var result *store.BotRun
	var holdErr error
	hash, _ := botHash(q)
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
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
		saved, err := tx.ReadRun(ctx, q.BotID, q.RunID)
		if err != nil {
			return err
		}
		run := &saved.Run
		progressContinuation := q.Kind == store.BotContinuationProgress
		mandatedProgress := progressContinuation && run.MandateRef != nil
		if !mandatedProgress && (!run.IsInteractiveChat() || run.CallerCeiling == nil || admission == nil) {
			return store.ErrBotForbidden
		}
		if previous, ok := saved.ContinuationRequests[q.IdempotencyKey]; ok {
			if previous != hash {
				return store.ErrBotConflict
			}
			result = run
			return nil
		}
		statusAllowed := interactiveStatus(*run, rec.Bot.Status) || mandatedProgress && rec.Bot.Status == store.BotPaused
		if rec.Bot.Revision != q.ExpectedRevision || rec.Bot.Version != run.Version || rec.ActiveRunID != nil || !statusAllowed {
			return store.ErrBotConflict
		}
		previousStatus := run.Status
		previousFence := run.Lease.Fence
		if progressContinuation {
			if !progressSafe(saved) || q.CheckpointRevision != progressRevision(saved) || q.Response != nil || saved.Progress.Phase != store.BotProgressSuspended && saved.Progress.Phase != store.BotProgressWaitingAuthorization && saved.Progress.Phase != store.BotProgressWaitingBudget || run.ContinuationReason != store.BotContinuationRequired || run.Status != store.RunInterrupted {
				return store.ErrBotConflict
			}
			v, e := tx.ReadVersion(ctx, run.BotID, run.Version)
			if e != nil {
				return e
			}
			if v.Budget.RuntimePolicy.EffectiveMode() != execution.BotRuntimeModeProgressV1 {
				return store.ErrBotForbidden
			}
		}
		var approval *store.BotApproval
		switch run.Status {
		case store.RunQueued, store.RunInterrupted:
			if run.Status == store.RunInterrupted && run.ContinuationReason != store.BotContinuationRequired {
				return store.ErrBotConflict
			}
			if (!progressContinuation && !safeInteractiveQueue(*saved)) || q.Response != nil {
				return store.ErrBotConflict
			}
		case store.RunWaitingApproval:
			c, e := botdomain.DecodeApprovalCheckpoint(run.Checkpoint, *run)
			if e != nil || c.Format != botdomain.InteractionCheckpointFormat || c.Fence != run.Lease.Fence {
				return store.ErrBotConflict
			}
			approval, e = tx.ReadApproval(ctx, q.BotID, c.ApprovalID)
			if e != nil {
				return e
			}
			if !c.Matches(*approval) || approval.Interaction == nil || approval.Status != store.ApprovalPending || !r.clock().Before(approval.ExpiresAt) || approval.Interaction.ValidateResponse(q.Response) != nil {
				return store.ErrBotConflict
			}
		default:
			return store.ErrBotConflict
		}
		if mandatedProgress {
			if !verifiedMandateOwner(ctx, rec.Bot.OwnerID.String(), r.clock()) {
				return store.ErrBotForbidden
			}
			if err := r.authorizeMandateProgressContinuation(ctx, tx, rec, saved); err != nil {
				holdErr = err
				return r.holdProgressContinuation(ctx, tx, rec, saved, err)
			}
		} else {
			id := execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: actor.String(), OrgSlug: r.auth.orgSlug, TeamSlug: run.CallerCeiling.Principal.TeamSlug}
			// A paused progress assignment may still have live, bounded caller
			// authority. Reuse it without extending expiry; only expired/lost authority
			// goes through fresh authenticated registration and ceiling intersection.
			if !progressContinuation || r.interactiveAnswerAuthority(ctx, *run) != nil {
				auth, err := admission.RegisterContinuation(ctx, id, *run.CallerCeiling)
				if err != nil {
					return store.ErrBotForbidden
				}
				registered = &id
				if auth.Identity != id {
					return store.ErrBotForbidden
				}
				ceiling := auth.CallerCeiling()
				run.CallerCeiling = &ceiling
			}
		}
		run.ContinuationReason = ""
		if saved.ContinuationRequests == nil {
			saved.ContinuationRequests = map[string]string{}
		}
		saved.ContinuationRequests[q.IdempotencyKey] = hash
		if approval != nil {
			now := r.clock()
			approval.Status = store.ApprovalApproved
			approval.Response = q.Response
			approval.DecidedAt = &now
			approval.DecidedBy = &actor
			if err := tx.UpdateApproval(ctx, *approval, store.ApprovalPending, now); err != nil {
				return err
			}
			if err := tx.AdvanceSequence(ctx, q.BotID, rec.LastSequence); err != nil {
				return err
			}
			rec.LastSequence++
			if err := botApprovalActivity(ctx, tx, *approval, rec.LastSequence, now); err != nil {
				return err
			}
			if err := r.confirmTitleAssignment(ctx, tx, rec, saved, *approval); err != nil {
				return err
			}
			if err := r.queueApprovedCheckpoint(ctx, tx, rec, saved, run.Lease.Fence, approval.ID); err != nil {
				return err
			}
		} else {
			run.Status = store.RunQueued
			if progressContinuation {
				switch rec.Bot.Status {
				case store.BotPaused:
					run.Intent = store.BotIntentConversation
				case store.BotDraft:
					run.Intent = store.BotIntentDraftConversation
				case store.BotActive:
					run.Intent = store.BotIntentRegular
				}
				saved.Progress.Phase = store.BotProgressWorking
				saved.Progress.Reason = store.BotProgressReasonRecovery
				saved.Progress.Checkpoint.NextEligibleAt = r.clock()
				saved.ProgressAccountedAt = r.clock()
			}
			if err := tx.AdvanceSequence(ctx, q.BotID, rec.LastSequence); err != nil {
				return err
			}
			data, err := botJSONMap(*saved)
			if err != nil {
				return err
			}
			var n int
			switch db := tx.(type) {
			case personalBotDB:
				n, err = db.client.BotRun.Update().Where(prun.IDEQ(run.ID), prun.BotIDEQ(run.BotID), prun.StatusEQ(prun.Status(previousStatus)), prun.FenceEQ(previousFence)).SetStatus(prun.StatusQueued).SetData(data).Save(ctx)
			case teamBotDB:
				n, err = db.client.BotRun.Update().Where(trun.IDEQ(run.ID), trun.BotIDEQ(run.BotID), trun.StatusEQ(trun.Status(previousStatus)), trun.FenceEQ(previousFence)).SetStatus(trun.StatusQueued).SetData(data).Save(ctx)
			default:
				return store.ErrBotUnsupported
			}
			if err != nil {
				return err
			}
			if n != 1 {
				return store.ErrBotConflict
			}
			if err := appendBotRunStateTx(ctx, tx, rec.Bot, *run, rec.LastSequence+1, r.clock()); err != nil {
				return err
			}
		}
		result = run
		return nil
	})
	if err != nil {
		if registered != nil {
			admission.Cancel(*registered)
		}
		return nil, err
	}
	return result, holdErr
}
