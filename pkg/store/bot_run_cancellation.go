package store

import "context"

// BotRunCancellationStore stops only the identified run, never the Bot or its
// schedules. Running attempts stay interrupted until trusted physical teardown
// is confirmed through BotCleanupStore; cancellation is not a destruction receipt.
type BotRunCancellationStore interface {
	CancelRun(context.Context, BotID, RunID) (*BotRun, error)
}
