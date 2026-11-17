package provider

import (
	"context"

	"github.com/SAP/astonish/pkg/execution"
)

// CheckGenerationBudget checks liveness of an admitted generation without asking
// for capacity to admit another operation. Budgets without the optional contract
// retain their existing fail-closed Check behavior. This never grants admission.
func CheckGenerationBudget(ctx context.Context, budget execution.RunBudget) error {
	if inFlight, ok := budget.(interface{ CheckInFlight(context.Context) error }); ok {
		return inFlight.CheckInFlight(ctx)
	}
	return budget.Check(ctx)
}

type generationHealthCheckKey struct{}

// WithGenerationHealthCheck installs a trusted request-local authority recheck.
// It must honor cancellation, must not wait for generation to finish, and must
// not require the dispatch boundary to be idle. The monitor joins it on exit.
// This supplements, never replaces, the supervisor and shared budget checks.
func WithGenerationHealthCheck(ctx context.Context, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, generationHealthCheckKey{}, check)
}
