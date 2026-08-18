package api

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/a2a"
)

// --- Package-level state for A2A token validator (set by daemon during startup) ---

var (
	a2aValidatorMu sync.RWMutex
	a2aValidator   *a2a.TokenValidator
)

// SetA2ATokenValidator sets the A2A token validator for the HTTP handlers.
func SetA2ATokenValidator(v *a2a.TokenValidator) {
	a2aValidatorMu.Lock()
	defer a2aValidatorMu.Unlock()
	a2aValidator = v
}

func getA2ATokenValidator() *a2a.TokenValidator {
	a2aValidatorMu.RLock()
	defer a2aValidatorMu.RUnlock()
	return a2aValidator
}

// a2aClaimsContextKey is the context key for the validated A2A token claims.
type a2aClaimsContextKey struct{}

// A2AAuthMiddleware authenticates incoming A2A requests by validating a JWT
// Bearer token against configured trusted issuers. The token's signature is
// verified via JWKS, and the extracted identity (user + optional actor) is
// injected into the request context.
func A2AAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch := getA2AChannel()
		if ch == nil {
			writeJSONRPCError(w, nil, a2a.ErrCodeInternal, "A2A channel not configured")
			return
		}

		validator := getA2ATokenValidator()
		if validator == nil {
			writeJSONRPCError(w, nil, a2a.ErrCodeInternal, "A2A authentication not configured")
			return
		}

		tokenStr := extractBearerToken(r)
		if tokenStr == "" {
			writeJSONRPCError(w, nil, a2a.ErrCodeAuthRequired, "Authentication required: provide Authorization: Bearer <jwt>")
			return
		}

		claims, err := validator.Validate(tokenStr)
		if err != nil {
			// Determine appropriate error code from the error message
			code := a2a.ErrCodeForbidden
			if strings.Contains(err.Error(), "expired") {
				code = a2a.ErrCodeAuthRequired
			} else if strings.Contains(err.Error(), "untrusted issuer") ||
				strings.Contains(err.Error(), "no trusted issuer") {
				code = a2a.ErrCodeForbidden
			}
			writeJSONRPCError(w, nil, code, err.Error())
			return
		}

		// Inject validated claims into context
		ctx := context.WithValue(r.Context(), a2aClaimsContextKey{}, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// A2AClaimsFromContext extracts the validated A2A token claims from the request context.
func A2AClaimsFromContext(ctx context.Context) *a2a.A2ATokenClaims {
	claims, _ := ctx.Value(a2aClaimsContextKey{}).(*a2a.A2ATokenClaims)
	return claims
}

// extractBearerToken extracts the JWT from the Authorization: Bearer header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth != "" && strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}
