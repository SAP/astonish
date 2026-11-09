package launcher

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"time"
)

func (b *botPlatformBackend) DecideBotOperation(ctx context.Context, v store.BotOperationView, approve bool) error {
	bot, sid := b.watchSelection()
	if bot == nil || bot.ID != v.BotID || bot.HomeSessionID != v.HomeSessionID || sid != bot.HomeSessionID || v.Validate() != nil || v.Status != store.BotOperationBlocked || v.Decision != store.BotOperationRequireAuthorization || v.Exception == nil || v.Exception.OperationID != v.OperationID || !v.Exception.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("invalid bound operation exception")
	}
	_, err := b.bots.DecideOperation(ctx, bot.ID.String(), client.BotOperationDecision{OperationID: v.OperationID, PayloadDigest: v.Exception.PayloadDigest, ExpectedRevision: v.Revision, ExpiresAt: v.Exception.ExpiresAt, Approve: approve})
	return err
}
