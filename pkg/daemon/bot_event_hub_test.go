package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

func TestBotEventHubCoalescingAndIsolation(t *testing.T) {
	hub := NewBotEventHub()
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), OrgSlug: "org", TeamSlug: "team"}
	e := bot.RuntimeEvent{RunID: id.RunID, SessionID: uuid.New()}
	ctx := execution.WithBotRunIdentity(context.Background(), id)
	key := api.BotEventSubscription{OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, BotID: id.BotID, SessionID: e.SessionID}
	wake, cancel := hub.SubscribeBotEvents(key)
	defer cancel()
	other := key
	other.OrgSlug = "other"
	isolated, closeOther := hub.SubscribeBotEvents(other)
	defer closeOther()
	for i := 0; i < 10000; i++ {
		if err := hub.Publish(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if len(wake) != 1 {
		t.Fatal("wakes not coalesced")
	}
	select {
	case <-isolated:
		t.Fatal("cross-tenant wake")
	default:
	}
	<-wake
	if err := hub.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("subsequent commit not signaled")
	}
	cancel()
	cancel()
	if err := hub.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
		t.Fatal("cancelled subscriber signaled")
	default:
	}
	if err := hub.Publish(context.Background(), e); err == nil {
		t.Fatal("unauthenticated publish")
	}
}

func TestBotEventHubDefaultPublisher(t *testing.T) {
	publish := installBotEventPublisher(nil)
	if publish == nil {
		t.Fatal("missing default publisher")
	}
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), OrgSlug: "org", TeamSlug: "team"}
	e := bot.RuntimeEvent{RunID: id.RunID, SessionID: uuid.New()}
	wake, cancel := defaultBotEventHub.SubscribeBotEvents(api.BotEventSubscription{OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, BotID: id.BotID, SessionID: e.SessionID})
	defer cancel()
	if err := publish(execution.WithBotRunIdentity(context.Background(), id), e); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("default publication not wired")
	}
	customErr := errors.New("optional publisher unavailable")
	publish = installBotEventPublisher(func(context.Context, bot.RuntimeEvent) error { return customErr })
	if err := publish(execution.WithBotRunIdentity(context.Background(), id), e); !errors.Is(err, customErr) {
		t.Fatalf("custom error lost: %v", err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("custom publisher failure suppressed default wake")
	}
}
