package store

import (
	"context"
	"sync"
	"time"
)

// BotActivityTiming is request-local diagnostic data, not authority or a receipt.
// Durations can be nested; callers must not sum parents with their children.
type BotActivityTiming struct {
	Stage    string
	Sequence int64
	Duration time.Duration
}

type botActivityProfilerKey struct{}

func WithBotActivityProfiler(ctx context.Context, observe func(BotActivityTiming)) context.Context {
	return context.WithValue(ctx, botActivityProfilerKey{}, observe)
}

func ObserveBotActivity(ctx context.Context, stage string, sequence int64) func() {
	if ctx == nil {
		return func() {}
	}
	observe, _ := ctx.Value(botActivityProfilerKey{}).(func(BotActivityTiming))
	if observe == nil {
		return func() {}
	}
	start := time.Now()
	return func() { observe(BotActivityTiming{Stage: stage, Sequence: sequence, Duration: time.Since(start)}) }
}

// BotRuntimeIOOperation distinguishes internal runtime file I/O from workspace
// documents and configuration. Callers must instrument the actual file boundary,
// not database reads, cache lookups, or arbitrary activity timing stages.
type BotRuntimeIOOperation uint8

const (
	BotRuntimeIOOpen BotRuntimeIOOperation = iota + 1
	BotRuntimeIOPut
)

func (op BotRuntimeIOOperation) String() string {
	switch op {
	case BotRuntimeIOOpen:
		return "runtime_content_open"
	case BotRuntimeIOPut:
		return "runtime_content_put"
	default:
		return "unknown"
	}
}

// BotRuntimeIOTiming contains no content references, payloads, or error messages.
// Failed attempts count too; Duration covers the call, not stream consumption.
type BotRuntimeIOTiming struct {
	Operation BotRuntimeIOOperation
	Sequence  int64
	Duration  time.Duration
	Failed    bool
}

type BotRuntimeIOSnapshot struct {
	Opens    int64
	Puts     int64
	Failures int64
	// Covered is explicitly asserted by an instrumented repository entry point.
	// Without that assertion, zero observed calls is NOT proof of zero file I/O.
	Covered bool
}

type BotRuntimeIOProfile struct {
	mu       sync.Mutex
	snapshot BotRuntimeIOSnapshot
	observe  func(BotRuntimeIOTiming)
}

type botRuntimeIOProfilerKey struct{}

func WithBotRuntimeIOProfiler(ctx context.Context, observe func(BotRuntimeIOTiming)) (context.Context, *BotRuntimeIOProfile) {
	p := &BotRuntimeIOProfile{observe: observe}
	return context.WithValue(ctx, botRuntimeIOProfilerKey{}, p), p
}

func (p *BotRuntimeIOProfile) Snapshot() BotRuntimeIOSnapshot {
	if p == nil {
		return BotRuntimeIOSnapshot{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshot
}

// MarkBotRuntimeIOProfileCovered must only be called by a repository path whose
// runtime open AND put boundaries are instrumented, including legacy records.
// Mark at entry so a genuinely empty result can still prove zero I/O.
func MarkBotRuntimeIOProfileCovered(ctx context.Context) {
	if ctx == nil {
		return
	}
	if p, _ := ctx.Value(botRuntimeIOProfilerKey{}).(*BotRuntimeIOProfile); p != nil {
		p.mu.Lock()
		p.snapshot.Covered = true
		p.mu.Unlock()
	}
}

// ObserveBotRuntimeIO counts an attempted operation immediately and returns an
// idempotent completion callback. Pass the call's error without logging its text.
// Disabled profiling does not read the clock or allocate a profile.
func ObserveBotRuntimeIO(ctx context.Context, op BotRuntimeIOOperation, sequence int64) func(error) {
	if ctx == nil {
		return func(error) {}
	}
	p, _ := ctx.Value(botRuntimeIOProfilerKey{}).(*BotRuntimeIOProfile)
	if p == nil || (op != BotRuntimeIOOpen && op != BotRuntimeIOPut) {
		return func(error) {}
	}
	p.mu.Lock()
	if op == BotRuntimeIOOpen {
		p.snapshot.Opens++
	} else {
		p.snapshot.Puts++
	}
	p.mu.Unlock()
	start := time.Now()
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			if err != nil {
				p.mu.Lock()
				p.snapshot.Failures++
				p.mu.Unlock()
			}
			if p.observe != nil {
				p.observe(BotRuntimeIOTiming{Operation: op, Sequence: sequence, Duration: time.Since(start), Failed: err != nil})
			}
		})
	}
}
