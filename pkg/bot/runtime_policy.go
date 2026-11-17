package bot

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"time"
)

// ProgressExecutionEnabled excludes title clarification, which has no work
// assignment yet and resumes using the ordinary owner-answer checkpoint.
func ProgressExecutionEnabled(req RunRequest) bool {
	return req.Version.Budget.RuntimePolicy.EffectiveMode() == execution.BotRuntimeModeProgressV1 &&
		(req.Run.TitleConfirmation == nil || req.Run.TitleConfirmation.State != store.BotTitleConfirmationNeedsQuestion)
}

// RunContext never starts a new allowance. Production meters supply their actual
// cumulative deadline; legacy adapters without a meter use the pinned ledger.
// A zero deadline explicitly means no whole-task timer, not no cancellation.
func RunContext(ctx context.Context, spec store.BudgetSpec, budget execution.RunBudget, usage store.BotUsage, authorityExpiry time.Time) (context.Context, context.CancelFunc, error) {
	fallback, err := RestoreRunBudget(spec, time.Now(), usage)
	if err != nil {
		return nil, nil, err
	}
	deadline := fallback.Deadline()
	if bounded, ok := budget.(interface{ Deadline() time.Time }); ok {
		actual := bounded.Deadline()
		if !actual.IsZero() && (deadline.IsZero() || actual.Before(deadline)) {
			deadline = actual
		}
	}
	if !authorityExpiry.IsZero() && (deadline.IsZero() || authorityExpiry.Before(deadline)) {
		deadline = authorityExpiry
	}
	if deadline.IsZero() {
		c, cancel := context.WithCancel(ctx)
		return c, cancel, nil
	}
	c, cancel := context.WithDeadline(ctx, deadline)
	return c, cancel, nil
}
