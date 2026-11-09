package bot

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func budgetInt(v int64) *int64 { return &v }
func testRunBudget(t *testing.T, tokens, tools, cost *int64) *SharedRunBudget {
	t.Helper()
	b, err := NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: tokens, MaxToolCalls: tools, MaxModelCostMicros: cost}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSharedRunBudgetReserveClampAndSettleOnce(t *testing.T) {
	ctx := context.Background()
	limit := int64(100)
	b := testRunBudget(t, &limit, nil, nil)
	limit = 1000
	r, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 20, OutputTokens: 200})
	if err != nil || r.OutputTokens != 80 {
		t.Fatalf("reservation=%+v err=%v", r, err)
	}
	if _, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 1, OutputTokens: 1}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if err := b.SettleModel(ctx, r.ID, execution.BudgetUsage{InputTokens: 20, OutputTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if err := b.SettleModel(ctx, r.ID, execution.BudgetUsage{InputTokens: 500, OutputTokens: 500}); err != nil {
		t.Fatal(err)
	}
	u := b.Usage()
	if u.InputTokens != 20 || u.OutputTokens != 10 || u.CostMicros != nil {
		t.Fatalf("usage=%+v", u)
	}
	if err := b.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSharedRunBudgetUnknownCostAndSnapshot(t *testing.T) {
	ctx := context.Background()
	b := testRunBudget(t, nil, nil, budgetInt(100))
	if _, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 1, OutputTokens: 1}); !errors.Is(err, ErrBudgetUnknownCost) {
		t.Fatal(err)
	}
	cost := int64(50)
	r, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 1, OutputTokens: 1, CostMicros: &cost})
	if err != nil {
		t.Fatal(err)
	}
	cost = 0
	*r.CostMicros = 0
	if u := b.Usage(); u.CostMicros == nil || *u.CostMicros != 50 {
		t.Fatalf("usage=%+v", u)
	}
	if err := b.SettleModel(ctx, r.ID, execution.BudgetUsage{InputTokens: 1, OutputTokens: 1}); !errors.Is(err, ErrBudgetUnknownCost) {
		t.Fatal(err)
	}
	if b.Usage().CostMicros != nil {
		t.Fatal("unknown actual cost reported as known")
	}
	if err := b.Check(ctx); !errors.Is(err, ErrBudgetUnknownCost) {
		t.Fatal(err)
	}
}

func TestSharedRunBudgetConcurrentParentChild(t *testing.T) {
	b := testRunBudget(t, budgetInt(100), budgetInt(10), nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	var models, tools atomic.Int64
	for range 100 {
		wg.Go(func() {
			if _, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 1, OutputTokens: 1}); err == nil {
				models.Add(1)
			}
			if err := b.ReserveTool(ctx, "read_file"); err == nil {
				tools.Add(1)
			}
			_ = b.Usage()
		})
	}
	wg.Wait()
	if models.Load() != 50 || tools.Load() != 10 {
		t.Fatalf("models=%d tools=%d", models.Load(), tools.Load())
	}
}

func TestSharedRunBudgetCancellationExpiryAndInvalid(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := testRunBudget(t, nil, nil, nil)
	r, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 1, OutputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := b.ReserveModel(ctx, execution.BudgetReservation{OutputTokens: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := b.SettleModel(ctx, r.ID, execution.BudgetUsage{InputTokens: 1, OutputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	expired, err := NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 1}, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := expired.Check(context.Background()); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if err := (&SharedRunBudget{}).Check(context.Background()); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if err := b.SettleModel(ctx, uuid.New(), execution.BudgetUsage{}); !errors.Is(err, ErrBudgetReservation) {
		t.Fatal(err)
	}
	for _, spec := range []store.BudgetSpec{{}, {MaxRuntimeSeconds: math.MaxInt64}, {MaxRuntimeSeconds: 1, MaxTokens: budgetInt(-1)}} {
		if _, err := NewRunBudget(spec, time.Now()); err == nil {
			t.Fatalf("accepted %+v", spec)
		}
	}
}

func TestSharedRunBudgetActualOverrunAndZeroLimits(t *testing.T) {
	ctx := context.Background()
	b := testRunBudget(t, budgetInt(10), nil, nil)
	r, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 1, OutputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SettleModel(ctx, r.ID, execution.BudgetUsage{InputTokens: 10, OutputTokens: 10}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if err := b.Check(ctx); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	b = testRunBudget(t, budgetInt(0), budgetInt(0), nil)
	if _, err := b.ReserveModel(ctx, execution.BudgetReservation{OutputTokens: 1}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if err := b.ReserveTool(ctx, "read_file"); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
}
