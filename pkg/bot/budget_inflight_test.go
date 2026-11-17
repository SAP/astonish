package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestSharedRunBudgetInFlightCeilingAndOverrun(t *testing.T) {
	for _, dimension := range []string{"tokens", "cost"} {
		t.Run(dimension, func(t *testing.T) {
			ctx := context.Background()
			cap := int64(110)
			spec := store.BudgetSpec{MaxRuntimeSeconds: 60}
			if dimension == "tokens" {
				spec.MaxTokens = &cap
			} else {
				spec.MaxModelCostMicros = &cap
			}
			b, err := NewRunBudget(spec, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			r, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 10, OutputTokens: 100, CostMicros: &cap})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.CheckInFlight(ctx); err != nil {
				t.Fatalf("valid reservation: %v", err)
			}
			if !errors.Is(b.Check(ctx), ErrBudgetExceeded) {
				t.Fatal("admission allowed at cap")
			}
			if _, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 1, OutputTokens: 1, CostMicros: &cap}); !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("new reservation: %v", err)
			}
			over := cap + 1
			if err := b.SettleModel(ctx, r.ID, execution.BudgetUsage{InputTokens: 10, OutputTokens: 101, CostMicros: &over}); !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("overrun settlement: %v", err)
			}
			if !errors.Is(b.CheckInFlight(ctx), ErrBudgetExceeded) {
				t.Fatal("overrun ignored in flight")
			}
		})
	}
}

func TestSharedRunBudgetInFlightStillFailsClosed(t *testing.T) {
	ctx := context.Background()
	var zero SharedRunBudget
	if zero.CheckInFlight(ctx) == nil {
		t.Fatal("zero value accepted")
	}
	cap := int64(1)
	b, err := NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxToolCalls: &cap}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ReserveTool(ctx, "read_file"); err != nil {
		t.Fatal(err)
	}
	if err := b.CheckInFlight(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.ReserveTool(ctx, "read_file"); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("tool admitted at cap: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if !errors.Is(b.CheckInFlight(cancelled), context.Canceled) {
		t.Fatal("cancellation ignored")
	}
	b.deadline = time.Now().Add(-time.Second)
	if !errors.Is(b.CheckInFlight(ctx), ErrBudgetExceeded) {
		t.Fatal("deadline ignored")
	}
	// An embedded local meter must not bypass the mandate's authority check.
	m := &MandateBudget{SharedRunBudget: b}
	if !errors.Is(m.CheckInFlight(ctx), store.ErrBotForbidden) {
		t.Fatal("mandate authority bypassed")
	}
}
