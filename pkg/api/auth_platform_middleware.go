package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/store"
)

// PlatformAuthMiddleware returns an HTTP middleware that validates JWT tokens
// for platform mode. It replaces the device-auth middleware when the storage
// backend is "postgres".
//
// The middleware:
// 1. Allows SPA static assets to pass (the React app serves its own login page).
// 2. Allows loopback addresses to pass (CLI/local tools).
// 3. Allows auth endpoints to pass (/api/auth/*).
// 4. Allows platform setup endpoints to pass (/api/platform/mode, /api/platform/init/*).
// 5. Allows migration endpoints to pass (/api/migration/*) — needed before first user exists.
// 6. Validates the access token cookie.
// 7. On valid token: populates TenantContext for downstream TenantMiddleware.
// 8. On expired token: returns 401 so frontend can attempt refresh.
// 9. On missing/invalid token: returns 401 for API requests.
func PlatformAuthMiddleware(pa *PlatformAuth, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow unauthenticated access to exempt paths (SPA assets, auth endpoints, etc.)
		if isAuthExemptPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Loopback address handling (CLI, local tools, daemon internals).
		if isLoopbackRequest(r) {
			if pa.handleLoopbackAuth(w, r, next) {
				return // Handled (either served or wrote error)
			}
			// Fall through to normal auth for "never" mode
		}

		// Extract access token from Authorization header (CLI) or cookie (browser).
		tokenStr := extractAccessToken(r)
		if tokenStr == "" {
			handleUnauthenticated(w, r)
			return
		}

		// Validate the JWT
		claims, err := pa.jwt.ValidateAccessToken(tokenStr)
		if err != nil {
			handleUnauthenticated(w, r)
			return
		}

		// CSRF protection for state-changing requests from cookie-authenticated clients.
		if err := validateCSRF(r); err != nil {
			respondError(w, http.StatusForbidden, err.Error())
			return
		}

		// Resolve team and validate membership.
		teamSlug := resolveTeamSlug(r, claims)
		if err := pa.checkTeamAccess(r.Context(), claims, teamSlug); err != nil {
			respondError(w, http.StatusForbidden, err.Error())
			return
		}

		// Populate authenticated context and serve.
		ctx := buildAuthenticatedContext(r.Context(), claims, teamSlug)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ---------------------------------------------------------------------------
// Sub-functions extracted from PlatformAuthMiddleware
// ---------------------------------------------------------------------------

// isAuthExemptPath returns true for paths that do not require authentication.
// This includes SPA static assets, auth endpoints, platform setup, and migrations.
func isAuthExemptPath(path string) bool {
	// SPA static assets — the React app contains the login page and needs to load first.
	if !strings.HasPrefix(path, "/api/") {
		return true
	}
	// Health check endpoints — needed for Kubernetes probes (no auth).
	if path == "/api/healthz" || path == "/api/readyz" {
		return true
	}
	// Brand theme for login / first paint (no secrets).
	if path == "/api/brand-theme" {
		return true
	}
	// Auth endpoints (register, login, refresh, setup-status, etc.)
	if strings.HasPrefix(path, "/api/auth/") {
		return true
	}
	// Slack webhooks are authenticated by Slack request signatures in the channel handler.
	if path == "/api/slack/events" || path == "/api/slack/commands" {
		return true
	}
	// A2A Agent Card discovery — public endpoint for protocol compliance.
	// A2A JSON-RPC endpoints use their own auth middleware (API key / Bearer).
	if path == "/.well-known/agent-card.json" ||
		path == "/api/a2a" || path == "/api/a2a/stream" {
		return true
	}
	// Platform setup endpoints — needed before any user has registered.
	// NOTE: Does NOT include /api/platform/admin/* which requires superadmin auth.
	if path == "/api/platform/mode" ||
		path == "/api/platform/init" ||
		path == "/api/platform/init/sqlite" ||
		path == "/api/platform/init/status" {
		return true
	}
	// Migration endpoints — migration runs before first user registration.
	if strings.HasPrefix(path, "/api/migration/") {
		return true
	}
	return false
}

// extractAccessToken extracts the JWT access token from the request.
// Checks Authorization header first (CLI clients), then falls back to cookie (browser).
func extractAccessToken(r *http.Request) string {
	if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	if cookie, err := r.Cookie(accessCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	return ""
}

// validateCSRF checks CSRF protection on state-changing requests.
// Bearer token requests (CLI) are exempt because cross-origin scripts cannot read/set Bearer headers.
// SameSite=Strict already blocks most CSRF vectors; this is defense-in-depth.
func validateCSRF(r *http.Request) error {
	if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
		return nil
	}
	// Bearer-auth requests are exempt
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return nil
	}
	// Cookie-authenticated request — require CSRF indicator header
	if r.Header.Get("X-Requested-With") == "" && r.Header.Get("Content-Type") != "application/json" {
		return fmt.Errorf("missing CSRF protection header")
	}
	return nil
}

// resolveTeamSlug determines the team slug for the request from claims and overrides.
// Priority: X-Astonish-Team header > ?team query param > JWT default.
func resolveTeamSlug(r *http.Request, claims *PlatformClaims) string {
	teamSlug := claims.DefaultTeamSlug
	// Allow per-request team override via header
	if headerTeam := r.Header.Get("X-Astonish-Team"); headerTeam != "" {
		teamSlug = headerTeam
	}
	// Allow team override via query parameter (for WebSocket connections
	// which cannot set custom headers from the browser).
	if qTeam := r.URL.Query().Get("team"); qTeam != "" && teamSlug == claims.DefaultTeamSlug {
		teamSlug = qTeam
	}
	return teamSlug
}

// checkTeamAccess verifies that non-admin users are members of the requested team.
// Admins/owners can access any team in the org for management purposes.
func (pa *PlatformAuth) checkTeamAccess(ctx context.Context, claims *PlatformClaims, teamSlug string) error {
	if teamSlug == "" || claims.Role == "owner" || claims.Role == "admin" {
		return nil
	}
	orgDS, err := pa.orgResolver.ForOrg(claims.OrgSlug)
	if err != nil {
		slog.Warn("team membership check: failed to access org", "org", claims.OrgSlug, "err", err)
		return fmt.Errorf("failed to verify team membership")
	}
	isMember, err := orgDS.Teams().IsTeamMember(ctx, claims.UserID, teamSlug)
	if err != nil {
		slog.Warn("team membership check failed", "user", claims.UserID, "team", teamSlug, "err", err)
		return fmt.Errorf("failed to verify team membership")
	}
	if !isMember {
		return fmt.Errorf("you are not a member of this team")
	}
	return nil
}

// buildAuthenticatedContext constructs a request context with TenantContext and PlatformUser.
func buildAuthenticatedContext(ctx context.Context, claims *PlatformClaims, teamSlug string) context.Context {
	tc := &store.TenantContext{
		OrgSlug:  claims.OrgSlug,
		TeamSlug: teamSlug,
		UserID:   claims.UserID,
	}
	ctx = store.WithTenantContext(ctx, tc)
	ctx = WithPlatformUser(ctx, &PlatformUser{
		ID:           claims.UserID,
		Email:        claims.Email,
		DisplayName:  claims.DisplayName,
		OrgSlug:      claims.OrgSlug,
		TeamSlug:     teamSlug,
		Role:         claims.Role,
		PlatformRole: claims.PlatformRole,
	})
	return ctx
}

// handleLoopbackAuth handles authentication for loopback (localhost) requests.
// Returns true if the request was handled (served or error written), false if
// the caller should fall through to normal auth (for "never" mode).
//
// Behavior is controlled by the LoopbackBypass config:
//
//	"always"     — pass without token (personal mode default)
//	"with_token" — must carry a valid JWT (platform mode default)
//	"never"      — fall through to normal auth
func (pa *PlatformAuth) handleLoopbackAuth(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
	loopbackMode := pa.authCfg.LoopbackBypass
	if loopbackMode == "" {
		loopbackMode = "with_token" // platform mode default
	}

	if loopbackMode == "never" {
		return false // Fall through to normal auth
	}

	// "always" or "with_token" — attempt to extract token
	token := extractAccessToken(r)

	if token != "" {
		if claims, err := pa.jwt.ValidateAccessToken(token); err == nil {
			teamSlug := resolveTeamSlug(r, claims)
			ctx := buildAuthenticatedContext(r.Context(), claims, teamSlug)
			next.ServeHTTP(w, r.WithContext(ctx))
			return true
		}
	}

	// No valid token found
	if loopbackMode == "always" {
		// Pass through without user context (backward compat for personal mode)
		next.ServeHTTP(w, r)
		return true
	}
	// "with_token" mode: token required but missing/invalid → 401
	respondError(w, http.StatusUnauthorized, "loopback requests require a valid token")
	return true
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// handleUnauthenticated returns 401 for API requests.
// The frontend SPA checks for 401 and shows the login form.
func handleUnauthenticated(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized","message":"authentication required"}`))
}

// isLoopbackRequest checks if the request originates from localhost.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// --- PlatformUser context ---

// PlatformUser represents the authenticated user for the current request.
type PlatformUser struct {
	ID           string
	Email        string
	DisplayName  string
	OrgSlug      string
	TeamSlug     string
	Role         string
	PlatformRole string // "superadmin" or "" (regular user)
}

type platformUserKey struct{}

// WithPlatformUser stores the authenticated platform user in the context.
func WithPlatformUser(ctx context.Context, user *PlatformUser) context.Context {
	return context.WithValue(ctx, platformUserKey{}, user)
}

// GetPlatformUser retrieves the authenticated platform user from the request context.
// Returns nil if not in platform mode or user is not authenticated.
func GetPlatformUser(r *http.Request) *PlatformUser {
	u, _ := r.Context().Value(platformUserKey{}).(*PlatformUser)
	return u
}

// PlatformUserFromContext retrieves the platform user from a context directly.
func PlatformUserFromContext(ctx context.Context) *PlatformUser {
	u, _ := ctx.Value(platformUserKey{}).(*PlatformUser)
	return u
}

// GetPlatformUserFromServices is a helper that extracts user info from Services context.
// Falls back to checking for platform user in context.
func GetPlatformUserFromServices(svc *store.Services, r *http.Request) *PlatformUser {
	if svc == nil || svc.Mode != store.ModePlatform {
		return nil
	}
	return GetPlatformUser(r)
}
