package entstore

import (
	"context"
	"sync"
	"time"
)

// BotExecutionStage is a payload-free diagnostic, never an execution receipt.
type BotExecutionStage uint8

func (s BotExecutionStage) String() string {
	switch s {
	case BotStagePreflight:
		return "preflight"
	case BotStageProvider:
		return "provider"
	case BotStageTool:
		return "tool"
	case BotStageBookkeeping:
		return "bookkeeping"
	case BotStageQueue:
		return "queue"
	case BotStageClaim:
		return "claim"
	case BotStageStartup:
		return "startup"
	case BotStageApproval:
		return "approval"
	case BotStageCommit:
		return "commit"
	case BotStageOwnerSetup:
		return "owner-setup"
	case BotStageCompletion:
		return "completion"
	case BotStageHomeSession:
		return "home-session"
	case BotStageFactory:
		return "factory"
	case BotStageContentRead:
		return "content-read"
	case BotStageContentWrite:
		return "content-write"
	default:
		return "unknown"
	}
}

const (
	BotStagePreflight BotExecutionStage = iota + 1
	BotStageProvider
	BotStageTool
	BotStageBookkeeping
	BotStageQueue
	BotStageClaim
	BotStageStartup
	BotStageApproval
	BotStageCommit
	BotStageOwnerSetup
	BotStageCompletion
	BotStageHomeSession
	BotStageFactory
	BotStageContentRead
	BotStageContentWrite
)

type BotExecutionResult uint8

const (
	BotStageSucceeded BotExecutionResult = iota + 1
	BotStageFailed
)

type BotExecutionTiming struct {
	Stage    BotExecutionStage
	Result   BotExecutionResult
	Duration time.Duration
	// Nested spans are independent: account for their union, not their sum.
	StartedAt time.Time
	EndedAt   time.Time
}
type botExecutionObserverKey struct{}

func WithBotExecutionObserver(ctx context.Context, observe func(BotExecutionTiming)) context.Context {
	return context.WithValue(ctx, botExecutionObserverKey{}, observe)
}
func CopyBotExecutionObserver(dst, src context.Context) context.Context {
	if dst == nil || src == nil {
		return dst
	}
	if observe, ok := src.Value(botExecutionObserverKey{}).(func(BotExecutionTiming)); ok {
		return WithBotExecutionObserver(dst, observe)
	}
	return dst
}
func ObserveBotExecutionStage(ctx context.Context, stage BotExecutionStage) func(error) {
	return ObserveBotExecutionStageSince(ctx, stage, time.Now())
}

// ObserveBotExecutionStageSince also measures boundaries originating outside the
// current call, such as durable owner admission. Persisted times have no monotonic
// component; consumers must not subtract these aggregate spans from nested stages.
func ObserveBotExecutionStageSince(ctx context.Context, stage BotExecutionStage, started time.Time) func(error) {
	if ctx == nil {
		return func(error) {}
	}
	observe, _ := ctx.Value(botExecutionObserverKey{}).(func(BotExecutionTiming))
	if observe == nil {
		return func(error) {}
	}
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			ended := time.Now()
			duration := ended.Sub(started)
			if started.IsZero() || duration < 0 {
				started, duration = ended, 0
			}
			result := BotStageSucceeded
			if err != nil {
				result = BotStageFailed
			}
			observe(BotExecutionTiming{Stage: stage, Result: result, Duration: duration, StartedAt: started, EndedAt: ended})
		})
	}
}

type botContentReadOperationKey struct{}

// WithBotContentReadOperation deduplicates verified immutable content reads during
// one bounded synchronous operation. It never caches SQL, authorization, leases,
// binding lookups, errors, or mutable Home files. End MUST run before returning
// from the operation; retained contexts cannot reuse its bytes afterward.
func WithBotContentReadOperation(ctx context.Context) (context.Context, func()) {
	cache := &botTransactionContent{objects: make(map[botTransactionContentKey][]byte)}
	return context.WithValue(ctx, botContentReadOperationKey{}, cache), func() {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cache.closed = true
		cache.objects = nil
		cache.bytes = 0
	}
}

// WithBotContentReadPreflight borrows only an explicit active between-call gap,
// never an unrelated operation retained by the caller. Without a gap it owns fresh bytes.
func WithBotContentReadPreflight(ctx context.Context) (context.Context, func()) {
	if window := activeImmutableReadWindow(ctx); window != nil {
		return context.WithValue(ctx, botContentReadOperationKey{}, window), func() {}
	}
	return WithBotContentReadOperation(ctx)
}

// WithBotContentReadOperationIfAbsent is for synchronous bookkeeping helpers
// nested inside another operation. A borrower never closes the owner's lifetime.
// Execution boundaries must use WithBotContentReadOperation and own their close.
func WithBotContentReadOperationIfAbsent(ctx context.Context) (context.Context, func()) {
	if existing, ok := ctx.Value(botContentReadOperationKey{}).(*botTransactionContent); ok {
		existing.mu.Lock()
		active := !existing.closed
		existing.mu.Unlock()
		if active {
			return ctx, func() {}
		}
	}
	if window := activeImmutableReadWindow(ctx); window != nil {
		return context.WithValue(ctx, botContentReadOperationKey{}, window), func() {}
	}
	return WithBotContentReadOperation(ctx)
}

// WithBotContentReadOperationFromContext carries only immutable-read operation
// state through trusted owner rebinding, never identity or authorization values.
func WithBotContentReadOperationFromContext(owner, source context.Context) context.Context {
	if source == nil {
		return owner
	}
	cache, ok := source.Value(botContentReadOperationKey{}).(*botTransactionContent)
	if !ok {
		return owner
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.closed {
		return owner
	}
	return context.WithValue(owner, botContentReadOperationKey{}, cache)
}

// botImmutableReadWindow is one mutable slot installed on a run context before it
// splits into the value-free owner context and the agent context. It stays empty
// unless synchronous bookkeeping between a tool return and the next provider call
// explicitly begins it. A new SQL transaction still re-reads when the window is
// absent or closed, so corruption is not hidden across operations.
type botImmutableReadWindow struct {
	mu    sync.Mutex
	cache *botTransactionContent
}

type botImmutableReadWindowKey struct{}

// WithImmutableReadWindow installs an empty slot. Callers must copy the same
// pointer across value-stripping boundaries; beginning the window later is visible
// to every holder of that pointer.
func WithImmutableReadWindow(ctx context.Context) context.Context {
	if ctx == nil || immutableReadWindow(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, botImmutableReadWindowKey{}, &botImmutableReadWindow{})
}

// CopyImmutableReadWindow carries only the window pointer, never identity.
func CopyImmutableReadWindow(owner, source context.Context) context.Context {
	if owner != nil && source != nil {
		owner = CopyBotExecutionObserver(owner, source)
	}
	window := immutableReadWindow(source)
	if owner == nil || window == nil || immutableReadWindow(owner) == window {
		return owner
	}
	return context.WithValue(owner, botImmutableReadWindowKey{}, window)
}

func immutableReadWindow(ctx context.Context) *botImmutableReadWindow {
	if ctx == nil {
		return nil
	}
	window, _ := ctx.Value(botImmutableReadWindowKey{}).(*botImmutableReadWindow)
	return window
}

// BeginImmutableReadWindow starts verified-byte reuse for the current gap.
// A second begin keeps the existing bytes. It does nothing without a slot.
func BeginImmutableReadWindow(ctx context.Context) {
	window := immutableReadWindow(ctx)
	if window == nil {
		return
	}
	window.mu.Lock()
	defer window.mu.Unlock()
	if window.cache != nil {
		window.cache.mu.Lock()
		closed := window.cache.closed
		window.cache.mu.Unlock()
		if !closed {
			return
		}
	}
	window.cache = &botTransactionContent{objects: make(map[botTransactionContentKey][]byte)}
}

// EndImmutableReadWindow drops verified bytes before a provider call or run end.
// It is idempotent and does not remove the slot.
func EndImmutableReadWindow(ctx context.Context) {
	window := immutableReadWindow(ctx)
	if window == nil {
		return
	}
	window.mu.Lock()
	cache := window.cache
	window.cache = nil
	window.mu.Unlock()
	if cache == nil {
		return
	}
	cache.mu.Lock()
	cache.closed = true
	cache.objects = nil
	cache.bytes = 0
	cache.mu.Unlock()
}

func activeImmutableReadWindow(ctx context.Context) *botTransactionContent {
	window := immutableReadWindow(ctx)
	if window == nil {
		return nil
	}
	window.mu.Lock()
	defer window.mu.Unlock()
	if window.cache == nil {
		return nil
	}
	window.cache.mu.Lock()
	closed := window.cache.closed
	window.cache.mu.Unlock()
	if closed {
		return nil
	}
	return window.cache
}
