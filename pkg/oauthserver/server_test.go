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
	mu          sync.Mutex
	clients     map[string]*store.OAuthClient
	codes       map[string]store.OAuthAuthorization
	tokens      []store.OAuthToken
	signingKeys []store.OAuthSigningKey
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
func (m *memoryStore) ListOAuthClientsForOwner(_ context.Context, ownerUserID string) ([]store.OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	clients := make([]store.OAuthClient, 0)
	for _, client := range m.clients {
		if client.OwnerUserID == ownerUserID {
			clients = append(clients, *client)
		}
	}
	return clients, nil
}
func (m *memoryStore) UpdateOAuthClient(_ context.Context, client store.OAuthClient) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.clients[client.ClientID]; !ok {
		return nil
	}
	m.clients[client.ClientID] = &client
	return nil
}
func (m *memoryStore) DeleteOAuthClient(_ context.Context, clientID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clients, clientID)
	for i := range m.tokens {
		if m.tokens[i].ClientID == clientID && m.tokens[i].RevokedAt.IsZero() {
			m.tokens[i].RevokedAt = time.Now().UTC()
		}
	}
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
func (m *memoryStore) GetOAuthRefreshToken(_ context.Context, handle string) (*store.OAuthToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tokens {
		if m.tokens[i].TokenType == "refresh" && m.tokens[i].HandleHash == handle {
			copy := m.tokens[i]
			return &copy, nil
		}
	}
	return nil, nil
}
func (m *memoryStore) ConsumeOAuthRefreshToken(_ context.Context, handle string, now time.Time) (*store.OAuthToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tokens {
		token := &m.tokens[i]
		if token.TokenType != "refresh" || token.HandleHash != handle {
			continue
		}
		if !token.RevokedAt.IsZero() || !token.ExpiresAt.After(now) {
			for j := range m.tokens {
				if m.tokens[j].FamilyID == token.FamilyID && m.tokens[j].RevokedAt.IsZero() {
					m.tokens[j].RevokedAt, m.tokens[j].ReplayDetected = now, true
				}
			}
			copy := *token
			copy.ReplayDetected = true
			return &copy, nil
		}
		token.RevokedAt = now
		copy := *token
		return &copy, nil
	}
	return nil, nil
}
func (m *memoryStore) RevokeOAuthTokenFamily(_ context.Context, family string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tokens {
		if m.tokens[i].FamilyID == family {
			m.tokens[i].RevokedAt = now
		}
	}
	return nil
}
func (m *memoryStore) SaveOAuthConsent(context.Context, store.OAuthConsent) error { return nil }
func (m *memoryStore) RevokeOAuthConsent(context.Context, string, string, string, time.Time) error {
	return nil
}
func (m *memoryStore) SaveOAuthSigningKey(_ context.Context, key store.OAuthSigningKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signingKeys = append(m.signingKeys, key)
	return nil
}
func (m *memoryStore) ListOAuthSigningKeys(_ context.Context) ([]store.OAuthSigningKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.OAuthSigningKey(nil), m.signingKeys...), nil
}

func TestOAuthSigningKeyPersistsAcrossServerRestart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	backend := &memoryStore{clients: map[string]*store.OAuthClient{}, codes: map[string]store.OAuthAuthorization{}}
	validate := func(context.Context, *http.Request) (Subject, error) { return Subject{}, nil }
	first, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, validate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, validate)
	if err != nil {
		t.Fatal(err)
	}
	if len(backend.signingKeys) != 1 || first.signing.id != second.signing.id {
		t.Fatalf("signing key was not reused: keys=%d first=%q second=%q", len(backend.signingKeys), first.signing.id, second.signing.id)
	}
	if len(backend.signingKeys[0].EncryptedPrivateKey) == 0 || string(backend.signingKeys[0].EncryptedPrivateKey) == "RSA PRIVATE KEY" {
		t.Fatal("private signing key was not encrypted before persistence")
	}
}

func TestOAuthAuthorizationCodePKCEAndAudience(t *testing.T) {
	secret, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryStore{clients: map[string]*store.OAuthClient{"client": {ClientID: "client", ClientType: "confidential", SecretHash: string(secret), Active: true, OrgID: "org", TeamID: "assigned-team", RedirectURIs: []string{"https://client.example/callback"}, GrantTypes: []string{GrantAuthorizationCode}, Scopes: []string{"tool:execute", "offline_access"}, Resources: []string{"https://api.example"}}}, codes: map[string]store.OAuthAuthorization{}}
	server, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, func(context.Context, *http.Request) (Subject, error) {
		return Subject{ID: "user", OrgID: "org"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	verifier := "a-long-pkce-verifier"
	authorize := httptest.NewRequest(http.MethodGet, "/oauth/authorize?response_type=code&client_id=client&redirect_uri=https%3A%2F%2Fclient.example%2Fcallback&code_challenge_method=S256&code_challenge="+url.QueryEscape(pkceS256(verifier))+"&scope=tool%3Aexecute+offline_access&resource=https%3A%2F%2Fapi.example&state=state", nil)
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
	initialRefresh := response.RefreshToken
	refreshForm := url.Values{"grant_type": {GrantRefreshToken}, "refresh_token": {initialRefresh}}
	refreshRequest := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(refreshForm.Encode()))
	refreshRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	refreshRequest.SetBasicAuth("client", "secret")
	result = httptest.NewRecorder()
	server.Handler().ServeHTTP(result, refreshRequest)
	if result.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body=%s", result.Code, result.Body.String())
	}
	var rotated struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(result.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken == "" || rotated.RefreshToken == initialRefresh || len(backend.tokens) != 2 || backend.tokens[0].FamilyID != backend.tokens[1].FamilyID {
		t.Fatalf("refresh token did not rotate in one family: %#v", backend.tokens)
	}
	// Reuse of the original handle is replay evidence. It must fail and revoke
	// the newly issued descendant in the same family.
	replay := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(refreshForm.Encode()))
	replay.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replay.SetBasicAuth("client", "secret")
	result = httptest.NewRecorder()
	server.Handler().ServeHTTP(result, replay)
	if result.Code != http.StatusBadRequest || backend.tokens[1].RevokedAt.IsZero() || !backend.tokens[1].ReplayDetected {
		t.Fatalf("refresh replay was not denied and family-revoked: status=%d tokens=%#v", result.Code, backend.tokens)
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(response.AccessToken, claims, func(token *jwt.Token) (any, error) { return &server.signing.key.PublicKey, nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("parse access token: %v", err)
	}
	if claims["sub"] != "user" || claims["org_id"] != "org" || claims["team_id"] != "assigned-team" {
		t.Fatalf("identity claims = %#v", claims)
	}
	if len(backend.codes) != 1 {
		t.Fatalf("authorization code count = %d, want 1", len(backend.codes))
	}
	for _, authorization := range backend.codes {
		if authorization.TeamID != "assigned-team" {
			t.Fatalf("authorization team = %q, want client-assigned team", authorization.TeamID)
		}
	}
	if audience, ok := claims["aud"].([]any); !ok || len(audience) != 1 || audience[0] != "https://api.example" {
		t.Fatalf("audience = %#v", claims["aud"])
	}
}

func TestOAuthRevocationAndIntrospection(t *testing.T) {
	secret, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryStore{clients: map[string]*store.OAuthClient{"client": {ClientID: "client", ClientType: "confidential", SecretHash: string(secret), Active: true}}, codes: map[string]store.OAuthAuthorization{}, tokens: []store.OAuthToken{{HandleHash: hash("refresh"), FamilyID: "family", TokenType: "refresh", ClientID: "client", Subject: "user", OrgID: "org", Scopes: []string{"tool:execute"}, CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}}}
	server, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, func(context.Context, *http.Request) (Subject, error) { return Subject{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		form := url.Values{"token": {"refresh"}}
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.SetBasicAuth("client", "secret")
		out := httptest.NewRecorder()
		server.Handler().ServeHTTP(out, r)
		return out
	}
	result := request("/oauth/introspect")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"active":true`) {
		t.Fatalf("active introspection = %d %s", result.Code, result.Body.String())
	}
	result = request("/oauth/revoke")
	if result.Code != http.StatusOK || backend.tokens[0].RevokedAt.IsZero() {
		t.Fatalf("revocation = %d %#v", result.Code, backend.tokens[0])
	}
	result = request("/oauth/introspect")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"active":false`) {
		t.Fatalf("revoked introspection = %d %s", result.Code, result.Body.String())
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
