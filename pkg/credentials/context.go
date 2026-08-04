package credentials

import (
	"context"

	"github.com/SAP/astonish/pkg/store"
)

// redactorContextKey is the context key for the Redactor instance.
type redactorContextKey struct{}

// WithRedactor returns a new context containing the given Redactor.
// Used to propagate the per-session Redactor into the ADK runner context
// so that tool functions (e.g., memory_save) can call Placeholderize()
// without needing a direct reference to the agent's Redactor.
func WithRedactor(ctx context.Context, r *Redactor) context.Context {
	return context.WithValue(ctx, redactorContextKey{}, r)
}

// RedactorFromContext retrieves the Redactor from a context.
// Returns nil if no Redactor is present (personal mode without platform
// injection, or tests).
func RedactorFromContext(ctx context.Context) *Redactor {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(redactorContextKey{}).(*Redactor)
	return r
}

// ResolverFromContext returns a CredentialResolver backed by the
// request-scoped credential store when present (platform personal+team
// merged store). Returns nil when no store is on the context.
func ResolverFromContext(ctx context.Context) CredentialResolver {
	if ctx == nil {
		return nil
	}
	if cs := store.CredentialStoreFromContext(ctx); cs != nil {
		return NewStoreAdapter(cs)
	}
	return nil
}
