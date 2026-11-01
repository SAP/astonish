// Package streaming provides payload-free provider progress notifications.
package streaming

import (
	"context"
	"time"
)

// Kind distinguishes model phases from transport liveness and recovery.
// The zero value is a legacy phase notification, equivalent to KindPhase.
type Kind string

const (
	KindPhase     Kind = "phase"
	KindHeartbeat Kind = "heartbeat"
	KindRecovery  Kind = "recovery"
)

// Phase describes work that may not yet produce visible model output.
type Phase string

const (
	PhaseThinking      Phase = "thinking"
	PhaseToolArguments Phase = "tool_arguments"
	PhaseWaiting       Phase = "waiting"
	PhaseRecovering    Phase = "recovering"
)

// Progress deliberately carries no reasoning, tool names, or arguments.
// Attempt is one-based; Elapsed is generation age and LastActivity is time since
// the most recent upstream bytes (or attempt start before any bytes arrive).
type Progress struct {
	Kind         Kind
	Phase        Phase
	Attempt      int
	MaxAttempts  int
	Elapsed      time.Duration
	LastActivity time.Duration
}

type progressKey struct{}

// WithProgress binds a synchronous callback to ctx. A nil callback disables it.
// Callers of Report are responsible for bounding emission; callbacks should not block.
func WithProgress(ctx context.Context, callback func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, callback)
}

// Report delivers progress synchronously to the callback bound to ctx, if any.
func Report(ctx context.Context, progress Progress) {
	if callback, ok := ctx.Value(progressKey{}).(func(Progress)); ok && callback != nil {
		callback(progress)
	}
}
