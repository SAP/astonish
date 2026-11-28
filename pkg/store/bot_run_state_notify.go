package store

import "context"

type botRunStateNotifierKey struct{}

// WithBotRunStateNotifier supplies a wakeup accelerator for committed run-state
// metadata. The callback must be nonblocking; durable transcript catchup remains
// authoritative. It is never invoked for a rolled-back transaction. Consumers
// must reauthorize replay; notification is not read authority.
func WithBotRunStateNotifier(ctx context.Context, notify func(context.Context, BotRunState)) context.Context {
	return context.WithValue(ctx, botRunStateNotifierKey{}, notify)
}

// BotRunStateNotifierFromContext returns the scoped post-commit wakeup callback.
func BotRunStateNotifierFromContext(ctx context.Context) func(context.Context, BotRunState) {
	notify, _ := ctx.Value(botRunStateNotifierKey{}).(func(context.Context, BotRunState))
	return notify
}
