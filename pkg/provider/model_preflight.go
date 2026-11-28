package provider

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
)

// WithModelPreflightEnd transfers synchronous preparation cleanup to the
// innermost budget wrapper. No scope may survive provider entry or consumer yield.
func WithModelPreflightEnd(ctx context.Context, end func()) context.Context {
	return execution.WithModelPreflightEnd(ctx, end)
}
func EndModelPreflight(ctx context.Context) { execution.EndModelPreflight(ctx) }

// SupportsModelPreflight promises cleanup before provider iterator creation.
func (b *BudgetLLM) SupportsModelPreflight() bool { return true }
