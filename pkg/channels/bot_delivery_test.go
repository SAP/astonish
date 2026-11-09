package channels

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type deliveryVerifier func(context.Context, store.BotID, store.BotChannelBinding) error

func (f deliveryVerifier) AuthorizeBotDelivery(ctx context.Context, id store.BotID, target store.BotChannelBinding) error {
	return f(ctx, id, target)
}

type deliveryCapture struct {
	Channel
	calls  int
	target Target
}

func (*deliveryCapture) ID() string { return "slack" }
func (c *deliveryCapture) Send(_ context.Context, target Target, _ OutboundMessage) error {
	c.calls++
	c.target = target
	return nil
}

func TestBotNotificationsScopedDestinationGate(t *testing.T) {
	manager := NewChannelManager(nil, nil, nil, nil)
	capture := &deliveryCapture{}
	manager.Register(capture)
	id := uuid.New()
	target := store.BotChannelBinding{ID: uuid.New(), Channel: "slack", ChatID: "TONE:CONE", ThreadID: "thread"}
	msg := OutboundMessage{Text: "private report"}
	ctx := context.Background()
	if err := manager.SendBotNotification(ctx, nil, id, target, msg); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	revoked := false
	verify := deliveryVerifier(func(_ context.Context, bot store.BotID, binding store.BotChannelBinding) error {
		if revoked || bot != id || binding != target {
			return store.ErrBotForbidden
		}
		return nil
	})
	foreign := target
	foreign.ChatID = "TOTHER:COTHER"
	if err := manager.SendBotNotification(ctx, verify, id, foreign, msg); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	if capture.calls != 0 {
		t.Fatal("global Slack transport received unauthorized payload")
	}
	if err := manager.SendBotNotification(ctx, verify, id, target, msg); err != nil {
		t.Fatal(err)
	}
	revoked = true
	if err := manager.SendBotNotification(ctx, verify, id, target, msg); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	if capture.calls != 1 || capture.target.ChatID != target.ChatID || capture.target.ThreadID != target.ThreadID {
		t.Fatal("wrong scoped transport call")
	}
	// Existing ordinary replies retain their original contract.
	if err := manager.Send(ctx, Target{ChannelID: "slack", ChatID: "ordinary"}, msg); err != nil {
		t.Fatal(err)
	}
	if capture.calls != 2 {
		t.Fatal("ordinary channel behavior changed")
	}
}
