package store

import (
	"context"
	"time"
)

// BotScheduleFailureOutcome distinguishes a retryable admission failure from a
// terminal denial. Unknown errors remain retryable; consumers must not classify
// denials by matching error strings.
type BotScheduleFailureOutcome string

const (
	BotScheduleRetry    BotScheduleFailureOutcome = "retry"
	BotScheduleTerminal BotScheduleFailureOutcome = "terminal"
)

// BotScheduleRejected carries the repository's failure acknowledgement through
// existing callback error wrappers. Acknowledged also covers a CAS miss: a newer
// or disabled occurrence belongs to its new owner and must not be overwritten.
type BotScheduleRejected struct {
	Cause        error
	Outcome      BotScheduleFailureOutcome
	Acknowledged bool
}

func (e *BotScheduleRejected) Error() string { return e.Cause.Error() }
func (e *BotScheduleRejected) Unwrap() error { return e.Cause }

// BotScheduleFailureStore is optional so legacy stores remain source compatible.
// Implementations compare enabled state, Bot/trigger/version and exact NextRun;
// only failure runtime fields may change. Terminal stops this exact schedule,
// never a concurrently edited or rebound schedule. Retry retains NextRun.
type BotScheduleFailureStore interface {
	FailBotSchedule(context.Context, BotScheduledOccurrence, time.Time, string, BotScheduleFailureOutcome) (bool, error)
}
