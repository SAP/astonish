package tools

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
)

// The model-facing scheduler cannot mutate Bot-owned rows, including changing
// their mode to disguise the ownership. The durable scheduler itself is unchanged.
type botGuardedScheduler struct{ store.SchedulerStore }

func guardBotSchedules(s store.SchedulerStore) store.SchedulerStore {
	if s == nil {
		return nil
	}
	return botGuardedScheduler{s}
}
func (s botGuardedScheduler) Add(ctx context.Context, j *store.ScheduledJob) error {
	if store.BotOwnedSchedule(j) {
		return store.ErrBotForbidden
	}
	return s.SchedulerStore.Add(ctx, j)
}
func (s botGuardedScheduler) Update(ctx context.Context, j *store.ScheduledJob) error {
	if store.BotOwnedSchedule(j) || store.BotOwnedSchedule(s.Get(ctx, j.ID)) {
		return store.ErrBotForbidden
	}
	return s.SchedulerStore.Update(ctx, j)
}
func (s botGuardedScheduler) Remove(ctx context.Context, id string) error {
	if store.BotOwnedSchedule(s.Get(ctx, id)) {
		return store.ErrBotForbidden
	}
	return s.SchedulerStore.Remove(ctx, id)
}
