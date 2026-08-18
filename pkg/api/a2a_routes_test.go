package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/channels"
	a2achan "github.com/SAP/astonish/pkg/channels/a2a"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
)

// testRSAKey is a shared RSA key for test JWT signing.
var testRSAKey *rsa.PrivateKey

func init() {
	var err error
	testRSAKey, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("failed to generate test RSA key: " + err.Error())
	}
}

// newTestJWKSServer creates an httptest.Server serving a JWKS with the test RSA public key.
func newTestJWKSServer(t *testing.T) *httptest.Server {
	t.Helper()

	nBytes := testRSAKey.PublicKey.N.Bytes()
	eBytes := big.NewInt(int64(testRSAKey.PublicKey.E)).Bytes()

	jwks := map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "RSA",
				"kid": "test-key-1",
				"n":   base64.RawURLEncoding.EncodeToString(nBytes),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			},
		},
	}

	data, err := json.Marshal(jwks)
	if err != nil {
		t.Fatalf("failed to marshal JWKS: %v", err)
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
}

// signTestJWT creates a signed JWT for testing with the given claims.
func signTestJWT(t *testing.T, issuer, subject, audience string, expiresAt time.Time, actorSub string) string {
	t.Helper()

	claims := jwt.MapClaims{
		"iss": issuer,
		"sub": subject,
		"aud": audience,
		"exp": jwt.NewNumericDate(expiresAt),
		"iat": jwt.NewNumericDate(time.Now()),
	}
	if actorSub != "" {
		claims["act"] = map[string]any{"sub": actorSub}
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key-1"

	signed, err := token.SignedString(testRSAKey)
	if err != nil {
		t.Fatalf("failed to sign test JWT: %v", err)
	}
	return signed
}

// setupA2ATestWithJWT sets up the A2A channel and token validator for tests.
func setupA2ATestWithJWT(t *testing.T) *a2achan.A2AChannel {
	t.Helper()
	store := a2a.NewInMemoryTaskStore(1 * time.Hour)
	t.Cleanup(store.Close)
	reg := a2a.NewInMemoryAgentRegistry()

	ch := a2achan.New(&a2achan.Config{
		TaskStore:     store,
		AgentRegistry: reg,
		BaseURL:       "http://localhost:9393",
	}, nil)

	// Set the global channel
	SetA2AChannel(ch)
	t.Cleanup(func() { SetA2AChannel(nil) })

	// Set up a token validator with a test JWKS server
	jwksServer := newTestJWKSServer(t)
	t.Cleanup(jwksServer.Close)

	validator := a2a.NewTokenValidator(a2a.TokenValidatorConfig{
		Issuers: []a2a.TrustedIssuer{
			{
				ID:        "test-issuer-id",
				Name:      "Test Issuer",
				Issuer:    "https://idp.example.com",
				JWKSURL:   jwksServer.URL,
				Audience:  "astonish-a2a",
				UserClaim: "sub",
				OrgID:     "org-1",
			},
		},
		Agents: []a2a.AllowedAgent{
			{
				ID:       "agent-1",
				Name:     "Test Service Agent",
				ActorSub: "service-account-1",
				IssuerID: "test-issuer-id",
				OrgID:    "org-1",
				Enabled:  true,
			},
		},
		RequireActorClaim: false,
	})

	SetA2ATokenValidator(validator)
	t.Cleanup(func() { SetA2ATokenValidator(nil) })

	return ch
}

func TestA2AAgentCardHandler(t *testing.T) {
	_ = setupA2ATestWithJWT(t)

	req := httptest.NewRequest("GET", "/.well-known/agent-card.json", nil)
	w := httptest.NewRecorder()

	AgentCardHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var card a2a.AgentCard
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatalf("failed to parse agent card: %v", err)
	}

	if card.Name != "Astonish" {
		t.Fatalf("expected name 'Astonish', got %q", card.Name)
	}
	if card.URL != "http://localhost:9393/api/a2a" {
		t.Fatalf("expected URL with /api/a2a, got %q", card.URL)
	}
}

func TestA2AAgentCardHandler_NotConfigured(t *testing.T) {
	SetA2AChannel(nil)

	req := httptest.NewRequest("GET", "/.well-known/agent-card.json", nil)
	w := httptest.NewRecorder()

	AgentCardHandler(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestA2AHandler_MessageSend_ValidJWT(t *testing.T) {
	ch := setupA2ATestWithJWT(t)

	// Start the channel with a handler that responds
	_ = ch.Start(context.Background(), func(ctx context.Context, msg channels.InboundMessage) error {
		return nil
	})

	// Build JSON-RPC request
	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: "Hello"}},
		},
		Configuration: &a2a.TaskConfig{
			ReturnImmediately: true,
		},
	}
	paramsJSON, _ := json.Marshal(params)
	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
		Params:  paramsJSON,
	}
	body, _ := json.Marshal(rpcReq)

	// Create router with auth middleware
	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	// Sign a valid JWT
	token := signTestJWT(t, "https://idp.example.com", "user@example.com", "astonish-a2a", time.Now().Add(time.Hour), "")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestA2AHandler_MessageSend_WithActorClaim(t *testing.T) {
	ch := setupA2ATestWithJWT(t)

	_ = ch.Start(context.Background(), func(ctx context.Context, msg channels.InboundMessage) error {
		return nil
	})

	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: "Hello from service"}},
		},
		Configuration: &a2a.TaskConfig{
			ReturnImmediately: true,
		},
	}
	paramsJSON, _ := json.Marshal(params)
	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
		Params:  paramsJSON,
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	// Sign a JWT with actor claim (delegation)
	token := signTestJWT(t, "https://idp.example.com", "user@example.com", "astonish-a2a", time.Now().Add(time.Hour), "service-account-1")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestA2AHandler_ExpiredToken(t *testing.T) {
	_ = setupA2ATestWithJWT(t)

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	// Sign an expired JWT
	token := signTestJWT(t, "https://idp.example.com", "user@example.com", "astonish-a2a", time.Now().Add(-time.Hour), "")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for expired token")
	}
	if resp.Error.Code != a2a.ErrCodeAuthRequired {
		t.Fatalf("expected error code %d (auth required), got %d", a2a.ErrCodeAuthRequired, resp.Error.Code)
	}
}

func TestA2AHandler_WrongAudience(t *testing.T) {
	_ = setupA2ATestWithJWT(t)

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	// Sign a JWT with wrong audience
	token := signTestJWT(t, "https://idp.example.com", "user@example.com", "wrong-audience", time.Now().Add(time.Hour), "")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for wrong audience")
	}
	if resp.Error.Code != a2a.ErrCodeForbidden {
		t.Fatalf("expected error code %d (forbidden), got %d", a2a.ErrCodeForbidden, resp.Error.Code)
	}
}

func TestA2AHandler_UnknownIssuer(t *testing.T) {
	_ = setupA2ATestWithJWT(t)

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	// Sign a JWT with unknown issuer
	token := signTestJWT(t, "https://unknown-idp.example.com", "user@example.com", "astonish-a2a", time.Now().Add(time.Hour), "")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown issuer")
	}
	if resp.Error.Code != a2a.ErrCodeForbidden {
		t.Fatalf("expected error code %d (forbidden), got %d", a2a.ErrCodeForbidden, resp.Error.Code)
	}
}

func TestA2AHandler_UnauthorizedActor(t *testing.T) {
	_ = setupA2ATestWithJWT(t)

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	// Sign a JWT with an actor that's not in the allowed agents list
	token := signTestJWT(t, "https://idp.example.com", "user@example.com", "astonish-a2a", time.Now().Add(time.Hour), "unauthorized-service")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for unauthorized actor")
	}
	if resp.Error.Code != a2a.ErrCodeForbidden {
		t.Fatalf("expected error code %d (forbidden), got %d", a2a.ErrCodeForbidden, resp.Error.Code)
	}
}

func TestA2AHandler_NoAuth(t *testing.T) {
	_ = setupA2ATestWithJWT(t)

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	// No auth header
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for missing auth")
	}
	if resp.Error.Code != a2a.ErrCodeAuthRequired {
		t.Fatalf("expected error code %d, got %d", a2a.ErrCodeAuthRequired, resp.Error.Code)
	}
}

func TestA2AHandler_UnknownMethod(t *testing.T) {
	_ = setupA2ATestWithJWT(t)

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "unknown/method",
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	// Valid JWT
	token := signTestJWT(t, "https://idp.example.com", "user@example.com", "astonish-a2a", time.Now().Add(time.Hour), "")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != a2a.ErrCodeMethodNotFound {
		t.Fatalf("expected error code %d, got %d", a2a.ErrCodeMethodNotFound, resp.Error.Code)
	}
}

func TestA2AHandler_ValidatorNotConfigured(t *testing.T) {
	// Set up channel but no validator
	store := a2a.NewInMemoryTaskStore(1 * time.Hour)
	t.Cleanup(store.Close)
	reg := a2a.NewInMemoryAgentRegistry()

	ch := a2achan.New(&a2achan.Config{
		TaskStore:     store,
		AgentRegistry: reg,
		BaseURL:       "http://localhost:9393",
	}, nil)

	SetA2AChannel(ch)
	t.Cleanup(func() { SetA2AChannel(nil) })
	SetA2ATokenValidator(nil)

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer some-token")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error when validator not configured")
	}
	if resp.Error.Code != a2a.ErrCodeInternal {
		t.Fatalf("expected error code %d (internal), got %d", a2a.ErrCodeInternal, resp.Error.Code)
	}
}

func TestA2AAuthExemptPaths(t *testing.T) {
	tests := []struct {
		path   string
		exempt bool
	}{
		{"/.well-known/agent-card.json", true},
		{"/api/a2a", true},
		{"/api/a2a/stream", true},
		{"/api/admin/a2a/agents", false},
	}
	for _, tt := range tests {
		got := isAuthExemptPath(tt.path)
		if got != tt.exempt {
			t.Errorf("isAuthExemptPath(%q) = %v, want %v", tt.path, got, tt.exempt)
		}
	}
}
