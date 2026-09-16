package channels

import "context"

// ProgressKind identifies a safe, user-visible progress event.
type ProgressKind string

const (
	ProgressToolStarted   ProgressKind = "tool_started"
	ProgressToolCompleted ProgressKind = "tool_completed"
)

// ProgressEvent carries lifecycle metadata without tool arguments or results.
type ProgressEvent struct {
	Kind     ProgressKind
	ToolName string
}

// ProgressSink receives redacted, user-facing progress text from an agent run.
type ProgressSink interface {
	Progress(text string)
}

// StructuredProgressSink additionally receives safe tool lifecycle events.
type StructuredProgressSink interface {
	ProgressSink
	ProgressEvent(event ProgressEvent)
}

type progressSinkKey struct{}

// WithProgressSink attaches a progress sink to the execution context.
func WithProgressSink(ctx context.Context, sink ProgressSink) context.Context {
	return context.WithValue(ctx, progressSinkKey{}, sink)
}

// ProgressSinkFromContext returns the progress sink attached to ctx, if any.
func ProgressSinkFromContext(ctx context.Context) ProgressSink {
	sink, _ := ctx.Value(progressSinkKey{}).(ProgressSink)
	return sink
}
