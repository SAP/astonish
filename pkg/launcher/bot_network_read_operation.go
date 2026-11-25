package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/tool"
)

// The owner callback is private to one bound invocation; never store it on a tool.
type botNativeNetworkPreflightKey struct{}

func endBotNativeNetworkPreflight(ctx context.Context) {
	if end, ok := ctx.Value(botNativeNetworkPreflightKey{}).(func()); ok {
		end()
	}
}

func botNetworkReadOperation(c tool.Context) (tool.Context, func()) {
	var reads context.Context
	var end func()
	if _, owned := c.Value(botNativeNetworkPreflightKey{}).(func()); owned {
		reads, end = entstore.WithBotContentReadOperationIfAbsent(c)
	} else {
		// Unrelated ambient operations cannot lend bytes across this dispatch.
		reads, end = entstore.WithBotContentReadOperation(c)
	}
	wrapped := tool.Context(botContentReadToolContext{Context: c, reads: reads})
	if bound, ok := c.(botBoundToolContext); ok {
		bound.owner = reads
		bound.Context = wrapped
		wrapped = bound
	}
	return wrapped, end
}
