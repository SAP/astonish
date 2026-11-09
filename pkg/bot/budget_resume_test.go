package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestRestoreRunBudgetCumulativeCaps(t *testing.T) {
	cap := int64(10)
	for _, tc := range []struct {
		name  string
		usage store.BotUsage
	}{
		{"tokens", store.BotUsage{InputTokens: 7, OutputTokens: 3, CostMicros: new(int64)}},
		{"tools", store.BotUsage{ToolCalls: 10, CostMicros: new(int64)}},
		{"cost", store.BotUsage{CostMicros: &cap}},
		{"runtime", store.BotUsage{RuntimeNanos: int64(time.Minute), CostMicros: new(int64)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := RestoreRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &cap, MaxToolCalls: &cap, MaxModelCostMicros: &cap}, time.Now(), tc.usage)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Check(context.Background()); !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("resumed cap reset: %v", err)
			}
		})
	}
}

func TestRestoreRunBudgetTotalsAndUnknownCost(t *testing.T) {
	cost := int64(5)
	prior := store.BotUsage{InputTokens: 3, OutputTokens: 2, ToolCalls: 4, CostMicros: &cost, RuntimeNanos: int64(20 * time.Second)}
	start := time.Now()
	b, err := RestoreRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, start, prior)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Deadline().Equal(start.Add(40 * time.Second)) {
		t.Fatal("runtime allowance reset")
	}
	r, err := b.ReserveModel(context.Background(), execution.BudgetReservation{InputTokens: 2, OutputTokens: 1, CostMicros: &cost})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SettleModel(context.Background(), r.ID, execution.BudgetUsage{InputTokens: 2, OutputTokens: 1, CostMicros: &cost}); err != nil {
		t.Fatal(err)
	}
	if err := b.ReserveTool(context.Background(), "read"); err != nil {
		t.Fatal(err)
	}
	u := b.Usage()
	if u.InputTokens != 5 || u.OutputTokens != 3 || u.ToolCalls != 5 || u.CostMicros == nil || *u.CostMicros != 10 || u.RuntimeNanos < prior.RuntimeNanos {
		t.Fatalf("not cumulative: %+v", u)
	}
	prior.CostMicros = nil
	b, err = RestoreRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxModelCostMicros: &cost}, start, prior)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Check(context.Background()); !errors.Is(err, ErrBudgetUnknownCost) {
		t.Fatal(err)
	}
	if b.Usage().CostMicros != nil {
		t.Fatal("unknown cost fabricated")
	}
}
