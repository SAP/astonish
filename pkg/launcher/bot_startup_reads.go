package launcher

import (
	"context"

	"github.com/SAP/astonish/pkg/store/entstore"
)

type botStartupReadsKey struct{}

// WithBotStartupReads bounds the adapter's synchronous first-request preparation.
// The caller must defer end for failure paths; the model boundary also ends it
// before any generation or tool dispatch. It stores verified immutable bytes
// only, never directory membership, lease state, or authorization decisions.
func WithBotStartupReads(ctx context.Context) (context.Context, func()) {
	ctx, end := entstore.WithBotContentReadOperation(ctx)
	return context.WithValue(ctx, botStartupReadsKey{}, end), end
}

func endBotStartupReads(ctx context.Context) {
	if end, ok := ctx.Value(botStartupReadsKey{}).(func()); ok {
		end()
	}
}
