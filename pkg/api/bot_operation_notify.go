package api

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// WithBotOperationWakeups composes an accelerator with existing callbacks.
// The store invokes it only after SQL commit; replay still reauthorizes readers.
func WithBotOperationWakeups(ctx context.Context) context.Context {
	prior := store.BotOperationNotifierFromContext(ctx)
	return store.WithBotOperationNotifier(ctx, func(c context.Context, v store.BotOperationView) {
		if prior != nil {
			prior(c, v)
		}
		p, ok := execution.PrincipalFromContext(c)
		if !ok {
			return
		}
		botEventSource.RLock()
		source := botEventSource.source
		botEventSource.RUnlock()
		if wake, ok := source.(interface{ WakeBotEvents(BotEventSubscription) }); ok {
			wake.WakeBotEvents(BotEventSubscription{OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, BotID: v.BotID, SessionID: v.HomeSessionID})
		}
	})
}
