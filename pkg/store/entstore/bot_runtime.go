package entstore

import (
	"context"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotRuntimeStore = (*personalBotStore)(nil)
var _ store.BotRuntimeStore = (*teamBotStore)(nil)

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
		input = store.BotRunInput{Bot: rec.Bot, Version: *version, Run: saved.Run, Goals: goals, Instructions: saved.Instructions, Attachments: saved.Attachments, ExternalAdmissionValidated: saved.ExternalAdmissionValidated}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &input, nil
}
