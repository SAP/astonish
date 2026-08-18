package a2a

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
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
				ActorSub:  "service-abc",
				IssuerID:  "issuer-1",
				OrgID:     "org-1",
				RateLimit: 100,
				MaxTasks:  5,
				Enabled:   true,
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
	if result.RateLimit != 100 {
		t.Errorf("expected RateLimit=100, got %d", result.RateLimit)
	}
	if result.MaxTasks != 5 {
		t.Errorf("expected MaxTasks=5, got %d", result.MaxTasks)
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

// =============================================================================
// Security Attack Vector Tests
// =============================================================================

func TestTokenValidator_AlgNone(t *testing.T) {
	// Attack: craft a token with alg:none and no signature.
	// The validator must reject this.
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

	// Manually construct a token with alg:none
	headerJSON := `{"alg":"none","typ":"JWT","kid":"` + kid + `"}`
	claimsJSON := fmt.Sprintf(`{"iss":"%s","aud":"%s","sub":"user-123","exp":%d}`, issuerURL, audience, time.Now().Add(time.Hour).Unix())
	header := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))
	payload := base64.RawURLEncoding.EncodeToString([]byte(claimsJSON))
	tokenStr := header + "." + payload + "."

	_, err = validator.Validate(tokenStr)
	if err == nil {
		t.Fatal("expected error for alg:none token, got nil")
	}
	t.Logf("alg:none correctly rejected: %v", err)
}

func TestTokenValidator_HS256Confusion(t *testing.T) {
	// Attack: sign a token with HS256 using the RSA public key bytes as the HMAC secret.
	// This is the classic algorithm confusion attack.
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

	// Marshal the RSA public key to use as HMAC secret
	pubKeyBytes, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal public key: %v", err)
	}

	// Create token signed with HS256 using the public key bytes as secret
	claims := jwt.MapClaims{
		"iss": issuerURL,
		"aud": audience,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = kid
	tokenStr, err := token.SignedString(pubKeyBytes)
	if err != nil {
		t.Fatalf("failed to sign HS256 token: %v", err)
	}

	_, err = validator.Validate(tokenStr)
	if err == nil {
		t.Fatal("expected error for HS256 confusion attack, got nil")
	}
	t.Logf("HS256 confusion correctly rejected: %v", err)
}

func TestTokenValidator_TamperedSignature(t *testing.T) {
	// Attack: create a valid RS256 token, then tamper with the signature.
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
		"exp": time.Now().Add(time.Hour).Unix(),
	}

	tokenStr := signToken(t, kid, privKey, claims)

	// Tamper with the signature by flipping a byte
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts in JWT, got %d", len(parts))
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("failed to decode signature: %v", err)
	}
	// Flip the first byte
	sigBytes[0] ^= 0xFF
	parts[2] = base64.RawURLEncoding.EncodeToString(sigBytes)
	tamperedToken := strings.Join(parts, ".")

	_, err = validator.Validate(tamperedToken)
	if err == nil {
		t.Fatal("expected error for tampered signature, got nil")
	}
	t.Logf("tampered signature correctly rejected: %v", err)
}

func TestTokenValidator_UnknownKID(t *testing.T) {
	// Attack: use a valid signing key but with a kid that doesn't exist in the JWKS.
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	kid := "known-key"
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

	// Generate a different key and sign with an unknown kid
	attackerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate attacker key: %v", err)
	}

	claims := jwt.MapClaims{
		"iss": issuerURL,
		"aud": audience,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	}

	// Sign with attacker's key using an unknown kid
	tokenStr := signToken(t, "unknown-kid-xyz", attackerKey, claims)

	_, err = validator.Validate(tokenStr)
	if err == nil {
		t.Fatal("expected error for unknown kid, got nil")
	}
	t.Logf("unknown kid correctly rejected: %v", err)
}

func TestTokenValidator_ES256ValidToken(t *testing.T) {
	// Happy path: validate a properly signed ES256 token.
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate EC key: %v", err)
	}
	kid := "ec-key-1"

	// Create a JWKS server with the EC public key
	xBytes := ecKey.PublicKey.X.Bytes()
	yBytes := ecKey.PublicKey.Y.Bytes()
	// Pad to 32 bytes for P-256
	padTo32 := func(b []byte) []byte {
		if len(b) >= 32 {
			return b
		}
		padded := make([]byte, 32)
		copy(padded[32-len(b):], b)
		return padded
	}

	jwks := map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "EC",
				"kid": kid,
				"use": "sig",
				"alg": "ES256",
				"crv": "P-256",
				"x":   base64.RawURLEncoding.EncodeToString(padTo32(xBytes)),
				"y":   base64.RawURLEncoding.EncodeToString(padTo32(yBytes)),
			},
		},
	}

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(jwksServer.Close)

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
		"sub": "ec-user-456",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}

	// Sign with ES256
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = kid
	tokenStr, err := token.SignedString(ecKey)
	if err != nil {
		t.Fatalf("failed to sign ES256 token: %v", err)
	}

	result, err := validator.Validate(tokenStr)
	if err != nil {
		t.Fatalf("expected valid ES256 token, got error: %v", err)
	}

	if result.UserIdentifier != "ec-user-456" {
		t.Errorf("expected UserIdentifier=ec-user-456, got %q", result.UserIdentifier)
	}
	if result.Issuer != issuerURL {
		t.Errorf("expected Issuer=%q, got %q", issuerURL, result.Issuer)
	}
	if result.IssuerID != "issuer-1" {
		t.Errorf("expected IssuerID=issuer-1, got %q", result.IssuerID)
	}
	t.Logf("ES256 token validated successfully: user=%s", result.UserIdentifier)
}


