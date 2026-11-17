package api

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
)

func TestBotGenerationHealthSetupRemainsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, progress, _, _, _ := progressContextFixture(t)
		req := progress.req
		req.Version.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
		budget, err := bot.NewRunBudget(req.Version.Budget, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		req.Budget = budget
		called := false
		adapter, err := NewBotChatRuntimeAdapter(func(c context.Context, _ bot.RunRequest) (*BotChatResources, error) {
			called = true
			<-c.Done()
			return nil, c.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err = adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
		if !called || err == nil || time.Since(start) != 120*time.Second {
			t.Fatalf("setup called=%v err=%v elapsed=%s", called, err, time.Since(start))
		}
	})
}
