package email

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/channels"
	emailpkg "github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type botEmailProof func(context.Context, store.BotID, store.BotChannelBinding) error

func (f botEmailProof) AuthorizeBotDelivery(c context.Context, id store.BotID, b store.BotChannelBinding) error {
	return f(c, id, b)
}

type botEmailClient struct {
	captureClient
	sends, replies int
}

func (c *botEmailClient) Send(ctx context.Context, m emailpkg.OutgoingMessage) (string, error) {
	c.sends++
	return c.captureClient.Send(ctx, m)
}
func (c *botEmailClient) Reply(ctx context.Context, id string, all bool, m emailpkg.OutgoingMessage) (string, error) {
	c.replies++
	return c.captureClient.Reply(ctx, id, all, m)
}

// This executes the production manager and native EmailChannel with an isolated
// email.Client. No mailbox, credentials, network service or real mail is used.
func TestBotOwnerEmailNativeTransport(t *testing.T) {
	ctx := context.Background()
	manager := channels.NewChannelManager(nil, nil, nil, nil)
	client := &botEmailClient{}
	ch := &EmailChannel{client: client, status: channels.ChannelStatus{Connected: true}}
	manager.Register(ch)
	id := uuid.New()
	target := store.BotChannelBinding{ID: uuid.New(), Channel: store.BotDeliveryChannelEmail, ChatID: "owner@example.test"}
	revoked := false
	authority := botEmailProof(func(_ context.Context, bid store.BotID, b store.BotChannelBinding) error {
		if revoked || bid != id || b != target {
			return store.ErrBotForbidden
		}
		return nil
	})
	msg := channels.OutboundMessage{Text: "private report", ReplyTo: "untrusted-message-with-other-recipient", ThreadID: "untrusted-thread"}
	if err := manager.SendBotNotification(ctx, authority, id, target, msg); err != nil {
		t.Fatal(err)
	}
	if client.sends != 1 || client.replies != 0 || len(client.last.To) != 1 || client.last.To[0] != target.ChatID || client.last.Body != msg.Text {
		t.Fatalf("unexpected email: %+v sends=%d replies=%d", client.last, client.sends, client.replies)
	}
	bad := target
	bad.ChatID = "other@example.test"
	if err := manager.SendBotNotification(ctx, authority, id, bad, msg); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	revoked = true
	if err := manager.SendBotNotification(ctx, authority, id, target, msg); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	revoked = false
	ch.status.Connected = false
	if err := manager.SendBotNotification(ctx, authority, id, target, msg); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
	empty := channels.NewChannelManager(nil, nil, nil, nil)
	if err := empty.SendBotNotification(ctx, authority, id, target, msg); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
	if client.sends != 1 || client.replies != 0 {
		t.Fatal("denied mail reached transport")
	}
	// Ordinary reply threading is deliberately unchanged.
	if err := manager.Send(ctx, channels.Target{ChannelID: "email", ChatID: target.ChatID}, msg); err != nil {
		t.Fatal(err)
	}
	if client.replies != 1 {
		t.Fatal("ordinary reply path changed")
	}
}
