package entstore

import (
	"context"
	"errors"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// This receipt authorizes one saved assignment while recurrence remains paused.
// A later lifecycle revision invalidates it; it is never an authority renewal.
type botProgressOwnerContinuation struct {
	BotRevision        int64 `json:"bot_revision"`
	CheckpointRevision int64 `json:"checkpoint_revision"`
}

func (r *botRepository) authorizeMandateProgressContinuation(ctx context.Context, tx botDraftDB, rec *botStoredRecord, saved *botStoredRun) error {
	if !verifiedMandateOwner(ctx, rec.Bot.OwnerID.String(), r.clock()) {
		return store.ErrBotForbidden
	}
	policy, ok := store.BotMandateAdmissionFromContext(ctx)
	if !ok {
		return store.ErrBotForbidden
	}
	runtime := &botMandateRuntime{repo: r, policy: policy}
	ref := *saved.Run.MandateRef
	// verify recursively checks the original parent, operation receipt and grant.
	// No new source event, parent, grant, expiry or budget is minted here.
	m, _, err := runtime.verify(ctx, tx, ref, execution.BotMandatePurposeTask)
	if err != nil {
		return store.ErrBotForbidden
	}
	if m.Mandate.RuntimePolicy.EffectiveMode() != execution.BotRuntimeModeProgressV1 {
		return store.ErrBotForbidden
	}
	if saved.Run.Trigger == store.TriggerCron {
		binding, err := readScheduledTask(ctx, tx, ref)
		if err != nil || binding.TriggerID != saved.Run.TriggerID || binding.Instructions != saved.Instructions {
			return store.ErrBotForbidden
		}
	}
	if err := restoreProgressOccupancy(ctx, tx, rec, saved, m.Mandate.Limits); err != nil {
		return err
	}
	saved.ProgressOwnerContinuation = &botProgressOwnerContinuation{BotRevision: rec.Bot.Revision, CheckpointRevision: progressRevision(saved)}
	return nil
}

// Called only after full mandate verification. The private checkpoint quota is
// restored atomically, not replaced with a new daily/per-run reservation.
func restoreProgressOccupancy(ctx context.Context, tx botDraftDB, rec *botStoredRecord, saved *botStoredRun, limits execution.BotMandateLimits) error {
	u, err := progressQuotaUsage(rec)
	if err != nil {
		return err
	}
	ref := *saved.Run.MandateRef
	current, active := u.Active[saved.Run.ID.String()]
	if active {
		if current.Ref != ref || saved.ProgressQuota != nil {
			return store.ErrBotForbidden
		}
	} else {
		if saved.ProgressQuota == nil || saved.ProgressQuota.Ref != ref || int64(len(u.Active)) >= limits.MaxConcurrentRuns {
			return store.ErrBotForbidden
		}
		current = *saved.ProgressQuota
	}
	// Check actual remaining balances, without resetting the aggregate ledger.
	for _, cap := range []struct {
		dimension   string
		used, limit int64
	}{
		{"run_tokens", current.Tokens, limits.MaxTokens}, {"total_tokens", u.Tokens, limits.MaxTotalTokens},
		{"run_tool_calls", current.Tools, limits.MaxToolCalls}, {"total_tool_calls", u.Tools, limits.MaxTotalToolCalls},
		{"run_spend", current.Spend, limits.MaxSpendMicros}, {"total_spend", u.Spend, limits.MaxSpendMicros},
	} {
		// A zero spend ceiling permits free operations (the dispatch ledger still
		// rejects every positive spend). It is not an exhausted token/tool cap.
		if (cap.dimension == "run_spend" || cap.dimension == "total_spend") && cap.limit == 0 && cap.used == 0 {
			continue
		}
		if cap.used >= cap.limit {
			return &store.BotMandateQuotaError{Dimension: cap.dimension, Used: cap.used, Limit: cap.limit}
		}
	}
	if !active {
		u.Active[saved.Run.ID.String()] = current
		saved.ProgressQuota = nil
		if rec.Metadata == nil {
			rec.Metadata = map[string]any{}
		}
		rec.Metadata[mandateUsageKey] = u
		return saveMandateMetadata(ctx, tx, rec)
	}
	return nil
}

func (r *botRepository) holdProgressContinuation(ctx context.Context, tx botDraftDB, rec *botStoredRecord, saved *botStoredRun, cause error) error {
	phase, reason := store.BotProgressWaitingAuthorization, store.BotProgressReasonAuthorization
	var quota *store.BotMandateQuotaError
	if errors.As(cause, &quota) {
		phase, reason = store.BotProgressWaitingBudget, store.BotProgressReasonBudget
	}
	if saved.Progress.Phase == phase && saved.Progress.Reason == reason {
		return nil
	}
	saved.Progress.Phase, saved.Progress.Reason = phase, reason
	saved.ProgressOwnerContinuation = nil
	if err := tx.AdvanceSequence(ctx, rec.Bot.ID, rec.LastSequence); err != nil {
		return err
	}
	if err := saveProgressEnvelope(ctx, tx, saved); err != nil {
		return err
	}
	return appendBotRunStateTx(ctx, tx, rec.Bot, saved.Run, rec.LastSequence+1, r.clock())
}

func mandateProgressWhilePaused(ctx context.Context, tx botDraftDB, rec *botStoredRecord, ref store.BotMandateRef) bool {
	id, ok := execution.BotRunIdentityFromContext(ctx)
	if !ok || id.BotID != ref.BotID || id.ActorID != ref.OwnerID || id.OrgSlug != ref.OrgSlug || id.TeamSlug != ref.TeamSlug {
		return false
	}
	saved, err := tx.ReadRun(ctx, ref.BotID, id.RunID)
	if err != nil || saved.Run.MandateRef == nil || *saved.Run.MandateRef != ref || saved.Run.Version != id.Version || saved.Run.Version != rec.Bot.Version {
		return false
	}
	permit := saved.ProgressOwnerContinuation
	return permit != nil && permit.BotRevision == rec.Bot.Revision && permit.CheckpointRevision > 0 && progressRevision(saved) >= permit.CheckpointRevision && saved.Run.Intent == store.BotIntentConversation && (saved.Run.Status == store.RunQueued || saved.Run.Status == store.RunRunning)
}
