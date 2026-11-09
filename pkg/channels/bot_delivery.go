package channels

import (
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/store"
)

// SendBotNotification is the scoped outbound boundary for autonomous Bot sends.
// Verify against the live routed repository immediately before resolving a global
// transport. Ordinary interactive channel Send behavior remains unchanged.
func (m *ChannelManager) SendBotNotification(ctx context.Context, authority store.BotDeliveryAuthorizer, id store.BotID, binding store.BotChannelBinding, message OutboundMessage) error {
	if authority == nil {
		return fmt.Errorf("%w: notification destination verifier unavailable", store.ErrBotForbidden)
	}
	if err := authority.AuthorizeBotDelivery(ctx, id, binding); err != nil {
		return err
	}
	target := Target{ChannelID: binding.Channel, ChatID: binding.ChatID, ThreadID: binding.ThreadID}
	message.ThreadID = binding.ThreadID
	return m.Send(ctx, target, message)
}
