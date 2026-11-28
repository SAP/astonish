package bot

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"testing"
	"time"
)

type noDeadlineBudget struct{ execution.RunBudget }

func (noDeadlineBudget) Deadline() time.Time { return time.Time{} }

func TestRunContextExplicitZeroDeadlineIsAuthoritative(t *testing.T) {
	for _, spec := range []store.BudgetSpec{{MaxRuntimeSeconds: 1}, {}} {
		parent, stop := context.WithCancel(context.Background())
		ctx, cancel, err := RunContext(parent, spec, noDeadlineBudget{}, store.BotUsage{RuntimeNanos: int64(24 * time.Hour)}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("explicit zero deadline replaced by historic runtime cap")
		}
		expiry := time.Now().Add(time.Minute)
		authorized, release, err := RunContext(ctx, spec, noDeadlineBudget{}, store.BotUsage{}, expiry)
		if err != nil {
			t.Fatal(err)
		}
		if d, ok := authorized.Deadline(); !ok || !d.Equal(expiry) {
			t.Fatal("authority expiry lost")
		}
		stop()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("parent cancellation lost")
		}
		release()
		cancel()
	}
}

func TestBotProgressBudgetCumulativeDurationAndAuthority(t *testing.T) {
	now := time.Now()
	cap := int64(5)
	spec := store.BudgetSpec{MaxRuntimeSeconds: 300, RuntimePolicy: execution.NewBotProgressRuntimePolicy(), MaxToolCalls: &cap}
	usage := store.BotUsage{RuntimeNanos: int64(6 * time.Hour), ToolCalls: 2}
	b, err := RestoreRunBudget(spec, now, usage)
	if err != nil || !b.Deadline().IsZero() || b.Check(context.Background()) != nil {
		t.Fatalf("six hour progress budget: %v", err)
	}
	c, cancel, err := RunContext(context.Background(), spec, b, usage, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if _, ok := c.Deadline(); ok {
		t.Fatal("implicit whole-task deadline")
	}
	if b.Usage().RuntimeNanos < usage.RuntimeNanos || b.Usage().ToolCalls != 2 {
		t.Fatal("ledger reset")
	}
	expiry := now.Add(time.Minute)
	bounded, release, err := RunContext(c, spec, b, usage, expiry)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if d, ok := bounded.Deadline(); !ok || !d.Equal(expiry) {
		t.Fatal("authority expiry lost")
	}
	cancel()
	if !errors.Is(b.Check(c), context.Canceled) {
		t.Fatal("cancellation disabled")
	}
	active := int64(21610)
	spec.RuntimePolicy.MaxActiveSeconds = &active
	b, err = RestoreRunBudget(spec, now, usage)
	if err != nil || !b.Deadline().Equal(now.Add(10*time.Second)) {
		t.Fatal("active duration was reset or counted waiting")
	}
	for _, seconds := range []int64{300, 900} {
		spec.RuntimePolicy = nil
		spec.MaxRuntimeSeconds = seconds
		old, err := RestoreRunBudget(spec, now, usage)
		if err != nil || !errors.Is(old.Check(context.Background()), ErrBudgetExceeded) {
			t.Fatal("old duration widened")
		}
	}
}
