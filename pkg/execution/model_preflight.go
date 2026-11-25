package execution

import "context"

type modelPreflightEndKey struct{}

// WithModelPreflightEnd transfers synchronous cleanup to a model decorator that
// promises to close it before actual provider entry (including iterator creation).
func WithModelPreflightEnd(ctx context.Context, end func()) context.Context {
	return context.WithValue(ctx, modelPreflightEndKey{}, end)
}

func EndModelPreflight(ctx context.Context) {
	if end, ok := ctx.Value(modelPreflightEndKey{}).(func()); ok {
		end()
	}
}
