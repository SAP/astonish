package daemon

import (
	"context"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// ownerEmailResolver never accepts an owner/tenant selector from model arguments.
// The closure is per run; account, link and transport facts are live per call.
func (w *BotWorker) ownerEmailResolver(r bot.RunRequest) func(context.Context) (store.BotOwnerEmailDelivery, error) {
	return func(ctx context.Context) (store.BotOwnerEmailDelivery, error) {
		p, ok := execution.PrincipalFromContext(ctx)
		if !ok || p.Subject != r.Bot.OwnerID.String() || p.Subject != r.Principal.Subject || p.OrgSlug != r.Principal.OrgSlug || p.TeamSlug != r.Principal.TeamSlug {
			return store.BotOwnerEmailDelivery{}, store.ErrBotForbidden
		}
		ready := false
		if w.config.ChannelManager != nil {
			if manager := w.config.ChannelManager(); manager != nil {
				ready = manager.Status()[store.BotDeliveryChannelEmail].Connected
			}
		}
		return store.ResolveBotOwnerEmailDelivery(ctx, w.config.Backend, r.Bot.OwnerID, ready)
	}
}
