package entstore

import (
	"context"

	"github.com/SAP/astonish/pkg/store"
)

// Validate in the same transaction and owner/lease/version scope used to capture
// the operation, before writing its grant or pending/blocked record. This is a
// read-only identity check, not permission to resume: application still rechecks
// current bindings, source, revision, ownership and the original task grant.
func (s *botAuthorizedOperationRuntime) validateResumeCaptureTargets(ctx context.Context, tx botDraftDB, rec *botStoredRecord, targets *store.BotControlResumeTargets) error {
	if targets == nil {
		return store.ErrBotInvalid
	}
	v, err := tx.ReadVersion(ctx, rec.Bot.ID, rec.Bot.Version)
	if err != nil {
		return err
	}
	d, ok := tx.(botTriggerDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	for _, id := range targets.ScheduleIDs {
		found := false
		for _, tr := range v.Triggers {
			if tr.ScheduleID == nil || *tr.ScheduleID != id {
				continue
			}
			if tr.Kind != store.TriggerCron || !tr.Enabled {
				return &store.BotResumeTargetError{ScheduleID: id}
			}
			// Use the existing binding guard; never search other scopes by supplied ID.
			if err := s.repo.validateControlScheduleBindings(ctx, d.triggerScheduler(), rec.Bot, []store.TriggerSpec{tr}, v.Version); err != nil {
				return err
			}
			found = true
			break
		}
		if !found {
			return &store.BotResumeTargetError{ScheduleID: id}
		}
	}
	return nil
}
