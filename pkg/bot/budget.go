package bot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var (
	ErrBudgetExceeded    = errors.New("bot run budget exhausted")
	ErrBudgetUnknownCost = errors.New("model cost unknown for cost-limited bot run")
	ErrBudgetReservation = errors.New("invalid bot budget reservation")
)

type modelCharge struct {
	usage   execution.BudgetUsage
	settled bool
	// heldCost remains reserved when actual cost is unknown.
	heldCost int64
}

// SharedRunBudget is a concurrency-safe, per-run meter. Pass the same instance
// to the parent, children, tools and every auxiliary model. It is not a durable
// ledger: callers must fence attempts and restore usage before resuming a run.
// Construct it with NewRunBudget; its zero value fails closed.
type SharedRunBudget struct {
	mu                                         sync.Mutex
	deadline                                   time.Time
	startedAt                                  time.Time
	runtimeUsed                                int64
	tokens, tools, cost                        *int64
	charges                                    map[uuid.UUID]modelCharge
	inputUsed, outputUsed, toolsUsed, costHeld int64
	unknownCosts                               int64
	overflow                                   bool
}

// NewRunBudget snapshots the limits. Daily run admission is a repository concern,
// not a per-run counter. A nil token/tool/cost limit is unlimited; zero forbids use.
func NewRunBudget(spec store.BudgetSpec, startedAt time.Time) (*SharedRunBudget, error) {
	zero := int64(0)
	return RestoreRunBudget(spec, startedAt, store.BotUsage{CostMicros: &zero})
}

// RestoreRunBudget restores the cumulative durable ledger, not a new allowance.
// Unknown cost remains unknown even when the next attempt makes no model calls.
// startedAt is this attempt's start: approval waiting time is not active runtime.
func RestoreRunBudget(spec store.BudgetSpec, startedAt time.Time, usage store.BotUsage) (*SharedRunBudget, error) {
	if usage.RuntimeNanos < 0 || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.ToolCalls < 0 || usage.CostMicros != nil && *usage.CostMicros < 0 {
		return nil, ErrBudgetReservation
	}
	if startedAt.IsZero() || spec.MaxRuntimeSeconds <= 0 || spec.MaxRuntimeSeconds > math.MaxInt64/int64(time.Second) {
		return nil, fmt.Errorf("%w: runtime", ErrBudgetReservation)
	}
	for _, limit := range []*int64{spec.MaxTokens, spec.MaxToolCalls, spec.MaxModelCostMicros, spec.MaxRunsPerDay} {
		if limit != nil && *limit < 0 {
			return nil, fmt.Errorf("%w: negative limit", ErrBudgetReservation)
		}
	}
	b := &SharedRunBudget{startedAt: startedAt, runtimeUsed: usage.RuntimeNanos, deadline: startedAt.Add(time.Duration(spec.MaxRuntimeSeconds)*time.Second - time.Duration(usage.RuntimeNanos)), tokens: copyBudgetInt(spec.MaxTokens), tools: copyBudgetInt(spec.MaxToolCalls), cost: copyBudgetInt(spec.MaxModelCostMicros), charges: make(map[uuid.UUID]modelCharge), inputUsed: usage.InputTokens, outputUsed: usage.OutputTokens, toolsUsed: usage.ToolCalls}
	if usage.CostMicros == nil {
		b.unknownCosts = 1
	} else {
		b.costHeld = *usage.CostMicros
	}
	return b, nil
}

// Deadline lets model decorators cancel in-flight generation at the run limit.
func (b *SharedRunBudget) Deadline() time.Time { return b.deadline }

func copyBudgetInt(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func (b *SharedRunBudget) live(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.charges == nil || !time.Now().Before(b.deadline) {
		return fmt.Errorf("%w: runtime", ErrBudgetExceeded)
	}
	if b.overflow {
		return ErrBudgetExceeded
	}
	return nil
}

func (b *SharedRunBudget) Check(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.live(ctx); err != nil {
		return err
	}
	if b.tokens != nil && (b.inputUsed >= *b.tokens || b.outputUsed >= *b.tokens-b.inputUsed) {
		return fmt.Errorf("%w: tokens", ErrBudgetExceeded)
	}
	if b.tools != nil && b.toolsUsed >= *b.tools {
		return fmt.Errorf("%w: tools", ErrBudgetExceeded)
	}
	if b.cost != nil {
		if b.unknownCosts > 0 {
			return ErrBudgetUnknownCost
		}
		if b.costHeld >= *b.cost {
			return fmt.Errorf("%w: model cost", ErrBudgetExceeded)
		}
	}
	return nil
}

// ReserveModel atomically holds input plus maximum output before generation.
// Output is reduced to the remaining token allowance. Cost is not reduced on a
// clamp: retaining the original estimate is conservative for arbitrary pricing.
func (b *SharedRunBudget) ReserveModel(ctx context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.live(ctx); err != nil {
		return execution.BudgetReservation{}, err
	}
	if r.InputTokens < 0 || r.OutputTokens <= 0 || r.InputTokens > math.MaxInt64-r.OutputTokens || r.CostMicros != nil && *r.CostMicros < 0 {
		return execution.BudgetReservation{}, ErrBudgetReservation
	}
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	if _, exists := b.charges[r.ID]; exists {
		return execution.BudgetReservation{}, ErrBudgetReservation
	}
	if b.tokens != nil {
		if b.inputUsed >= *b.tokens || b.outputUsed >= *b.tokens-b.inputUsed {
			return execution.BudgetReservation{}, fmt.Errorf("%w: tokens", ErrBudgetExceeded)
		}
		remaining := *b.tokens - b.inputUsed - b.outputUsed
		if remaining <= r.InputTokens {
			return execution.BudgetReservation{}, fmt.Errorf("%w: tokens", ErrBudgetExceeded)
		}
		r.OutputTokens = min(r.OutputTokens, remaining-r.InputTokens)
	}
	if b.cost != nil {
		if r.CostMicros == nil || b.unknownCosts > 0 {
			return execution.BudgetReservation{}, ErrBudgetUnknownCost
		}
		if b.costHeld > *b.cost || *r.CostMicros > *b.cost-b.costHeld {
			return execution.BudgetReservation{}, fmt.Errorf("%w: model cost", ErrBudgetExceeded)
		}
	}
	if r.InputTokens > math.MaxInt64-b.inputUsed || r.OutputTokens > math.MaxInt64-b.outputUsed || r.CostMicros != nil && *r.CostMicros > math.MaxInt64-b.costHeld {
		return execution.BudgetReservation{}, ErrBudgetExceeded
	}
	r.CostMicros = copyBudgetInt(r.CostMicros)
	charge := modelCharge{usage: execution.BudgetUsage{InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CostMicros: copyBudgetInt(r.CostMicros)}}
	if r.CostMicros == nil {
		b.unknownCosts++
	} else {
		charge.heldCost = *r.CostMicros
	}
	b.charges[r.ID] = charge
	b.inputUsed += r.InputTokens
	b.outputUsed += r.OutputTokens
	b.costHeld += charge.heldCost
	return r, nil
}

// SettleModel replaces a reservation exactly once with final cumulative usage.
// Cancellation/deadline do not prevent accounting. Unknown actual cost remains
// unknown in Usage and retains the estimated hold for enforcement. Duplicated
// final events are harmless; partial usage must be aggregated by the decorator.
func (b *SharedRunBudget) SettleModel(_ context.Context, id uuid.UUID, usage execution.BudgetUsage) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.charges[id]
	if !ok {
		return ErrBudgetReservation
	}
	if old.settled {
		return nil
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CostMicros != nil && *usage.CostMicros < 0 {
		return ErrBudgetReservation
	}
	if old.usage.CostMicros == nil {
		b.unknownCosts--
	}
	heldCost := old.heldCost
	if usage.CostMicros == nil {
		b.unknownCosts++
	} else {
		heldCost = *usage.CostMicros
	}
	b.inputUsed = b.add(b.inputUsed-old.usage.InputTokens, usage.InputTokens)
	b.outputUsed = b.add(b.outputUsed-old.usage.OutputTokens, usage.OutputTokens)
	b.costHeld = b.add(b.costHeld-old.heldCost, heldCost)
	usage.CostMicros = copyBudgetInt(usage.CostMicros)
	b.charges[id] = modelCharge{usage: usage, heldCost: heldCost, settled: true}
	if b.overflow || b.tokens != nil && (b.inputUsed > *b.tokens || b.outputUsed > *b.tokens-b.inputUsed) || b.cost != nil && b.costHeld > *b.cost {
		return ErrBudgetExceeded
	}
	if b.cost != nil && b.unknownCosts > 0 {
		return ErrBudgetUnknownCost
	}
	return nil
}

func (b *SharedRunBudget) add(a, n int64) int64 {
	if n > math.MaxInt64-a {
		b.overflow = true
		return math.MaxInt64
	}
	return a + n
}

func (b *SharedRunBudget) ReserveTool(ctx context.Context, tool string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.live(ctx); err != nil {
		return err
	}
	if tool == "" {
		return ErrBudgetReservation
	}
	if b.toolsUsed == math.MaxInt64 || b.tools != nil && b.toolsUsed >= *b.tools {
		return fmt.Errorf("%w: tools", ErrBudgetExceeded)
	}
	b.toolsUsed++
	return nil
}

// Usage returns a detached snapshot, including conservative outstanding holds.
// CostMicros is nil if any model cost is unknown, never a fabricated zero.
func (b *SharedRunBudget) Usage() store.BotUsage {
	b.mu.Lock()
	defer b.mu.Unlock()
	u := store.BotUsage{InputTokens: b.inputUsed, OutputTokens: b.outputUsed, ToolCalls: b.toolsUsed}
	if !b.startedAt.IsZero() {
		u.RuntimeNanos = b.add(b.runtimeUsed, max(0, int64(time.Since(b.startedAt))))
	}
	if b.unknownCosts == 0 {
		u.CostMicros = copyBudgetInt(&b.costHeld)
	}
	return u
}

var _ execution.RunBudget = (*SharedRunBudget)(nil)
