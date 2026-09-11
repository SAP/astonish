package oauthserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestValidateBearer_UserAndServiceMapping(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	server := newValidatorServer(t)
	now := time.Now()

	userToken := signedAccessToken(t, server.signing.key, server.signing.id, jwt.MapClaims{
		"iss": server.config.Issuer, "aud": []string{server.config.Resource}, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"sub": "user", "client_id": "client", "org_id": "org", "team_id": "team", "scope": "tool:execute offline_access", "act": map[string]string{"sub": "worker"},
	})
	principal, err := server.ValidateBearer(context.Background(), "Bearer "+userToken, "", []string{"tool:execute"}, execution.SurfaceMCP)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Kind != execution.PrincipalKindUser || principal.Subject != "user" || principal.Actor != "worker" || principal.ClientID != "client" || principal.Authentication != execution.AuthMethodOAuth || principal.Surface != execution.SurfaceMCP {
		t.Fatalf("unexpected user principal: %#v", principal)
	}

	serviceToken := signedAccessToken(t, server.signing.key, server.signing.id, jwt.MapClaims{
		"iss": server.config.Issuer, "aud": server.config.Resource, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"client_id": "machine", "org_id": "org", "team_id": "team", "scope": "tool:execute",
	})
	principal, err = server.ValidateBearer(context.Background(), "Bearer "+serviceToken, server.config.Resource, nil, execution.SurfaceA2A)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Kind != execution.PrincipalKindService || principal.Subject != "" || principal.ClientID != "machine" {
		t.Fatalf("unexpected service principal: %#v", principal)
	}
}

func TestValidateBearer_PersistedKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	server := newValidatorServer(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	persisted := &signingKey{id: "previous", key: key}
	if err := server.store.SaveOAuthSigningKey(context.Background(), store.OAuthSigningKey{KeyID: persisted.id, Algorithm: "RS256", Status: "active", PublicJWK: persisted.publicJWK(), NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	token := signedAccessToken(t, key, persisted.id, jwt.MapClaims{
		"iss": server.config.Issuer, "aud": server.config.Resource, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"sub": "user", "client_id": "client", "org_id": "org", "team_id": "team", "scope": "tool:execute",
	})
	if _, err := server.ValidateBearer(context.Background(), "Bearer "+token, "", nil, execution.SurfaceCode); err != nil {
		t.Fatalf("validate persisted key: %v", err)
	}
}

func TestValidateBearer_RequiredScopes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	server := newValidatorServer(t)
	token := validValidatorToken(t, server, nil)
	if _, err := server.ValidateBearer(context.Background(), "Bearer "+token, "", []string{"offline_access"}, execution.SurfaceMCP); err == nil {
		t.Fatal("missing required scope was accepted")
	}
}

func TestValidateBearer_Rejects(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	server := newValidatorServer(t)
	now := time.Now()
	base := jwt.MapClaims{"iss": server.config.Issuer, "aud": server.config.Resource, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "sub": "user", "client_id": "client", "org_id": "org", "team_id": "team", "scope": "tool:execute"}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, kid string
		key       *rsa.PrivateKey
		claims    jwt.MapClaims
		method    jwt.SigningMethod
	}{
		{"unknown kid", "unknown", server.signing.key, base, jwt.SigningMethodRS256},
		{"bad signature", server.signing.id, other, base, jwt.SigningMethodRS256},
		{"external issuer", server.signing.id, server.signing.key, copyClaims(base, "iss", "https://external.example"), jwt.SigningMethodRS256},
		{"wrong audience", server.signing.id, server.signing.key, copyClaims(base, "aud", "https://other.example"), jwt.SigningMethodRS256},
		{"expired", server.signing.id, server.signing.key, copyClaims(base, "exp", now.Add(-time.Minute).Unix()), jwt.SigningMethodRS256},
		{"not before", server.signing.id, server.signing.key, copyClaims(base, "nbf", now.Add(time.Hour).Unix()), jwt.SigningMethodRS256},
		{"future issued", server.signing.id, server.signing.key, copyClaims(base, "iat", now.Add(time.Hour).Unix()), jwt.SigningMethodRS256},
		{"missing expiration", server.signing.id, server.signing.key, copyClaims(base, "exp", nil), jwt.SigningMethodRS256},
		{"incomplete tenant", server.signing.id, server.signing.key, copyClaims(base, "team_id", ""), jwt.SigningMethodRS256},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			token := signedToken(t, test.method, test.key, test.kid, test.claims)
			if _, err := server.ValidateBearer(context.Background(), "Bearer "+token, "", nil, execution.SurfaceMCP); err == nil {
				t.Fatal("invalid token was accepted")
			}
		})
	}
	t.Run("non RS256", func(t *testing.T) {
		token := signedToken(t, jwt.SigningMethodHS256, nil, server.signing.id, base)
		if _, err := server.ValidateBearer(context.Background(), "Bearer "+token, "", nil, execution.SurfaceMCP); err == nil {
			t.Fatal("HS256 token was accepted")
		}
	})
	t.Run("missing kid", func(t *testing.T) {
		token := signedToken(t, jwt.SigningMethodRS256, server.signing.key, "", base)
		if _, err := server.ValidateBearer(context.Background(), "Bearer "+token, "", nil, execution.SurfaceMCP); err == nil {
			t.Fatal("kid-less token was accepted")
		}
	})
}

func newValidatorServer(t *testing.T) *Server {
	t.Helper()
	backend := &memoryStore{clients: map[string]*store.OAuthClient{}, codes: map[string]store.OAuthAuthorization{}}
	server, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, func(context.Context, *http.Request) (Subject, error) { return Subject{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func validValidatorToken(t *testing.T, server *Server, overrides jwt.MapClaims) string {
	t.Helper()
	claims := jwt.MapClaims{"iss": server.config.Issuer, "aud": server.config.Resource, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "sub": "user", "client_id": "client", "org_id": "org", "team_id": "team", "scope": "tool:execute"}
	for key, value := range overrides {
		claims[key] = value
	}
	return signedAccessToken(t, server.signing.key, server.signing.id, claims)
}

func signedAccessToken(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	return signedToken(t, jwt.SigningMethodRS256, key, kid, claims)
}
func signedToken(t *testing.T, method jwt.SigningMethod, key any, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	if kid != "" {
		token.Header["kid"] = kid
	}
	if method == jwt.SigningMethodHS256 {
		key = []byte("not-an-rsa-key")
	}
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func copyClaims(claims jwt.MapClaims, key string, value any) jwt.MapClaims {
	copy := jwt.MapClaims{}
	for name, claim := range claims {
		copy[name] = claim
	}
	if value == nil {
		delete(copy, key)
	} else {
		copy[key] = value
	}
	return copy
}
