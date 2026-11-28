package streaming

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestGenerationHealthSixHoursOfFactualActivity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := StartHealthMonitor(context.Background(), HealthOptions{IdleTimeout: 2 * time.Minute, CheckInterval: time.Second})
		defer stop()
		if !HealthEnabled(ctx) || HealthEnabled(context.Background()) {
			t.Fatal("context isolation")
		}
		for i := 0; i < 6*60; i++ {
			time.Sleep(time.Minute)
			ReportUpstreamActivity(ctx)
			if ctx.Err() != nil {
				t.Fatalf("healthy generation at minute %d: %v", i, context.Cause(ctx))
			}
		}
		if err := stop(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
	})
}

func TestGenerationHealthLocalHeartbeatsDoNotResetIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := StartHealthMonitor(context.Background(), HealthOptions{IdleTimeout: 2 * time.Minute})
		for i := 0; i < 3*60; i++ {
			time.Sleep(time.Second)
			Report(ctx, Progress{Kind: KindHeartbeat})
		}
		var stalled *GenerationStalledError
		if !errors.As(stop(), &stalled) || stalled.IdleFor < 2*time.Minute {
			t.Fatal("local activity masked upstream silence")
		}
	})
}

func TestGenerationHealthCheckCancellationAndJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancelCause(context.Background())
		entered, exited := false, false
		ctx, stop := StartHealthMonitor(parent, HealthOptions{Check: func(ctx context.Context) error {
			entered = true
			<-ctx.Done()
			exited = true
			return context.Cause(ctx)
		}})
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if !entered {
			t.Fatal("check not called")
		}
		cause := errors.New("authority revoked")
		cancel(cause)
		if err := stop(); !errors.Is(err, cause) || !exited || !errors.Is(context.Cause(ctx), cause) {
			t.Fatalf("stop did not join/check cause: %v", err)
		}
	})
}

func TestGenerationHealthCheckFailureDespiteActivity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cause := errors.New("budget exhausted")
		ctx, stop := StartHealthMonitor(context.Background(), HealthOptions{IdleTimeout: time.Minute, Check: func(context.Context) error { return cause }})
		ReportUpstreamActivity(ctx)
		time.Sleep(2 * time.Second)
		if err := stop(); !errors.Is(err, cause) {
			t.Fatalf("check cause: %v", err)
		}
	})
}

func TestGenerationHealthIdleCancelsBlockedCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exited := false
		_, stop := StartHealthMonitor(context.Background(), HealthOptions{IdleTimeout: 2 * time.Minute, Check: func(ctx context.Context) error {
			<-ctx.Done()
			exited = true
			return ctx.Err()
		}})
		time.Sleep(3 * time.Minute)
		var stalled *GenerationStalledError
		if err := stop(); !errors.As(err, &stalled) || !exited {
			t.Fatalf("idle watchdog blocked by Check: %v, joined=%v", err, exited)
		}
	})
}

func TestGenerationHealthNormalStopJoinsCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exited := false
		_, stop := StartHealthMonitor(context.Background(), HealthOptions{Check: func(ctx context.Context) error {
			<-ctx.Done()
			exited = true
			return ctx.Err()
		}})
		time.Sleep(2 * time.Second)
		if err := stop(); err != nil || !exited {
			t.Fatalf("normal stop: %v, joined=%v", err, exited)
		}
		if err := stop(); err != nil {
			t.Fatal(err)
		}
	})
}
