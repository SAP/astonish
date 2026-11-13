package entstore

import (
	"context"
	"encoding/json"
	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"time"
)

// Preflight performs service readiness outside the write transaction. The exact
// persisted Bot is compared again inside it, including owner and home binding.
func (r *botRepository) prepareControlExecution(ctx context.Context, q store.BotControlProposalDecision) (*store.Bot, error) {
	rec, err := r.db.GetBot(ctx, q.BotID)
	if err != nil {
		return nil, err
	}
	if _, err = r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
		return nil, err
	}
	p, err := readControlProposal(ctx, r.db, q.BotID, q.ProposalID)
	if err != nil {
		return nil, err
	}
	if p.Digest != q.Digest {
		return nil, store.ErrBotConflict
	}
	if p.Status != store.BotControlProposed || !r.clock().Before(p.ExpiresAt) || p.Version != rec.Bot.Version || p.ExpectedRevision != rec.Bot.Revision {
		return nil, nil
	}
	op := p.Payload.Operation
	if op != store.BotControlActivate && op != store.BotControlResume && op != store.BotControlTaskOnceOperation {
		return nil, nil
	}
	source, err := r.db.ReadRun(ctx, p.BotID, p.SourceRunID)
	if err != nil {
		return nil, err
	}
	if rec.ActiveRunID != nil || !source.Run.Status.Terminal() {
		return nil, store.ErrBotBusy
	}
	v, err := r.db.ReadVersion(ctx, p.BotID, p.Version)
	if err != nil {
		return nil, err
	}
	if err = botdomain.ValidateVersion(*v); err != nil {
		return nil, err
	}
	if op == store.BotControlTaskOnceOperation {
		if rec.Bot.Status != store.BotDraft && rec.Bot.Status != store.BotActive {
			return nil, store.ErrBotForbidden
		}
		v.Triggers = nil
	} else {
		event := botdomain.LifecycleActivate
		if op == store.BotControlResume {
			event = botdomain.LifecycleResume
		}
		if _, err = botdomain.NextBotStatus(rec.Bot.Status, event, r.clock(), time.Time{}); err != nil {
			return nil, err
		}
		if err = validateActivationTriggers(ctx, r.db, rec.Bot, *v); err != nil {
			return nil, err
		}
	}
	check := r.activationCheck
	if check == nil {
		check = r.auth.readiness
	}
	if check == nil {
		return nil, store.ErrBotUnsupported
	}
	// Only explicit, validated consent may allocate. Use the trusted composition
	// callback, never model-supplied registration authority or home coordinates.
	if svc := store.FromContext(ctx); rec.Bot.Status == store.BotDraft && rec.Bot.Home.ID == uuid.Nil && svc != nil && svc.BotHomeSetup != nil {
		repo := svc.Bots
		if rec.Bot.Scope == store.BotScopePersonal {
			repo = svc.PersonalBots
		}
		if repo == nil {
			return nil, store.ErrBotUnsupported
		}
		before := rec.Bot
		prepared, e := svc.BotHomeSetup(ctx, repo, before.ID, before.Revision)
		if e != nil {
			return nil, e
		}
		current, e := r.db.GetBot(ctx, before.ID)
		if e != nil {
			return nil, e
		}
		if prepared == nil || !reflect.DeepEqual(*prepared, current.Bot) || current.Bot.Home.ID == uuid.Nil || current.Bot.Revision != before.Revision+1 {
			return nil, store.ErrBotConflict
		}
		// Permit exactly the home registration, not an intervening edit/transfer.
		normalized := current.Bot
		normalized.Home, normalized.Revision, normalized.UpdatedAt = before.Home, before.Revision, before.UpdatedAt
		if !reflect.DeepEqual(normalized, before) {
			return nil, store.ErrBotConflict
		}
		rec = current
	}
	if err = check(ctx, rec.Bot, *v); err != nil {
		return nil, err
	}
	return &rec.Bot, nil
}
func (r *botRepository) applyControlLifecycleTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, p store.BotControlProposal, actor uuid.UUID) error {
	v, err := tx.ReadVersion(ctx, p.BotID, p.Version)
	if err != nil {
		return err
	}
	if err = validateActivationTriggers(ctx, tx, rec.Bot, *v); err != nil {
		return err
	}
	event := botdomain.LifecycleActivate
	if p.Payload.Operation == store.BotControlResume {
		event = botdomain.LifecycleResume
	}
	status, err := botdomain.NextBotStatus(rec.Bot.Status, event, r.clock(), time.Time{})
	if err != nil {
		return err
	}
	next := rec.Bot
	next.Status = status
	next.Revision++
	next.UpdatedAt = r.clock()
	if err = tx.SetLifecycle(ctx, *rec, next); err != nil {
		return err
	}
	if err = r.enableDraftActivationSchedules(ctx, tx, rec.Bot, next, *v); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"event": event, "status": status, "actor_id": actor, "proposal_id": p.ID})
	if err != nil {
		return err
	}
	return tx.InsertActivity(ctx, store.BotActivity{BotID: p.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: data, CreatedAt: r.clock()}, "control-lifecycle:"+p.ID.String())
}
func (r *botRepository) applyControlTaskTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, p store.BotControlProposal, actor uuid.UUID) error {
	intent := store.BotIntentRegular
	switch rec.Bot.Status {
	case store.BotDraft:
		intent = store.BotIntentDraftTest
	case store.BotActive:
	default:
		return store.ErrBotForbidden
	}
	v, err := tx.ReadVersion(ctx, p.BotID, p.Version)
	if err != nil {
		return err
	}
	now := r.clock()
	if v.Budget.MaxRunsPerDay != nil {
		loc, e := time.LoadLocation(v.Persona.Timezone)
		if e != nil {
			return store.ErrBotInvalid
		}
		local := now.In(loc)
		midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		count, e := tx.CountRunsSince(ctx, p.BotID, midnight)
		if e != nil {
			return e
		}
		if int64(count) >= *v.Budget.MaxRunsPerDay {
			return store.ErrBotForbidden
		}
	}
	cas, ok := tx.(botControlVersionDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	if err = cas.updateControlVersion(ctx, *rec, rec.Bot.Version, now); err != nil {
		return err
	}
	request := store.BotRunRequest{SkillID: p.Payload.TaskOnce.SkillID, Invocation: p.Payload.TaskOnce.Invocation, BotID: p.BotID, Version: p.Version, Trigger: store.TriggerManual, Intent: intent, IdempotencyKey: controlProposalKey(p.ID), Instructions: p.Payload.TaskOnce.Instructions}
	if err := validateRunSkillSelection(ctx, tx, p.BotID, request.SkillID, request.Invocation); err != nil {
		return err
	}
	if request.SkillID != nil {
		if request.Instructions != "" && request.Instructions != request.Invocation {
			return store.ErrBotInvalid
		}
		request.Instructions = request.Invocation
	}
	hash, err := botHash(request)
	if err != nil {
		return err
	}
	run := store.BotRun{SkillID: request.SkillID, Invocation: request.Invocation, ID: uuid.NewSHA1(p.ID, []byte("run")), SessionID: uuid.NewSHA1(p.ID, []byte("session")), BotID: p.BotID, Version: p.Version, Intent: intent, Trigger: store.TriggerManual, IdempotencyKey: request.IdempotencyKey, Status: store.RunQueued, CreatedAt: now}
	if ref, bounded := ctx.Value(routineTaskKey{}).(store.BotMandateRef); bounded {
		run.MandateRef = &ref
		binding := routineTaskBinding{Ref: ref, OperationID: p.ID, Instructions: request.Instructions}
		fresh, e := tx.GetBot(ctx, p.BotID)
		if e != nil {
			return e
		}
		if e = appendMandateRecord(ctx, tx, fresh, "routine-task-run:"+run.ID.String(), binding, now); e != nil {
			return e
		}
		rec.LastSequence = fresh.LastSequence
		// Reserve the sequence for the dispatch event after the binding record.
		if e = tx.AdvanceSequence(ctx, p.BotID, rec.LastSequence); e != nil {
			return e
		}
	}
	if err = tx.InsertRun(ctx, botStoredRun{Run: run, RequestHash: hash, Instructions: request.Instructions}, rec.Bot.OwnerID); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"run_id": run.ID, "status": run.Status, "intent": store.BotOutboxDispatch, "actor_id": actor, "proposal_id": p.ID, "source_user_id": p.SourceUserID})
	if err != nil {
		return err
	}
	return insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: p.BotID, RunID: &run.ID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityRun, Payload: data, CreatedAt: now}, "dispatch:"+run.ID.String())
}
