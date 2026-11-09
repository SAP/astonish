package store

import (
	"context"
	"testing"
)

func TestBotRunStateNotifierContextIsolation(t *testing.T) {
	base := context.Background()
	if BotRunStateNotifierFromContext(base) != nil {
		t.Fatal("unexpected notifier")
	}
	calls := 0
	ctx := WithBotRunStateNotifier(base, func(context.Context, BotRunState) { calls++ })
	BotRunStateNotifierFromContext(ctx)(ctx, BotRunState{})
	if calls != 1 || BotRunStateNotifierFromContext(base) != nil {
		t.Fatal("notifier leaked context")
	}
	if BotRunStateNotifierFromContext(WithBotRunStateNotifier(ctx, nil)) != nil {
		t.Fatal("nil callback did not override")
	}
}
