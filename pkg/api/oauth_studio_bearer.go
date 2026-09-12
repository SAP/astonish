package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// OAuthStudioBearerMiddleware accepts only Astonish-issued OAuth access tokens
// for the extension-facing Studio chat endpoints. Cookie/platform-session traffic
// remains on PlatformAuthMiddleware's existing path.
func OAuthStudioBearerMiddleware(validator BearerPrincipalValidator, resolveTenant MCPTenantResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if validator == nil || !strings.HasPrefix(r.URL.Path, "/api/studio/") || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			next.ServeHTTP(w, r)
			return
		}
		principal, err := validator.ValidateBearer(r.Context(), r.Header.Get("Authorization"), "", []string{string(execution.CapabilityChat)}, execution.SurfaceStudio)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "Studio OAuth authentication required", http.StatusUnauthorized)
			return
		}
		if err := (execution.CapabilityAuthorizer{}).Authorize(principal, execution.CapabilityChat); err != nil {
			http.Error(w, "Studio OAuth principal is not authorized", http.StatusForbidden)
			return
		}
		if resolveTenant != nil {
			principal.OrgSlug, principal.TeamSlug, err = resolveTenant(r.Context(), principal.OrgSlug, principal.TeamSlug)
			if err != nil {
				http.Error(w, "Studio OAuth tenant is unavailable", http.StatusForbidden)
				return
			}
		}
		ctx, err := execution.WithPrincipal(r.Context(), principal)
		if err != nil {
			http.Error(w, "Studio OAuth principal is not authorized", http.StatusForbidden)
			return
		}
		ctx = store.WithTenantContext(ctx, &store.TenantContext{OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug, UserID: principal.Subject})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

var _ context.Context
