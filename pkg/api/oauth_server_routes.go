package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/SAP/astonish/pkg/oauthserver"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// NewOAuthSessionValidator adapts an existing Astonish platform session to the
// OAuth authorization endpoint. It deliberately validates the local session
// rather than accepting an upstream IdP token: federation has already happened
// at the platform login boundary.
func NewOAuthSessionValidator(pa *PlatformAuth, backend store.PlatformBackend) oauthserver.SessionValidator {
	return func(ctx context.Context, r *http.Request) (oauthserver.Subject, error) {
		if pa == nil || backend == nil {
			return oauthserver.Subject{}, fmt.Errorf("platform authentication is unavailable")
		}
		token := extractAccessToken(r)
		if token == "" {
			return oauthserver.Subject{}, fmt.Errorf("missing platform session")
		}
		claims, err := pa.JWTIssuer().ValidateAccessToken(token)
		if err != nil {
			return oauthserver.Subject{}, fmt.Errorf("invalid platform session: %w", err)
		}
		user, err := backend.Users().GetByID(ctx, claims.UserID)
		if err != nil || user == nil || user.Status != "active" {
			return oauthserver.Subject{}, fmt.Errorf("platform user is inactive")
		}
		org, err := backend.Organizations().GetBySlug(ctx, claims.OrgSlug)
		if err != nil || org == nil || org.Status != "active" {
			return oauthserver.Subject{}, fmt.Errorf("organization is inactive")
		}
		return oauthserver.Subject{ID: user.ID, OrgID: org.ID}, nil
	}
}

// RegisterOAuthServerRoutes mounts public OAuth/OIDC discovery and protocol
// endpoints. /authorize independently validates the caller's existing
// platform session through NewOAuthSessionValidator.
func RegisterOAuthServerRoutes(router *mux.Router, server *oauthserver.Server) {
	if server == nil {
		return
	}
	oauthserver.RegisterRoutes(router, server)
}
