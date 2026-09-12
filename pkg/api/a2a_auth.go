package api

import (
	"context"
	"net/http"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

const a2aScope = string(execution.CapabilityA2A)

// A2ATenantResolver translates OAuth tenant IDs to the slugs required by scoped stores.
type A2ATenantResolver func(context.Context, string, string) (orgSlug, teamSlug string, err error)

// a2aBearerMiddleware accepts only Astonish-issued OAuth tokens carrying the
// exact A2A scope, then installs the canonical principal and tenant context.
func a2aBearerMiddleware(validator BearerPrincipalValidator, resolveTenant A2ATenantResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := validator.ValidateBearer(r.Context(), r.Header.Get("Authorization"), "", []string{a2aScope}, execution.SurfaceA2A)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeJSONRPCError(w, nil, -32002, "A2A authentication required")
			return
		}
		if err := (execution.CapabilityAuthorizer{}).Authorize(principal, execution.CapabilityA2A); err != nil {
			writeJSONRPCError(w, nil, -32004, "A2A principal is not authorized")
			return
		}
		ctx, err := execution.WithPrincipal(r.Context(), principal)
		if err != nil {
			writeJSONRPCError(w, nil, -32004, "A2A principal is not authorized")
			return
		}
		orgSlug, teamSlug := principal.OrgSlug, principal.TeamSlug
		if resolveTenant != nil {
			orgSlug, teamSlug, err = resolveTenant(ctx, orgSlug, teamSlug)
			if err != nil {
				writeJSONRPCError(w, nil, -32004, "A2A tenant is unavailable")
				return
			}
		}
		userID := principal.Subject
		if userID == "" {
			userID = principal.ClientID
		}
		ctx = store.WithTenantContext(ctx, &store.TenantContext{OrgSlug: orgSlug, TeamSlug: teamSlug, UserID: userID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
