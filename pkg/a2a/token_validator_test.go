package a2a

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testJWKSServer creates an httptest server that serves a JWKS containing the given RSA public key.
func testJWKSServer(t *testing.T, kid string, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()

	jwks := map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "RSA",
				"kid": kid,
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(server.Close)
	return server
}

// signToken creates a signed JWT with the given claims and key.
func signToken(t *testing.T, kid string, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	tokenStr, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}
	return tokenStr
}

func TestTokenValidator_ValidToken(t *testing.T) {
	// Generate RSA key pair
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	kid := "test-key-1"

	// Create JWKS server
	jwksServer := testJWKSServer(t, kid, &privKey.PublicKey)

	issuerURL := "https://idp.example.com"
	audience := "astonish-api"

	cfg := TokenValidatorConfig{
		Issuers: []TrustedIssuer{
			{
				ID:        "issuer-1",
				Name:      "Test IdP",
				Issuer:    issuerURL,
				JWKSURL:   jwksServer.URL,
				Audience:  audience,
				UserClaim: "sub",
				OrgID:     "org-1",
			},
		},
		Agents:            []AllowedAgent{},
		RequireActorClaim: false,
	}

	validator := NewTokenValidator(cfg)

	claims := jwt.MapClaims{
		"iss": issuerURL,
		"aud": audience,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}

	tokenStr := signToken(t, kid, privKey, claims)

	result, err := validator.Validate(tokenStr)
	if err != nil {
		t.Fatalf("expected valid token, got error: %v", err)
	}

	if result.UserIdentifier != "user-123" {
		t.Errorf("expected UserIdentifier=user-123, got %q", result.UserIdentifier)
	}
	if result.Issuer != issuerURL {
		t.Errorf("expected Issuer=%q, got %q", issuerURL, result.Issuer)
	}
	if result.IssuerID != "issuer-1" {
		t.Errorf("expected IssuerID=issuer-1, got %q", result.IssuerID)
	}
	if result.OrgID != "org-1" {
		t.Errorf("expected OrgID=org-1, got %q", result.OrgID)
	}
	if result.ActorIdentifier != "" {
		t.Errorf("expected empty ActorIdentifier, got %q", result.ActorIdentifier)
	}
}

func TestTokenValidator_WrongIssuer(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	kid := "test-key-1"
	jwksServer := testJWKSServer(t, kid, &privKey.PublicKey)

	cfg := TokenValidatorConfig{
		Issuers: []TrustedIssuer{
			{
				ID:      "issuer-1",
				Issuer:  "https://trusted.example.com",
				JWKSURL: jwksServer.URL,
			},
		},
	}

	validator := NewTokenValidator(cfg)

	claims := jwt.MapClaims{
		"iss": "https://evil.example.com",
		"aud": "some-aud",
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	}

	tokenStr := signToken(t, kid, privKey, claims)

	_, err = validator.Validate(tokenStr)
	if err == nil {
		t.Fatal("expected error for wrong issuer, got nil")
	}
	t.Logf("got expected error: %v", err)
}

func TestTokenValidator_WrongAudience(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	kid := "test-key-1"
	jwksServer := testJWKSServer(t, kid, &privKey.PublicKey)

	issuerURL := "https://idp.example.com"

	cfg := TokenValidatorConfig{
		Issuers: []TrustedIssuer{
			{
				ID:        "issuer-1",
				Issuer:    issuerURL,
				JWKSURL:   jwksServer.URL,
				Audience:  "correct-audience",
				UserClaim: "sub",
				OrgID:     "org-1",
			},
		},
	}

	validator := NewTokenValidator(cfg)

	claims := jwt.MapClaims{
		"iss": issuerURL,
		"aud": "wrong-audience",
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	}

	tokenStr := signToken(t, kid, privKey, claims)

	_, err = validator.Validate(tokenStr)
	if err == nil {
		t.Fatal("expected error for wrong audience, got nil")
	}
	t.Logf("got expected error: %v", err)
}

func TestTokenValidator_ExpiredToken(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	kid := "test-key-1"
	jwksServer := testJWKSServer(t, kid, &privKey.PublicKey)

	issuerURL := "https://idp.example.com"
	audience := "astonish-api"

	cfg := TokenValidatorConfig{
		Issuers: []TrustedIssuer{
			{
				ID:        "issuer-1",
				Issuer:    issuerURL,
				JWKSURL:   jwksServer.URL,
				Audience:  audience,
				UserClaim: "sub",
				OrgID:     "org-1",
			},
		},
	}

	validator := NewTokenValidator(cfg)

	claims := jwt.MapClaims{
		"iss": issuerURL,
		"aud": audience,
		"sub": "user-123",
		"exp": time.Now().Add(-time.Hour).Unix(), // expired
	}

	tokenStr := signToken(t, kid, privKey, claims)

	_, err = validator.Validate(tokenStr)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
	t.Logf("got expected error: %v", err)
}

func TestTokenValidator_ActClaimWithMatchingAgent(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	kid := "test-key-1"
	jwksServer := testJWKSServer(t, kid, &privKey.PublicKey)

	issuerURL := "https://idp.example.com"
	audience := "astonish-api"

	cfg := TokenValidatorConfig{
		Issuers: []TrustedIssuer{
			{
				ID:        "issuer-1",
				Issuer:    issuerURL,
				JWKSURL:   jwksServer.URL,
				Audience:  audience,
				UserClaim: "sub",
				OrgID:     "org-1",
			},
		},
		Agents: []AllowedAgent{
			{
				ID:       "agent-1",
				Name:     "Test Agent",
				ActorSub: "service-abc",
				IssuerID: "issuer-1",
				OrgID:    "org-1",
				Enabled:  true,
			},
		},
	}

	validator := NewTokenValidator(cfg)

	claims := jwt.MapClaims{
		"iss": issuerURL,
		"aud": audience,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
		"act": map[string]interface{}{
			"sub": "service-abc",
		},
	}

	tokenStr := signToken(t, kid, privKey, claims)

	result, err := validator.Validate(tokenStr)
	if err != nil {
		t.Fatalf("expected valid token with act claim, got error: %v", err)
	}

	if result.ActorIdentifier != "service-abc" {
		t.Errorf("expected ActorIdentifier=service-abc, got %q", result.ActorIdentifier)
	}
	if result.UserIdentifier != "user-123" {
		t.Errorf("expected UserIdentifier=user-123, got %q", result.UserIdentifier)
	}
}

func TestTokenValidator_ActClaimAgentNotAllowed(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	kid := "test-key-1"
	jwksServer := testJWKSServer(t, kid, &privKey.PublicKey)

	issuerURL := "https://idp.example.com"
	audience := "astonish-api"

	cfg := TokenValidatorConfig{
		Issuers: []TrustedIssuer{
			{
				ID:        "issuer-1",
				Issuer:    issuerURL,
				JWKSURL:   jwksServer.URL,
				Audience:  audience,
				UserClaim: "sub",
				OrgID:     "org-1",
			},
		},
		Agents: []AllowedAgent{
			{
				ID:       "agent-1",
				Name:     "Other Agent",
				ActorSub: "service-other",
				IssuerID: "issuer-1",
				OrgID:    "org-1",
				Enabled:  true,
			},
		},
	}

	validator := NewTokenValidator(cfg)

	claims := jwt.MapClaims{
		"iss": issuerURL,
		"aud": audience,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
		"act": map[string]interface{}{
			"sub": "service-unauthorized",
		},
	}

	tokenStr := signToken(t, kid, privKey, claims)

	_, err = validator.Validate(tokenStr)
	if err == nil {
		t.Fatal("expected error for unauthorized agent, got nil")
	}
	t.Logf("got expected error: %v", err)
}

func TestTokenValidator_NoActClaimRequireActorTrue(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	kid := "test-key-1"
	jwksServer := testJWKSServer(t, kid, &privKey.PublicKey)

	issuerURL := "https://idp.example.com"
	audience := "astonish-api"

	cfg := TokenValidatorConfig{
		Issuers: []TrustedIssuer{
			{
				ID:        "issuer-1",
				Issuer:    issuerURL,
				JWKSURL:   jwksServer.URL,
				Audience:  audience,
				UserClaim: "sub",
				OrgID:     "org-1",
			},
		},
		RequireActorClaim: true,
	}

	validator := NewTokenValidator(cfg)

	claims := jwt.MapClaims{
		"iss": issuerURL,
		"aud": audience,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	}

	tokenStr := signToken(t, kid, privKey, claims)

	_, err = validator.Validate(tokenStr)
	if err == nil {
		t.Fatal("expected error when act claim is required but missing, got nil")
	}
	t.Logf("got expected error: %v", err)
}

func TestTokenValidator_NoActClaimRequireActorFalse(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	kid := "test-key-1"
	jwksServer := testJWKSServer(t, kid, &privKey.PublicKey)

	issuerURL := "https://idp.example.com"
	audience := "astonish-api"

	cfg := TokenValidatorConfig{
		Issuers: []TrustedIssuer{
			{
				ID:        "issuer-1",
				Issuer:    issuerURL,
				JWKSURL:   jwksServer.URL,
				Audience:  audience,
				UserClaim: "sub",
				OrgID:     "org-1",
			},
		},
		RequireActorClaim: false,
	}

	validator := NewTokenValidator(cfg)

	claims := jwt.MapClaims{
		"iss": issuerURL,
		"aud": audience,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	}

	tokenStr := signToken(t, kid, privKey, claims)

	result, err := validator.Validate(tokenStr)
	if err != nil {
		t.Fatalf("expected valid token without act claim, got error: %v", err)
	}

	if result.UserIdentifier != "user-123" {
		t.Errorf("expected UserIdentifier=user-123, got %q", result.UserIdentifier)
	}
	if result.ActorIdentifier != "" {
		t.Errorf("expected empty ActorIdentifier, got %q", result.ActorIdentifier)
	}
}


