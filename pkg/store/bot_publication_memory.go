package store

import "context"

// BotMemoryPublication is trusted committed metadata, not a model-authored index.
type BotMemoryPublication struct {
	Publication BotPublication
	Goal        string
	Status      RunStatus
}

// BotPublicationMemoryStore separates post-commit index maintenance from run
// execution authority. Retrying these operations never invokes the model.
type BotPublicationMemoryStore interface {
	ReadMemoryPublication(context.Context, BotID, RunID, int64) (*BotMemoryPublication, error)
	WithBotPublicationFileCommit(context.Context, BotHomeBinding, RunID, int64, func() error) error
}
