package entstore

import (
	"context"
	"strings"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// SetBotActivationReadiness is trusted backend composition, never request data.
// Existing scoped stores observe replacement/removal without rebuilding routers.
func (s *Store) SetBotActivationReadiness(check store.BotActivationReadiness) {
	s.botActivationMu.Lock()
	defer s.botActivationMu.Unlock()
	s.botActivationReadiness = check
}
func (s *Store) checkBotActivation(ctx context.Context, b store.Bot, v store.BotVersion) error {
	s.botActivationMu.RLock()
	check := s.botActivationReadiness
	s.botActivationMu.RUnlock()
	if check == nil {
		return store.ErrBotUnsupported
	}
	return check(ctx, b, v)
}

// Every enabled trigger must retain its exact scoped durable binding, checked
// again inside the CAS. Disabled projections remain valid for an Active Bot;
// paused/stopped schedules stay inert on resume (not recurrence consent).
func validateActivationTriggers(ctx context.Context, db botDB, b store.Bot, v store.BotVersion) error {
	for _, tr := range v.Triggers {
		// A reviewed disabled cron has a server-assigned schedule identity;
		// legacy/raw version triggers are not a reviewed recurrence binding.
		if tr.Kind == store.TriggerCron && (tr.ScheduleID == nil || *tr.ScheduleID == uuid.Nil) {
			return store.ErrBotUnsupported
		}
		if !tr.Enabled {
			continue
		}
		if tr.Kind != store.TriggerCron {
			return store.ErrBotUnsupported
		}
		if tr.ID == uuid.Nil || tr.ScheduleID == nil || *tr.ScheduleID == uuid.Nil || strings.TrimSpace(tr.Instructions) == "" {
			return store.ErrBotInvalid
		}
		scheduled, ok := db.(botScheduledDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		job, err := scheduled.readBotSchedule(ctx, *tr.ScheduleID)
		if err != nil {
			return err
		}
		if job == nil {
			return store.ErrBotConflict
		}
		lifecycleDisabled := !job.Enabled && job.NextRun == nil
		if (b.Status == store.BotPaused || b.Status == store.BotStopped) && !lifecycleDisabled {
			return store.ErrBotConflict
		}
		if (!lifecycleDisabled && (!job.Enabled || job.NextRun == nil)) || job.Mode != "bot_trigger" || job.Scope != string(b.Scope) || job.OwnerID != b.OwnerID.String() || job.Payload.BotID != b.ID.String() || job.Payload.BotVersion != v.Version || job.Payload.BotTriggerID != tr.ID.String() || job.Payload.Flow != "" || job.Payload.Instructions != "" || len(job.Payload.Params) != 0 || job.Schedule.Cron != tr.Cron || job.Schedule.Timezone != tr.Timezone {
			return store.ErrBotConflict
		}
	}
	return nil
}
