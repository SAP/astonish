package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/oauthserver"
	"github.com/SAP/astonish/pkg/store"
)

type botEvidenceOAuthStore struct {
	store.OAuthServerStore
	keys []store.OAuthSigningKey
}

func (s *botEvidenceOAuthStore) ListOAuthSigningKeys(context.Context) ([]store.OAuthSigningKey, error) {
	return s.keys, nil
}
func (s *botEvidenceOAuthStore) SaveOAuthSigningKey(_ context.Context, k store.OAuthSigningKey) error {
	s.keys = append(s.keys, k)
	return nil
}

func testBotOAuthEvidenceCapabilityDeny(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	backend := &botEvidenceOAuthStore{}
	server, err := oauthserver.New(oauthserver.Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, func(context.Context, *http.Request) (oauthserver.Subject, error) { return oauthserver.Subject{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	backend.keys = append(backend.keys, store.OAuthSigningKey{KeyID: "acceptance", Algorithm: "RS256", Status: "active", PublicJWK: map[string]any{"kty": "RSA", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}})
	claims := jwt.MapClaims{"iss": "https://issuer.example", "aud": "https://api.example", "exp": time.Now().Add(time.Minute).Unix(), "sub": "user", "org_id": "org", "team_id": "team", "client_id": "client", "scope": "chat"}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "acceptance"
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	// The actual validator checks the signed request token before the middleware
	// preserves it and the actual capability authorizer denies tool execution.
	called := false
	downstream := PlatformAuthMiddleware(testPlatformAuth(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		p, _ := execution.PrincipalFromContext(r.Context())
		e, ok := execution.AuthenticationEvidenceFromContext(r.Context())
		if !ok || e.Method != execution.AuthMethodOAuth || e.LocalLoopback {
			t.Error("oauth evidence lost")
		}
		if err := (execution.CapabilityAuthorizer{}).Authorize(p, execution.CapabilityToolExecute); err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.WriteHeader(204)
	}))
	adapter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, e, err := server.ValidateBearerWithEvidence(r.Context(), r.Header.Get("Authorization"), "", []string{"chat"}, execution.SurfaceStudio)
		if err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		ctx, err := execution.WithPrincipal(r.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		ctx, err = execution.WithAuthenticationEvidence(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		downstream.ServeHTTP(w, r.WithContext(ctx))
	})
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/bots", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	adapter.ServeHTTP(rec, req)
	if !called || rec.Code != 403 {
		t.Fatalf("capability status %d", rec.Code)
	}
}
func testBotLocalEvidence(t *testing.T) {
	pa := testPlatformAuth(t)
	pa.noAuthMode = true
	pa.claimsEmail = config.SingleUserEmail
	pa.claims = &PlatformClaims{UserID: "local", OrgSlug: "org", DefaultTeamSlug: "team", Role: "owner"}
	handler := PlatformAuthMiddleware(pa, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e, ok := execution.AuthenticationEvidenceFromContext(r.Context())
		if !ok || !e.LocalLoopback || e.Method != execution.AuthMethodLocalOS {
			t.Errorf("local evidence %+v", e)
		}
		w.WriteHeader(204)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/bots", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("local status %d", rec.Code)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("proxy status %d", rec.Code)
	}
}

// Exercises the real platform authentication adapter, not a preinstalled principal.
func TestBotAuthenticationEvidenceHTTPAcceptance(t *testing.T) {
	t.Run("oauth capability deny", testBotOAuthEvidenceCapabilityDeny)
	t.Run("local evidence", testBotLocalEvidence)
	pa := testPlatformAuth(t)
	token, err := pa.jwt.IssueAccessToken("user", "user@example.com", "User", "org", "team", "admin", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, local := range []bool{false, true} {
		called := false
		handler := PlatformAuthMiddleware(pa, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			e, ok := execution.AuthenticationEvidenceFromContext(r.Context())
			if !ok || e.Method != execution.AuthMethodPlatformJWT || e.LocalLoopback || !e.ExpiresAt.After(time.Now()) {
				t.Errorf("missing token evidence: %+v", e)
			}
			p, _ := execution.PrincipalFromContext(r.Context())
			if err := (execution.CapabilityAuthorizer{}).Authorize(p, execution.CapabilityChat); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		req := httptest.NewRequest(http.MethodPost, "http://localhost/api/bots", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if local {
			req.RemoteAddr = "127.0.0.1:1234"
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if !called || rec.Code != 204 {
			t.Fatalf("valid token status %d", rec.Code)
		}
		req.Header.Set("Authorization", "Bearer invalid")
		called = false
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if called || rec.Code != 401 {
			t.Fatalf("invalid token status %d", rec.Code)
		}
	}
}
