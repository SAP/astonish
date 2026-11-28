package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/tool"
)

// Keep the dispatch authorization marker only when the caller already owns it.
// This wrapper supplies immutable bytes, never a new Bot execution binding.
type botContentReadToolContext struct {
	tool.Context
	reads context.Context
}

func (c botContentReadToolContext) Value(key any) any { return c.reads.Value(key) }
func botToolReadOperation(c tool.Context) (tool.Context, func()) {
	reads, end := entstore.WithBotContentReadOperation(c)
	wrapped := tool.Context(botContentReadToolContext{Context: c, reads: reads})
	if bound, ok := c.(botBoundToolContext); ok {
		bound.owner = reads
		bound.Context = wrapped
		wrapped = bound
	}
	return wrapped, end
}
