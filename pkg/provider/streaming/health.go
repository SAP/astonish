package streaming

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// HealthOptions controls request-local liveness, not total generation age.
// A non-positive IdleTimeout disables the silence limit. Check runs serially
// independently of upstream activity. It must honor ctx cancellation; stop joins
// it, so callbacks must not wait on the provider completing or call stop itself.
type HealthOptions struct {
	IdleTimeout   time.Duration
	CheckInterval time.Duration
	Check         func(context.Context) error
}

// GenerationStalledError means no factual upstream activity was observed during
// the silence window. It does not establish whether the provider charged usage.
type GenerationStalledError struct {
	IdleTimeout time.Duration
	IdleFor     time.Duration
}

func (e *GenerationStalledError) Error() string {
	return fmt.Sprintf("generation stalled: no upstream activity for %s (limit %s)", e.IdleFor, e.IdleTimeout)
}

type healthKey struct{}
type generationHealth struct {
	mu   sync.Mutex
	last time.Time
}

// HealthEnabled reports whether ctx carries a request-local generation monitor.
func HealthEnabled(ctx context.Context) bool {
	h, _ := ctx.Value(healthKey{}).(*generationHealth)
	return h != nil
}

// ReportUpstreamActivity records actual upstream response headers, bytes or
// decoded events, without retaining their payload. Never call it for a local
// timer heartbeat, semantic progress display, or request writes. Report is
// deliberately independent and cannot reset the silence window.
func ReportUpstreamActivity(ctx context.Context) {
	if h, _ := ctx.Value(healthKey{}).(*generationHealth); h != nil {
		h.mu.Lock()
		h.last = time.Now()
		h.mu.Unlock()
	}
}

var errHealthStopped = errors.New("generation health monitor stopped")

// StartHealthMonitor starts one watchdog for one underlying generation. There
// is no generation-age deadline. Inherited cancellation/deadlines remain binding.
// Call stop exactly when the provider iterator exits (including early consumer
// exit). stop is concurrent-safe, cancels and joins the watchdog and Check, and
// returns the original cancellation/stall/check cause, or nil on normal stop.
// The returned context is canceled even on normal stop; callers must use stop's
// result, not ctx.Err(), to classify completion after stopping.
func StartHealthMonitor(parent context.Context, options HealthOptions) (context.Context, func() error) {
	ctx, cancel := context.WithCancelCause(parent)
	h := &generationHealth{last: time.Now()}
	ctx = context.WithValue(ctx, healthKey{}, h)
	interval := options.CheckInterval
	if interval <= 0 {
		interval = time.Second
	}
	if options.IdleTimeout > 0 && interval > options.IdleTimeout {
		interval = options.IdleTimeout
	}
	done := make(chan struct{})
	var checks sync.WaitGroup
	if options.Check != nil {
		checks.Add(1)
		go func() {
			defer checks.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if ctx.Err() != nil {
						return
					}
					if err := options.Check(ctx); err != nil {
						cancel(err)
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(done)
		defer checks.Wait()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				h.mu.Lock()
				idle := time.Since(h.last)
				if options.IdleTimeout > 0 && idle >= options.IdleTimeout {
					cancel(&GenerationStalledError{IdleTimeout: options.IdleTimeout, IdleFor: idle})
				}
				h.mu.Unlock()
			}
		}
	}()
	return ctx, func() error {
		cancel(errHealthStopped)
		<-done
		cause := context.Cause(ctx)
		if errors.Is(cause, errHealthStopped) {
			return nil
		}
		return cause
	}
}
