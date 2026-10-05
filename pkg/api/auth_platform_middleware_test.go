package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
)

// testPlatformAuth creates a minimal PlatformAuth for middleware testing.
func testPlatformAuth(t *testing.T) *PlatformAuth {
	t.Helper()
	return &PlatformAuth{
		jwt: NewJWTIssuer("test-secret-for-middleware", 15*time.Minute, 90*24*time.Hour),
	}
}

func TestBuildAuthenticatedContextRejectsIncompletePrincipal(t *testing.T) {
	t.Parallel()

	ctx, err := buildAuthenticatedContext(context.Background(), &PlatformClaims{
		UserID:  "user-123",
		OrgSlug: "my-org",
	}, "")
	if err == nil {
		t.Fatal("expected incomplete platform principal to be rejected")
	}
	if ctx != nil {
		t.Fatal("expected no context after principal validation failure")
	}
}

// (HTML, JS, CSS, images) pass through without authentication.
func TestPlatformAuthMiddleware_AllowsSPAAssets(t *testing.T) {
	pa := testPlatformAuth(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := PlatformAuthMiddleware(pa, inner)

	paths := []string{
		"/",
		"/index.html",
		"/assets/index-abc123.js",
		"/assets/index-abc123.css",
		"/favicon.ico",
		"/1.12.0/index.html",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			req.RemoteAddr = "192.168.1.100:54321" // non-loopback
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("SPA path %q from remote IP should be allowed, got status %d", path, w.Code)
			}
		})
	}
}

func TestPlatformAuthHandleMe_NoAuthRejectsRemote(t *testing.T) {
	pa := testPlatformAuth(t)
	pa.noAuthMode = true

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	rec := httptest.NewRecorder()
	pa.handleMe(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("remote no-auth /api/auth/me status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestPlatformAuthMiddleware_NoAuthPreservesScopedPrincipal(t *testing.T) {
	pa := testPlatformAuth(t)
	pa.noAuthMode = true

	scoped := execution.Principal{
		Kind:           execution.PrincipalKindUser,
		Authentication: execution.AuthMethodOAuth,
		Surface:        execution.SurfaceMCP,
		Subject:        "oauth-user",
		OrgSlug:        "scoped-org",
		TeamSlug:       "scoped-team",
		Scopes:         []string{"mcp:read"},
		Authenticated:  true,
	}
	ctx, err := execution.WithPrincipal(context.Background(), scoped)
	if err != nil {
		t.Fatalf("WithPrincipal() error: %v", err)
	}

	var got execution.Principal
	var present bool
	handler := PlatformAuthMiddleware(pa, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, present = execution.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || !present {
		t.Fatalf("scoped principal request status = %d, present = %t; want 204 and principal", rec.Code, present)
	}
	if !reflect.DeepEqual(got, scoped) {
		t.Fatalf("principal = %#v, want %#v", got, scoped)
	}
}
func TestPlatformAuthMiddleware_NoAuthRejectsForeignOriginPOST(t *testing.T) {
	pa := testPlatformAuth(t)
	pa.noAuthMode = true

	called := false
	handler := PlatformAuthMiddleware(pa, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/agents", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("foreign-origin no-auth request reached the downstream handler")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign-origin no-auth request status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestPlatformAuthMiddleware_NoAuthRejectsForeignHostGET(t *testing.T) {
	pa := testPlatformAuth(t)
	pa.noAuthMode = true

	called := false
	handler := PlatformAuthMiddleware(pa, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://evil.example.com/api/agents", nil)
	req.Host = "evil.example.com"
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("foreign-host no-auth request reached the downstream handler")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign-host no-auth request status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestPlatformAuthMiddleware_NoAuthRejectsRemote(t *testing.T) {
	pa := testPlatformAuth(t)
	pa.noAuthMode = true

	called := false
	handler := PlatformAuthMiddleware(pa, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("remote no-auth request reached the downstream handler")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("remote no-auth request status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestPlatformAuthMiddleware_BlocksAPIWithoutAuth(t *testing.T) {
	pa := testPlatformAuth(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := PlatformAuthMiddleware(pa, inner)

	paths := []string{
		"/api/agents",
		"/api/sessions",
		"/api/settings",
		"/api/chat/run",
		"/api/memories/search",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			req.RemoteAddr = "192.168.1.100:54321"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("API path %q without auth should be 401, got %d", path, w.Code)
			}
		})
	}
}

// TestPlatformAuthMiddleware_AllowsAuthEndpoints verifies that /api/auth/*
// endpoints are accessible without authentication.
func TestPlatformAuthMiddleware_AllowsAuthEndpoints(t *testing.T) {
	pa := testPlatformAuth(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := PlatformAuthMiddleware(pa, inner)

	paths := []string{
		"/api/auth/register",
		"/api/auth/login",
		"/api/auth/refresh",
		"/api/auth/setup-status",
		"/api/auth/me",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("POST", path, nil)
			req.RemoteAddr = "192.168.1.100:54321"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("auth path %q should be accessible, got status %d", path, w.Code)
			}
		})
	}
}

func TestPlatformAuthMiddleware_AllowsMCPProtocolEndpointToUseOAuthBearer(t *testing.T) {
	pa := testPlatformAuth(t)
	pa.authCfg = config.PlatformAuthConfig{LoopbackBypass: "with_token"}

	called := false
	handler := PlatformAuthMiddleware(pa, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, MCPPath, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer oauth-access-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("MCP OAuth bearer request status = %d, called = %t; want protocol handler to receive it", rec.Code, called)
	}
}

func TestPlatformAuthMiddleware_AllowsSlackWebhookEndpoints(t *testing.T) {
	pa := testPlatformAuth(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := PlatformAuthMiddleware(pa, inner)

	paths := []string{
		"/api/slack/events",
		"/api/slack/commands",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("POST", path, nil)
			req.RemoteAddr = "192.168.1.100:54321"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Slack webhook path %q should be accessible, got status %d", path, w.Code)
			}
		})
	}
}

// TestPlatformAuthMiddleware_AllowsPlatformSetupEndpoints verifies that
// /api/platform/* endpoints pass without auth (needed before first user).
func TestPlatformAuthMiddleware_AllowsPlatformSetupEndpoints(t *testing.T) {
	pa := testPlatformAuth(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := PlatformAuthMiddleware(pa, inner)

	paths := []string{
		"/api/platform/mode",
		"/api/platform/init",
		"/api/platform/init/status",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			req.RemoteAddr = "192.168.1.100:54321"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("platform path %q should be accessible, got status %d", path, w.Code)
			}
		})
	}
}

// TestPlatformAuthMiddleware_AllowsMigrationEndpoints verifies that
// /api/migration/* endpoints pass without auth (migration runs before first user).
func TestPlatformAuthMiddleware_AllowsMigrationEndpoints(t *testing.T) {
	pa := testPlatformAuth(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := PlatformAuthMiddleware(pa, inner)

	paths := []string{
		"/api/migration/status",
		"/api/migration/start",
		"/api/migration/progress",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			req.RemoteAddr = "192.168.1.100:54321"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("migration path %q should be accessible, got status %d", path, w.Code)
			}
		})
	}
}

// TestPlatformAuthMiddleware_AllowsLoopback verifies that loopback requests
// bypass auth when LoopbackBypass is set to "always".
func TestPlatformAuthMiddleware_AllowsLoopback(t *testing.T) {
	pa := testPlatformAuth(t)
	pa.authCfg = config.PlatformAuthConfig{LoopbackBypass: "always"}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := PlatformAuthMiddleware(pa, inner)

	loopbackAddrs := []string{"127.0.0.1:12345", "[::1]:12345"}

	for _, addr := range loopbackAddrs {
		t.Run(addr, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/agents", nil)
			req.RemoteAddr = addr
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("loopback %q should be allowed in 'always' mode, got status %d", addr, w.Code)
			}
		})
	}
}

// TestPlatformAuthMiddleware_ValidJWT verifies that requests with a valid
// JWT access token cookie are allowed and the tenant context is populated.
func TestPlatformAuthMiddleware_ValidJWT(t *testing.T) {
	pa := testPlatformAuth(t)

	token, err := pa.jwt.IssueAccessToken("user-123", "test@example.com", "Test User", "my-org", "ops", "admin", "")
	if err != nil {
		t.Fatalf("IssueAccessToken() error: %v", err)
	}

	var gotUser *PlatformUser
	var gotPrincipal execution.Principal
	var principalPresent bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = GetPlatformUser(r)
		gotPrincipal, principalPresent = execution.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler := PlatformAuthMiddleware(pa, inner)

	req := httptest.NewRequest("GET", "/api/agents", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	req.AddCookie(&http.Cookie{Name: accessCookieName, Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("valid JWT should be allowed, got status %d", w.Code)
	}
	if gotUser == nil {
		t.Fatal("expected PlatformUser to be set in context")
	}
	if gotUser.ID != "user-123" {
		t.Errorf("user ID = %q, want %q", gotUser.ID, "user-123")
	}
	if gotUser.OrgSlug != "my-org" {
		t.Errorf("org slug = %q, want %q", gotUser.OrgSlug, "my-org")
	}
	if gotUser.TeamSlug != "ops" {
		t.Errorf("team slug = %q, want %q", gotUser.TeamSlug, "ops")
	}
	if !principalPresent {
		t.Fatal("expected canonical principal to be set in context")
	}
	if gotPrincipal.Subject != "user-123" || gotPrincipal.OrgSlug != "my-org" || gotPrincipal.TeamSlug != "ops" {
		t.Errorf("principal = %#v, want user-123 in my-org/ops", gotPrincipal)
	}
}

// TestPlatformAuthMiddleware_TeamOverrideHeader verifies that X-Astonish-Team
// header overrides the default team from the JWT.
func TestPlatformAuthMiddleware_TeamOverrideHeader(t *testing.T) {
	pa := testPlatformAuth(t)

	token, err := pa.jwt.IssueAccessToken("user-123", "test@example.com", "Test User", "my-org", "ops", "admin", "")
	if err != nil {
		t.Fatalf("IssueAccessToken() error: %v", err)
	}

	var gotUser *PlatformUser
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = GetPlatformUser(r)
		w.WriteHeader(http.StatusOK)
	})
	handler := PlatformAuthMiddleware(pa, inner)

	req := httptest.NewRequest("GET", "/api/agents", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	req.AddCookie(&http.Cookie{Name: accessCookieName, Value: token})
	req.Header.Set("X-Astonish-Team", "sre")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("valid JWT should be allowed, got status %d", w.Code)
	}
	if gotUser.TeamSlug != "sre" {
		t.Errorf("team slug = %q, want %q (overridden by header)", gotUser.TeamSlug, "sre")
	}
}

// TestPlatformAuthMiddleware_ExpiredJWT verifies that expired tokens get 401.
func TestPlatformAuthMiddleware_ExpiredJWT(t *testing.T) {
	// Create an issuer with 1ms TTL
	pa := &PlatformAuth{
		jwt: NewJWTIssuer("test-secret", 1*time.Millisecond, 90*24*time.Hour),
	}

	token, err := pa.jwt.IssueAccessToken("user-123", "test@example.com", "Test", "org", "team", "member", "")
	if err != nil {
		t.Fatalf("IssueAccessToken() error: %v", err)
	}

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := PlatformAuthMiddleware(pa, inner)

	req := httptest.NewRequest("GET", "/api/agents", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	req.AddCookie(&http.Cookie{Name: accessCookieName, Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expired JWT should get 401, got %d", w.Code)
	}
}

// TestPlatformAuthMiddleware_InvalidJWT verifies that garbage tokens get 401.
func TestPlatformAuthMiddleware_InvalidJWT(t *testing.T) {
	pa := testPlatformAuth(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := PlatformAuthMiddleware(pa, inner)

	req := httptest.NewRequest("GET", "/api/agents", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	req.AddCookie(&http.Cookie{Name: accessCookieName, Value: "garbage-token-value"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid JWT should get 401, got %d", w.Code)
	}
}
