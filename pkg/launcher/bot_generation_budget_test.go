package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotFactoryBindingInFlightBudgetRetainsAuthority(t *testing.T) {
	f := newBotFactoryFixture(t)
	cap := int64(110)
	budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &cap}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = budget
	checks := 0
	b := &botFactoryBinding{owner: store.WithServices(f.ctx, f.services), req: f.req, verifyIsolation: func(context.Context) error { checks++; return nil }}
	if _, err := budget.ReserveModel(f.ctx, execution.BudgetReservation{InputTokens: 10, OutputTokens: 100}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(b.check(f.ctx), bot.ErrBudgetExceeded) {
		t.Fatal("normal admission accepted at ceiling")
	}
	if err := b.checkDuringGeneration(f.ctx); err != nil {
		t.Fatalf("healthy admitted generation: %v", err)
	}
	if checks != 1 {
		t.Fatal("isolation bypassed")
	}
	f.req.Run.Lease.Fence++
	if !errors.Is(b.checkDuringGeneration(f.ctx), store.ErrBotConflict) {
		t.Fatal("stale fence accepted")
	}
	f.req.Run.Lease.Fence--
	f.req.Run.Lease.ExpiresAt = time.Now().Add(-time.Second)
	if !errors.Is(b.checkDuringGeneration(f.ctx), store.ErrBotConflict) {
		t.Fatal("expired lease accepted")
	}
	f.req.Run.Lease.ExpiresAt = time.Now().Add(time.Minute)
	f.req.Bot.Status = store.BotPaused
	if !errors.Is(b.checkDuringGeneration(f.ctx), store.ErrBotConflict) {
		t.Fatal("paused bot accepted")
	}
}
