package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/a2a"
)

// a2aAgentContextKey is the context key for the authenticated A2A agent.
type a2aAgentContextKey struct{}

// A2AAuthMiddleware authenticates incoming A2A requests by looking up the
// agent in the registry. Supports Bearer token and X-API-Key header auth.
func A2AAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch := getA2AChannel()
		if ch == nil {
			writeJSONRPCError(w, nil, a2a.ErrCodeInternal, "A2A channel not configured")
			return
		}

		apiKey := extractA2AKey(r)
		if apiKey == "" {
			writeJSONRPCError(w, nil, a2a.ErrCodeAuthRequired, "Authentication required: provide Authorization: Bearer <key> or X-API-Key header")
			return
		}

		agent, err := ch.AgentRegistry().GetByAPIKey(apiKey)
		if err != nil {
			writeJSONRPCError(w, nil, a2a.ErrCodeForbidden, "Invalid credentials")
			return
		}

		// Inject agent into context
		ctx := context.WithValue(r.Context(), a2aAgentContextKey{}, agent)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AgentFromContext extracts the authenticated RegisteredAgent from the request context.
func AgentFromContext(ctx context.Context) *a2a.RegisteredAgent {
	agent, _ := ctx.Value(a2aAgentContextKey{}).(*a2a.RegisteredAgent)
	return agent
}

// extractA2AKey extracts the API key from the request.
// Supports: Authorization: Bearer <key> and X-API-Key: <key>
func extractA2AKey(r *http.Request) string {
	// Check Authorization header first
	auth := r.Header.Get("Authorization")
	if auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
	}

	// Check X-API-Key header
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}

	return ""
}
