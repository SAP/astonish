package bot

import (
	"context"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// ProgressExecutionEnabled excludes title clarification, which has no work
// assignment yet and resumes using the ordinary owner-answer checkpoint.
func ProgressExecutionEnabled(req RunRequest) bool {
	return req.Version.Budget.RuntimePolicy.EffectiveMode() == execution.BotRuntimeModeProgressV1 &&
		(req.Run.TitleConfirmation == nil || req.Run.TitleConfirmation.State != store.BotTitleConfirmationNeedsQuestion)
}

// RunContext respects the supplied meter's deadline, including explicit zero.
// Legacy adapters without a deadline meter use the pinned ledger. Authority
// expiry and parent cancellation remain independent of local execution caps.
func RunContext(ctx context.Context, spec store.BudgetSpec, budget execution.RunBudget, usage store.BotUsage, authorityExpiry time.Time) (context.Context, context.CancelFunc, error) {
	var deadline time.Time
	if bounded, ok := budget.(interface{ Deadline() time.Time }); ok {
		deadline = bounded.Deadline()
	} else {
		fallback, err := RestoreRunBudget(spec, time.Now(), usage)
		if err != nil {
			return nil, nil, err
		}
		deadline = fallback.Deadline()
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
