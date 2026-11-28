package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestRunAccountingIgnoresHistoricExecutionCaps(t *testing.T) {
	zero := int64(0)
	active := int64(1)
	progress := execution.NewBotProgressRuntimePolicy()
	progress.MaxActiveSeconds = &active
	for _, policy := range []*execution.BotRuntimePolicy{nil, progress} {
		spec := store.BudgetSpec{MaxRuntimeSeconds: 1, MaxTokens: &zero, MaxToolCalls: &zero, MaxModelCostMicros: &zero, RuntimePolicy: policy}
		for _, restored := range []bool{false, true} {
			var b *SharedRunBudget
			var err error
			if restored {
				b, err = RestoreRunAccounting(spec, time.Now(), store.BotUsage{RuntimeNanos: int64(24 * time.Hour), InputTokens: 100, OutputTokens: 50, ToolCalls: 10})
			} else {
				b, err = NewRunAccounting(spec, time.Now())
			}
			if err != nil {
				t.Fatal(err)
			}
			if !b.Deadline().IsZero() || b.TokenLimit() != nil || b.tokens != nil || b.tools != nil || b.cost != nil {
				t.Fatal("accounting retained execution caps")
			}
			if err := b.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			before := b.Usage()
			reservation, err := b.ReserveModel(context.Background(), execution.BudgetReservation{InputTokens: 1000, OutputTokens: 2000})
			if err != nil || reservation.OutputTokens != 2000 {
				t.Fatalf("model reservation clamped: %+v %v", reservation, err)
			}
			if err := b.SettleModel(context.Background(), reservation.ID, execution.BudgetUsage{InputTokens: 1100, OutputTokens: 2100}); err != nil {
				t.Fatal(err)
			}
			if err := b.ReserveTool(context.Background(), "read"); err != nil {
				t.Fatal(err)
			}
			if err := b.CheckInFlight(context.Background()); err != nil {
				t.Fatal(err)
			}
			usage := b.Usage()
			if usage.InputTokens != before.InputTokens+1100 || usage.OutputTokens != before.OutputTokens+2100 || usage.ToolCalls != before.ToolCalls+1 || usage.CostMicros != nil || usage.RuntimeNanos < before.RuntimeNanos {
				t.Fatalf("ledger lost: %+v", usage)
			}
			if policy != nil && (b.ToolCallTimeout() != 120*time.Second || b.GenerationHealth() == nil || *policy.MaxActiveSeconds != active) {
				t.Fatal("operational policy lost or source mutated")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if !errors.Is(b.Check(ctx), context.Canceled) {
				t.Fatal("cancellation lost")
			}
		}
	}
}

func TestRunAccountingPreservesKnownAndUnknownCost(t *testing.T) {
	cost := int64(12)
	for _, prior := range []*int64{nil, &cost} {
		b, err := RestoreRunAccounting(store.BudgetSpec{}, time.Now(), store.BotUsage{CostMicros: prior})
		if err != nil {
			t.Fatal(err)
		}
		r, err := b.ReserveModel(context.Background(), execution.BudgetReservation{InputTokens: 1, OutputTokens: 1, CostMicros: &cost})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.SettleModel(context.Background(), r.ID, execution.BudgetUsage{InputTokens: 1, OutputTokens: 1, CostMicros: &cost}); err != nil {
			t.Fatal(err)
		}
		got := b.Usage().CostMicros
		if prior == nil && got != nil || prior != nil && (got == nil || *got != 24) {
			t.Fatal("cost knowledge changed")
		}
	}
}
