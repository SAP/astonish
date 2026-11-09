package daemon

import (
	"context"
	"fmt"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
)

// BotSchedulerScope is a routing hint set by the daemon's tenant iteration,
// never a principal. Resolve must still authenticate the live owner.
type BotSchedulerScope struct{ OrgSlug, TeamSlug, UserID string }
type botSchedulerScopeKey struct{}

func BotSchedulerScopeFromContext(ctx context.Context) (BotSchedulerScope, bool) {
	scope, ok := ctx.Value(botSchedulerScopeKey{}).(BotSchedulerScope)
	return scope, ok
}
func withBotSchedulerScope(ctx context.Context, scope BotSchedulerScope) context.Context {
	return context.WithValue(ctx, botSchedulerScopeKey{}, scope)
}

// BotSchedulerCallbacks is explicit trusted composition, not auto-approval.
// Resolve must authenticate the live owner and select the scoped store using
// trusted tenant routing. Request owner/scope fields are hints, not credentials.
// Runtime is required to avoid enabling unattended admission without a worker.
// Dispatch/recovery remains owned by the durable worker, not this callback.
type BotSchedulerCallbacks struct {
	installed func()
	Runtime   botdomain.Runtime
	Resolve   func(context.Context, scheduler.BotTriggerRequest) (context.Context, store.BotScheduledRunStore, error)
}

func (c BotSchedulerCallbacks) Admission() scheduler.BotTriggerFunc {
	if c.Runtime == nil || c.Resolve == nil {
		return nil
	}
	return func(ctx context.Context, req scheduler.BotTriggerRequest) (scheduler.BotAdmission, error) {
		ownerCtx, scoped, err := c.Resolve(ctx, req)
		if err != nil {
			return scheduler.BotAdmission{}, err
		}
		if ownerCtx == nil || scoped == nil {
			return scheduler.BotAdmission{}, fmt.Errorf("Bot scheduler owner/store binding unavailable")
		}
		occurrence := store.BotScheduledOccurrence{JobID: req.JobID, BotID: req.BotID, Version: req.Version, TriggerID: req.TriggerID, OccurrenceAt: req.OccurrenceAt, IdempotencyKey: req.IdempotencyKey}
		if polls, ok := scoped.(store.BotHookPollStore); ok {
			handled, receipt, err := polls.PollScheduledHook(ownerCtx, occurrence)
			if err != nil {
				return scheduler.BotAdmission{}, err
			}
			if handled {
				if receipt == nil {
					return scheduler.BotAdmission{Status: scheduler.BotAdmissionSkipped}, nil
				}
				return scheduler.BotAdmission{Status: scheduler.BotAdmissionAdmitted, RunID: receipt.Run.ID}, nil
			}
		}
		receipt, err := scoped.AdmitScheduledRun(ownerCtx, occurrence)
		if err != nil {
			return scheduler.BotAdmission{}, err
		}
		if receipt == nil {
			return scheduler.BotAdmission{}, fmt.Errorf("Bot scheduler admission receipt unavailable")
		}
		status := scheduler.BotAdmissionAdmitted
		if receipt.Duplicate {
			status = scheduler.BotAdmissionDuplicate
		}
		ack := scheduler.BotScheduleAcknowledgement("")
		if receipt.Acknowledged {
			ack = scheduler.BotScheduleAcknowledged
		}
		return scheduler.BotAdmission{Status: status, RunID: receipt.Run.ID, Acknowledgement: ack}, nil
	}
}

// ConfigureBotScheduler installs no defaults. An incomplete composition leaves
// Bot triggers explicitly disabled; legacy jobs are unaffected. The returned
// delivery wrapper must also be used by single-tenant daemon compositions.
func ConfigureBotScheduler(executor *scheduler.Executor, deliver scheduler.DeliverFunc, callbacks BotSchedulerCallbacks) scheduler.DeliverFunc {
	if executor != nil {
		executor.BotTrigger = callbacks.Admission()
		if executor.BotTrigger != nil && callbacks.installed != nil {
			callbacks.installed()
		}
	}
	return scheduler.WithoutBotTriggerDelivery(deliver)
}

// SetBotSchedulerCallbacks must be called before Start. No callback is installed
// by the production constructor until the authenticated runtime is supplied.
func (mts *MultiTenantScheduler) SetBotSchedulerCallbacks(callbacks BotSchedulerCallbacks) {
	mts.deliver = ConfigureBotScheduler(mts.executor, mts.deliver, callbacks)
}
