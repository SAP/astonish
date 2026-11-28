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
	if binding.Channel == store.BotDeliveryChannelEmail {
		if m == nil || !m.Status()[store.BotDeliveryChannelEmail].Connected {
			return fmt.Errorf("%w: email channel transport is not configured or connected", store.ErrBotUnsupported)
		}
		// Reply resolves recipients from mailbox content; use only the reviewed address.
		message.ReplyTo = ""
	}
	target := Target{ChannelID: binding.Channel, ChatID: binding.ChatID, ThreadID: binding.ThreadID}
	message.ThreadID = binding.ThreadID
	return m.Send(ctx, target, message)
}
