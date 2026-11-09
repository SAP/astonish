package bot

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"testing"
)

func TestSharedRunBudgetObservedScheduleSettlementKeepsFiniteCap(t *testing.T) {
	b := testRunBudget(t, budgetInt(100000), nil, nil)
	ctx := context.Background()
	r, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 76824, OutputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SettleModel(ctx, r.ID, execution.BudgetUsage{InputTokens: 32824, OutputTokens: 194}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 76824, OutputTokens: 4096}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("raw bytes must not bypass remaining 66982 tokens: %v", err)
	}
	// Authoritative unchanged-prefix tokens plus a conservative appended bound
	// fit; the meter itself neither guesses tokenization nor enlarges the cap.
	r, err = b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 34000, OutputTokens: 40000})
	if err != nil {
		t.Fatal(err)
	}
	if r.OutputTokens != 32982 {
		t.Fatalf("output not clamped to finite remaining allowance: %+v", r)
	}
	if u := b.Usage(); u.InputTokens+u.OutputTokens != 100000 {
		t.Fatalf("held=%+v", u)
	}
	if _, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 1, OutputTokens: 1}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("outstanding reservation bypassed: %v", err)
	}
	if err := b.SettleModel(ctx, r.ID, execution.BudgetUsage{InputTokens: 34000, OutputTokens: 32983}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("actual overrun not enforced: %v", err)
	}
	if err := b.Check(ctx); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("overrun must block subsequent work: %v", err)
	}
}
