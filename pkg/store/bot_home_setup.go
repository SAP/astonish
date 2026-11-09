package store

import "context"

// BotHomeSetupFunc provisions and registers retained storage after live scoped
// owner/admin authorization. It accepts intent, never a caller-supplied binding.
type BotHomeSetupFunc func(context.Context, BotStore, BotID, int64) (*Bot, error)
