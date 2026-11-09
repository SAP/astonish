package store

import "context"

// BotActivationReadiness is installed by trusted server composition. It checks
// the exact persisted snapshot; it is not request authority or a JSON DTO.
// The repository revalidates authorization and revision after it returns.
type BotActivationReadiness func(context.Context, Bot, BotVersion) error

// BotActivationInstaller is an optional backend capability, not a BotStore
// operation. Nil removes readiness and leaves activation fail closed.
type BotActivationInstaller interface {
	SetBotActivationReadiness(BotActivationReadiness)
}
