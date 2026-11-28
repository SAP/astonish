package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type notificationAuthorityFunc func(context.Context, store.BotID, store.BotChannelBinding) error

func (f notificationAuthorityFunc) AuthorizeBotDelivery(c context.Context, id store.BotID, b store.BotChannelBinding) error {
	return f(c, id, b)
}

func TestBotNotificationsTransportRequiresLiveScopeAuthority(t *testing.T) {
	capture := &workerCaptureChannel{t: t}
	manager := channels.NewChannelManager(nil, nil, nil, nil)
	manager.Register(capture)
	n := botChannelNotifier{getManager: func() *channels.ChannelManager { return manager }}
	q := bot.NotificationRequest{ID: uuid.New(), BotID: uuid.New(), Decision: store.NotificationSend, Target: store.BotChannelBinding{ID: uuid.New(), Channel: "controlled", ChatID: "tenant-B-room"}, Body: "private tenant A report"}
	if _, err := n.Send(context.Background(), q); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("missing verifier: %v", err)
	}
	revoked := false
	approved := q.Target
	approved.ChatID = "tenant-A-room"
	n.authority = notificationAuthorityFunc(func(_ context.Context, id store.BotID, b store.BotChannelBinding) error {
		if revoked || id != q.BotID || b != approved {
			return store.ErrBotForbidden
		}
		return nil
	})
	if _, err := n.Send(context.Background(), q); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("cross tenant: %v", err)
	}
	if len(capture.targets) != 0 {
		t.Fatal("unauthorized payload reached global transport")
	}
	q.Target = approved
	if _, err := n.Send(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	revoked = true
	if _, err := n.Send(context.Background(), q); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("revoked: %v", err)
	}
	if len(capture.targets) != 1 {
		t.Fatal("revoked binding sent")
	}
}
