package oauthserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/SAP/astonish/pkg/store"
)

type memoryStore struct {
	mu      sync.Mutex
	clients map[string]*store.OAuthClient
	codes   map[string]store.OAuthAuthorization
	tokens  []store.OAuthToken
	keys    []store.OAuthSigningKey
}

func (m *memoryStore) CreateOAuthClient(_ context.Context, client store.OAuthClient) error {
	m.clients[client.ClientID] = &client
	return nil
}
func (m *memoryStore) GetOAuthClient(_ context.Context, id string) (*store.OAuthClient, error) {
	return m.clients[id], nil
}
func (m *memoryStore) ListOAuthClients(_ context.Context) ([]store.OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	clients := make([]store.OAuthClient, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, *client)
	}
	return clients, nil
}
func (m *memoryStore) ListOAuthClientsForOwner(_ context.Context, ownerID string) ([]store.OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	clients := make([]store.OAuthClient, 0)
	for _, client := range m.clients {
		if client.OwnerUserID == ownerID {
			clients = append(clients, *client)
		}
	}
	return clients, nil
}
func (m *memoryStore) UpdateOAuthClient(_ context.Context, updated store.OAuthClient) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.clients[updated.ClientID]
	if current == nil {
		return nil
	}
	if updated.SecretHash == "" {
		updated.SecretHash = current.SecretHash
	}
	m.clients[updated.ClientID] = &updated
	return nil
}
func (m *memoryStore) DeleteOAuthClient(_ context.Context, clientID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clients, clientID)
	return nil
}
func (m *memoryStore) SaveOAuthAuthorization(_ context.Context, auth store.OAuthAuthorization) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[auth.CodeHash] = auth
	return nil
}
func (m *memoryStore) ConsumeOAuthAuthorization(_ context.Context, key string, now time.Time) (*store.OAuthAuthorization, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	auth, ok := m.codes[key]
	if !ok || !auth.ConsumedAt.IsZero() || !auth.ExpiresAt.After(now) {
		return nil, nil
	}
	auth.ConsumedAt = now
	m.codes[key] = auth
	return &auth, nil
}
func (m *memoryStore) SaveOAuthToken(_ context.Context, token store.OAuthToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens = append(m.tokens, token)
	return nil
}
func (m *memoryStore) GetOAuthRefreshToken(_ context.Context, handleHash string) (*store.OAuthToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tokens {
		if m.tokens[i].HandleHash == handleHash {
			token := m.tokens[i]
			return &token, nil
		}
	}
	return nil, nil
}
func (m *memoryStore) ConsumeOAuthRefreshToken(_ context.Context, handleHash string, now time.Time) (*store.OAuthToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tokens {
		token := &m.tokens[i]
		if token.HandleHash != handleHash || !token.RevokedAt.IsZero() || !token.ExpiresAt.After(now) {
			continue
		}
		token.RevokedAt = now
		out := *token
		return &out, nil
	}
	return nil, nil
}
func (m *memoryStore) RevokeOAuthTokenFamily(context.Context, string, time.Time) error { return nil }
func (m *memoryStore) SaveOAuthConsent(context.Context, store.OAuthConsent) error      { return nil }
func (m *memoryStore) RevokeOAuthConsent(context.Context, string, string, string, time.Time) error {
	return nil
}
func (m *memoryStore) SaveOAuthSigningKey(_ context.Context, key store.OAuthSigningKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys = append(m.keys, key)
	return nil
}
func (m *memoryStore) ListOAuthSigningKeys(_ context.Context) ([]store.OAuthSigningKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.OAuthSigningKey(nil), m.keys...), nil
}

func TestOAuthAuthorizationCodePKCEAndAudience(t *testing.T) {
	secret, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryStore{clients: map[string]*store.OAuthClient{"client": {ClientID: "client", ClientType: "confidential", SecretHash: string(secret), Active: true, OrgID: "org", TeamID: "team", RedirectURIs: []string{"https://client.example/callback"}, GrantTypes: []string{GrantAuthorizationCode}, Scopes: []string{"tool:execute", "offline_access"}, Resources: []string{"https://api.example"}}}, codes: map[string]store.OAuthAuthorization{}}
	server, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, func(context.Context, *http.Request) (Subject, error) {
		return Subject{ID: "user", OrgID: "org", TeamID: "team"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	verifier := "a-long-pkce-verifier"
	authorize := httptest.NewRequest(http.MethodGet, "/oauth/authorize?response_type=code&client_id=client&redirect_uri=https%3A%2F%2Fclient.example%2Fcallback&code_challenge_method=S256&code_challenge="+url.QueryEscape(pkceS256(verifier))+"&scope=tool%3Aexecute%20offline_access&resource=https%3A%2F%2Fapi.example&state=state", nil)
	result := httptest.NewRecorder()
	server.Handler().ServeHTTP(result, authorize)
	if result.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, body=%s", result.Code, result.Body.String())
	}
	code := mustCode(t, result.Header().Get("Location"))

	form := url.Values{"grant_type": {GrantAuthorizationCode}, "code": {code}, "redirect_uri": {"https://client.example/callback"}, "code_verifier": {verifier}}
	token := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	token.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	token.SetBasicAuth("client", "secret")
	result = httptest.NewRecorder()
	server.Handler().ServeHTTP(result, token)
	if result.Code != http.StatusOK {
		t.Fatalf("token status = %d, body=%s", result.Code, result.Body.String())
	}
	var response struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(result.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.RefreshToken == "" || len(backend.tokens) != 1 {
		t.Fatalf("expected persisted refresh token")
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(response.AccessToken, claims, func(token *jwt.Token) (any, error) { return &server.signing.key.PublicKey, nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("parse access token: %v", err)
	}
	if claims["sub"] != "user" || claims["org_id"] != "org" || claims["team_id"] != "team" {
		t.Fatalf("identity claims = %#v", claims)
	}
	if audience, ok := claims["aud"].([]any); !ok || len(audience) != 1 || audience[0] != "https://api.example" {
		t.Fatalf("audience = %#v", claims["aud"])
	}
}

func TestOAuthRejectsMissingPKCEAndWrongRedirect(t *testing.T) {
	backend := &memoryStore{clients: map[string]*store.OAuthClient{"client": {ClientID: "client", ClientType: "public", Active: true, OrgID: "org", RedirectURIs: []string{"https://client.example/callback"}, GrantTypes: []string{GrantAuthorizationCode}}}, codes: map[string]store.OAuthAuthorization{}}
	server, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, func(context.Context, *http.Request) (Subject, error) { return Subject{ID: "user", OrgID: "org"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/oauth/authorize?response_type=code&client_id=client&redirect_uri=https%3A%2F%2Fclient.example%2Fcallback",
		"/oauth/authorize?response_type=code&client_id=client&redirect_uri=https%3A%2F%2Fevil.example%2Fcallback&code_challenge_method=S256&code_challenge=test",
	} {
		result := httptest.NewRecorder()
		server.Handler().ServeHTTP(result, httptest.NewRequest(http.MethodGet, path, nil))
		if result.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d", path, result.Code)
		}
	}
}

func mustCode(t *testing.T, location string) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Fatalf("missing code in %q", location)
	}
	return code
}
