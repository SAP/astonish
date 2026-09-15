package channels

import "context"

// ProgressSink receives redacted, user-facing progress text from an agent run.
type ProgressSink interface {
	Progress(text string)
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
