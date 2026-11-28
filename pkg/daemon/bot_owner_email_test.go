package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type ownerEmailReadyChannel struct{ channels.Channel }

func (ownerEmailReadyChannel) ID() string { return store.BotDeliveryChannelEmail }
func (ownerEmailReadyChannel) Status() channels.ChannelStatus {
	return channels.ChannelStatus{Connected: true}
}

func TestBotOwnerEmailResolverCapturedScopeAndReload(t *testing.T) {
	backend, owner := setupChannelResolverFixture(t, "email-org", "email-team", "email", "test@example.com")
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "email-org", TeamSlug: "email-team", Authenticated: true}
	ctx, err := execution.WithPrincipal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	manager := channels.NewChannelManager(nil, nil, nil, nil)
	manager.Register(ownerEmailReadyChannel{})
	w := &BotWorker{config: BotWorkerConfig{Backend: backend, ChannelManager: func() *channels.ChannelManager { return manager }}}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		resolve := w.ownerEmailResolver(bot.RunRequest{Bot: store.Bot{OwnerID: uuid.MustParse(owner), Scope: scope}, Principal: p})
		got, err := resolve(ctx)
		if err != nil || got.State != store.BotOwnerEmailReady || got.Binding == nil {
			t.Fatalf("%+v %v", got, err)
		}
		saved := manager
		manager = nil
		got, err = resolve(ctx)
		if err != nil || got.State != store.BotOwnerEmailTransportUnavailable || got.Binding != nil {
			t.Fatalf("stale transport: %+v %v", got, err)
		}
		manager = saved
		for _, field := range []string{"owner", "org", "team"} {
			foreign := p
			switch field {
			case "owner":
				foreign.Subject = uuid.NewString()
			case "org":
				foreign.OrgSlug = "other"
			case "team":
				foreign.TeamSlug = "other"
			}
			c, err := execution.WithPrincipal(context.Background(), foreign)
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolve(c)
			if !errors.Is(err, store.ErrBotForbidden) || got.AccountEmail != "" {
				t.Fatalf("foreign %s: %+v %v", field, got, err)
			}
		}
	}
	links, err := backend.UserChannels().ListByUser(ctx, owner)
	if err != nil || len(links) != 1 {
		t.Fatalf("links %v %v", links, err)
	}
	if err := backend.UserChannels().Unlink(ctx, links[0].ID); err != nil {
		t.Fatal(err)
	}
	resolve := w.ownerEmailResolver(bot.RunRequest{Bot: store.Bot{OwnerID: uuid.MustParse(owner)}, Principal: p})
	got, err := resolve(ctx)
	if err != nil || got.State != store.BotOwnerEmailNeedsVerification || got.Binding != nil || got.AccountEmail != "test@example.com" {
		t.Fatalf("revoked: %+v %v", got, err)
	}
}
