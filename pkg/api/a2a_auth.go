package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/execution"
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

// --- Package-level state for A2A rate limiter (set by daemon during startup) ---

var (
	a2aRateLimiterMu sync.RWMutex
	a2aRateLimiter   *a2a.AgentRateLimiter
)

// SetA2ARateLimiter sets the A2A per-agent rate limiter for the HTTP handlers.
func SetA2ARateLimiter(rl *a2a.AgentRateLimiter) {
	a2aRateLimiterMu.Lock()
	defer a2aRateLimiterMu.Unlock()
	a2aRateLimiter = rl
}

func getA2ARateLimiter() *a2a.AgentRateLimiter {
	a2aRateLimiterMu.RLock()
	defer a2aRateLimiterMu.RUnlock()
	return a2aRateLimiter
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
			// Determine appropriate error code
			code := a2a.ErrCodeForbidden
			if errors.Is(err, a2a.ErrTokenExpired) {
				code = a2a.ErrCodeAuthRequired
			} else if errors.Is(err, a2a.ErrUntrustedIssuer) || errors.Is(err, a2a.ErrInvalidAudience) || errors.Is(err, a2a.ErrActorNotAllowed) {
				code = a2a.ErrCodeForbidden
			}
			writeJSONRPCError(w, nil, code, err.Error())
			return
		}

		resolver := getA2APrincipalResolver()
		if resolver == nil {
			writeJSONRPCError(w, nil, a2a.ErrCodeInternal, "A2A principal resolution is not configured")
			return
		}
		principal, err := resolver(r.Context(), claims)
		if err != nil {
			writeJSONRPCError(w, nil, a2a.ErrCodeForbidden, "A2A identity is not authorized")
			return
		}
		ctx, err := execution.WithPrincipal(r.Context(), principal)
		if err != nil {
			writeJSONRPCError(w, nil, a2a.ErrCodeForbidden, "A2A identity is not authorized")
			return
		}
		ctx = context.WithValue(ctx, a2aClaimsContextKey{}, claims)
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

// A2APrincipalResolverFunc resolves validated A2A claims into the canonical
// principal after checking the active Astonish user, organization, and team.
// Returning an error rejects the request before it reaches channel execution.
type A2APrincipalResolverFunc func(context.Context, *a2a.A2ATokenClaims) (execution.Principal, error)

var (
	a2aPrincipalResolverMu sync.RWMutex
	a2aPrincipalResolver   A2APrincipalResolverFunc
)

// SetA2APrincipalResolver configures the required fail-closed identity bridge
// from A2A protocol claims to Astonish's canonical execution principal.
func SetA2APrincipalResolver(fn A2APrincipalResolverFunc) {
	a2aPrincipalResolverMu.Lock()
	defer a2aPrincipalResolverMu.Unlock()
	a2aPrincipalResolver = fn
}

func getA2APrincipalResolver() A2APrincipalResolverFunc {
	a2aPrincipalResolverMu.RLock()
	defer a2aPrincipalResolverMu.RUnlock()
	return a2aPrincipalResolver
}

// SetA2AUserResolver is retained for test and migration compatibility. New
// callers must use SetA2APrincipalResolver so authorization returns the full
// canonical principal rather than only a boolean existence decision.
func SetA2AUserResolver(fn func(context.Context, string, string) bool) {
	if fn == nil {
		SetA2APrincipalResolver(nil)
		return
	}
	SetA2APrincipalResolver(func(ctx context.Context, claims *a2a.A2ATokenClaims) (execution.Principal, error) {
		if claims == nil || !fn(ctx, claims.UserIdentifier, claims.OrgID) {
			return execution.Principal{}, errors.New("A2A user is not authorized")
		}
		return execution.Principal{
			Kind:           execution.PrincipalKindUser,
			Authentication: execution.AuthMethodOAuth,
			Surface:        execution.SurfaceA2A,
			Subject:        claims.UserIdentifier,
			Actor:          claims.ActorIdentifier,
			Issuer:         claims.Issuer,
			OrgSlug:        claims.OrgID,
			TeamSlug:       claims.TeamID,
			Scopes:         []string{string(execution.CapabilityChat)},
			Authenticated:  true,
		}, nil
	})
}
