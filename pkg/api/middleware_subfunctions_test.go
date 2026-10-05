package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestIsAuthExemptPath(t *testing.T) {
	tests := []struct {
		path   string
		exempt bool
	}{
		// SPA static assets — always exempt
		{"/", true},
		{"/index.html", true},
		{"/assets/app.js", true},
		{"/favicon.ico", true},

		// Auth endpoints — exempt
		{"/api/auth/login", true},
		{"/api/auth/register", true},
		{"/api/auth/refresh", true},
		{"/api/auth/logout", true},
		{"/api/auth/sso/callback", true},

		// Platform setup endpoints — exempt
		{"/api/platform/mode", true},
		{"/api/platform/init", true},
		{"/api/platform/init/status", true},

		// Health endpoints — exempt
		{"/api/healthz", true},
		{"/api/readyz", true},

		// Migration endpoints — exempt
		{"/api/migration/status", true},
		{"/api/migration/run", true},

		// Regular API endpoints — NOT exempt
		{"/api/chat", false},
		{"/api/memories/team", false},
		{"/api/sessions", false},
		{"/api/credentials", false},
		{"/api/platform/admin/users", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isAuthExemptPath(tt.path)
			if got != tt.exempt {
				t.Errorf("isAuthExemptPath(%q) = %v, want %v", tt.path, got, tt.exempt)
			}
		})
	}
}

func TestValidateNoAuthHost(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		expectErr bool
	}{
		{name: "localhost", host: "localhost:9393"},
		{name: "IPv4 loopback", host: "127.0.0.1:9393"},
		{name: "IPv6 loopback", host: "[::1]:9393"},
		{name: "foreign host", host: "evil.example.com:9393", expectErr: true},
		{name: "deceptive localhost", host: "localhost.evil.com:9393", expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/api/test", nil)
			req.Host = tt.host
			err := validateNoAuthHost(req)
			if tt.expectErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.expectErr && err != nil {
				t.Fatalf("validateNoAuthHost() error = %v", err)
			}
		})
	}
}

func TestValidateNoAuthOrigin(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		origin    string
		expectErr bool
	}{
		{name: "GET with foreign origin", method: http.MethodGet, origin: "http://evil.example.com"},
		{name: "POST without origin", method: http.MethodPost},
		{name: "POST from localhost", method: http.MethodPost, origin: "http://localhost:9393"},
		{name: "POST from IPv4 loopback", method: http.MethodPost, origin: "http://127.0.0.1:9393"},
		{name: "POST from IPv6 loopback", method: http.MethodPost, origin: "http://[::1]:9393"},
		{name: "POST from HTTPS localhost", method: http.MethodPost, origin: "https://localhost:9393"},
		{name: "POST from foreign origin", method: http.MethodPost, origin: "http://evil.example.com", expectErr: true},
		{name: "POST from deceptive localhost hostname", method: http.MethodPost, origin: "http://localhost.evil.com", expectErr: true},
		{name: "DELETE from foreign origin", method: http.MethodDelete, origin: "http://attacker.local", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/test", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			err := validateNoAuthOrigin(req)
			if tt.expectErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.expectErr && err != nil {
				t.Fatalf("validateNoAuthOrigin() error = %v", err)
			}
		})
	}
}

func TestValidateCSRF(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		headers   map[string]string
		expectErr bool
	}{
		{
			name:      "GET request is exempt",
			method:    "GET",
			headers:   nil,
			expectErr: false,
		},
		{
			name:      "HEAD request is exempt",
			method:    "HEAD",
			headers:   nil,
			expectErr: false,
		},
		{
			name:      "OPTIONS request is exempt",
			method:    "OPTIONS",
			headers:   nil,
			expectErr: false,
		},
		{
			name:      "Bearer auth is exempt",
			method:    "POST",
			headers:   map[string]string{"Authorization": "Bearer token123"},
			expectErr: false,
		},
		{
			name:      "POST with X-Requested-With passes",
			method:    "POST",
			headers:   map[string]string{"X-Requested-With": "XMLHttpRequest"},
			expectErr: false,
		},
		{
			name:      "POST with Content-Type application/json passes",
			method:    "POST",
			headers:   map[string]string{"Content-Type": "application/json"},
			expectErr: false,
		},
		{
			name:      "POST without CSRF headers fails",
			method:    "POST",
			headers:   nil,
			expectErr: true,
		},
		{
			name:      "DELETE without CSRF headers fails",
			method:    "DELETE",
			headers:   map[string]string{"Content-Type": "text/plain"},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/test", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			err := validateCSRF(req)
			if tt.expectErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}

func TestResolveTeamSlug(t *testing.T) {
	tests := []struct {
		name         string
		claims       *PlatformClaims
		headerTeam   string
		queryTeam    string
		expectedSlug string
	}{
		{
			name:         "uses JWT default when no override",
			claims:       &PlatformClaims{DefaultTeamSlug: "general"},
			expectedSlug: "general",
		},
		{
			name:         "header override takes priority",
			claims:       &PlatformClaims{DefaultTeamSlug: "general"},
			headerTeam:   "engineering",
			expectedSlug: "engineering",
		},
		{
			name:         "query param override works",
			claims:       &PlatformClaims{DefaultTeamSlug: "general"},
			queryTeam:    "ops",
			expectedSlug: "ops",
		},
		{
			name:         "header beats query param",
			claims:       &PlatformClaims{DefaultTeamSlug: "general"},
			headerTeam:   "engineering",
			queryTeam:    "ops",
			expectedSlug: "engineering",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/test"
			if tt.queryTeam != "" {
				url += "?team=" + tt.queryTeam
			}
			req := httptest.NewRequest("GET", url, nil)
			if tt.headerTeam != "" {
				req.Header.Set("X-Astonish-Team", tt.headerTeam)
			}
			got := resolveTeamSlug(req, tt.claims)
			if got != tt.expectedSlug {
				t.Errorf("resolveTeamSlug() = %q, want %q", got, tt.expectedSlug)
			}
		})
	}
}

func TestResolveCredentialHeader(t *testing.T) {
	tests := []struct {
		name        string
		cred        *store.Credential
		expectKey   string
		expectValue string
		expectErr   bool
	}{
		{
			name:      "nil credential returns error",
			cred:      nil,
			expectErr: true,
		},
		{
			name:        "API key with custom header",
			cred:        &store.Credential{Type: store.CredAPIKey, Header: "X-API-Key", Value: "secret123"},
			expectKey:   "X-API-Key",
			expectValue: "secret123",
		},
		{
			name:        "API key defaults to Authorization header",
			cred:        &store.Credential{Type: store.CredAPIKey, Value: "token-abc"},
			expectKey:   "Authorization",
			expectValue: "token-abc",
		},
		{
			name:        "Bearer token",
			cred:        &store.Credential{Type: store.CredBearer, Token: "jwt.token.here"},
			expectKey:   "Authorization",
			expectValue: "Bearer jwt.token.here",
		},
		{
			name:        "Basic auth",
			cred:        &store.Credential{Type: store.CredBasic, Username: "user", Password: "pass"},
			expectKey:   "Authorization",
			expectValue: "Basic dXNlcjpwYXNz", // base64("user:pass")
		},
		{
			name:      "Password credential returns error",
			cred:      &store.Credential{Type: store.CredPassword, Password: "secret"},
			expectErr: true,
		},
		{
			name:      "Unknown type returns error",
			cred:      &store.Credential{Type: "unknown_type"},
			expectErr: true,
		},
		{
			name:        "OAuth auth_code with stored access token",
			cred:        &store.Credential{Type: store.CredOAuthAuthCode, AccessToken: "access-tok-123"},
			expectKey:   "Authorization",
			expectValue: "Bearer access-tok-123",
		},
		{
			name:      "OAuth auth_code without access token and no fetcher",
			cred:      &store.Credential{Type: store.CredOAuthAuthCode},
			expectErr: true,
		},
		{
			name:      "OAuth client_credentials without fetcher",
			cred:      &store.Credential{Type: store.CredOAuthClientCreds, ClientID: "id", ClientSecret: "sec"},
			expectErr: true,
		},
		{
			name:      "Keystone without fetcher",
			cred:      &store.Credential{Type: store.CredOpenStackKeystone, AuthURL: "https://identity.example.com/v3/auth/tokens"},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, value, err := store.ResolveCredentialHeader("test-cred", tt.cred, nil)
			if tt.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key != tt.expectKey {
				t.Errorf("headerKey = %q, want %q", key, tt.expectKey)
			}
			if value != tt.expectValue {
				t.Errorf("headerValue = %q, want %q", value, tt.expectValue)
			}
		})
	}
}

func TestResolveCredentialHeader_OAuthFetcher(t *testing.T) {
	// Test that the OAuth fetcher is called correctly
	fetcherCalled := false
	fetcher := func(cred *store.Credential) (string, error) {
		fetcherCalled = true
		if cred.ClientID != "my-client" {
			t.Errorf("fetcher received wrong ClientID: %q", cred.ClientID)
		}
		return "fetched-token-xyz", nil
	}

	cred := &store.Credential{
		Type:         store.CredOAuthClientCreds,
		ClientID:     "my-client",
		ClientSecret: "my-secret",
		AuthURL:      "https://auth.example.com/token",
	}

	key, value, err := store.ResolveCredentialHeader("oauth-cred", cred, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fetcherCalled {
		t.Error("OAuth fetcher was not called")
	}
	if key != "Authorization" {
		t.Errorf("headerKey = %q, want Authorization", key)
	}
	if value != "Bearer fetched-token-xyz" {
		t.Errorf("headerValue = %q, want 'Bearer fetched-token-xyz'", value)
	}
}

func TestResolveCredentialHeader_KeystoneFetcher(t *testing.T) {
	fetcherCalled := false
	fetcher := func(cred *store.Credential) (string, error) {
		fetcherCalled = true
		if cred.AuthURL != "https://identity.example.com/v3/auth/tokens" {
			t.Errorf("fetcher received wrong AuthURL: %q", cred.AuthURL)
		}
		return "gAAAA-keystone-token", nil
	}

	cred := &store.Credential{
		Type:                        store.CredOpenStackKeystone,
		AuthURL:                     "https://identity.example.com/v3/auth/tokens",
		ApplicationCredentialID:     "app-id",
		ApplicationCredentialSecret: "app-secret",
	}

	key, value, err := store.ResolveCredentialHeader("keystone-cred", cred, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fetcherCalled {
		t.Error("Keystone fetcher was not called")
	}
	if key != "X-Auth-Token" {
		t.Errorf("headerKey = %q, want X-Auth-Token", key)
	}
	if value != "gAAAA-keystone-token" {
		t.Errorf("headerValue = %q, want 'gAAAA-keystone-token'", value)
	}
}

func TestExtractAccessToken(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(r *http.Request)
		expect string
	}{
		{
			name:   "Bearer header takes priority",
			setup:  func(r *http.Request) { r.Header.Set("Authorization", "Bearer header-token") },
			expect: "header-token",
		},
		{
			name: "Falls back to cookie",
			setup: func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: accessCookieName, Value: "cookie-token"})
			},
			expect: "cookie-token",
		},
		{
			name:   "Returns empty when neither present",
			setup:  func(r *http.Request) {},
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/test", nil)
			tt.setup(req)
			got := extractAccessToken(req)
			if got != tt.expect {
				t.Errorf("extractAccessToken() = %q, want %q", got, tt.expect)
			}
		})
	}
}
