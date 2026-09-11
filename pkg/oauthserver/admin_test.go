package oauthserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestDiscovery_UsesOAuthJSONFieldNames(t *testing.T) {
	server, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, &memoryStore{clients: map[string]*store.OAuthClient{}, codes: map[string]store.OAuthAuthorization{}}, func(context.Context, *http.Request) (Subject, error) { return Subject{}, nil })
	if err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(server.Discovery())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"issuer":                 "https://issuer.example",
		"resource":               "https://api.example",
		"authorization_endpoint": "https://issuer.example/oauth/authorize",
		"token_endpoint":         "https://issuer.example/oauth/token",
		"jwks_uri":               "https://issuer.example/oauth/jwks",
		"revocation_endpoint":    "https://issuer.example/oauth/revoke",
		"introspection_endpoint": "https://issuer.example/oauth/introspect",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discovery JSON = %#v, want %#v", got, want)
	}
}

func TestClientAdministration_CreateRotateAndDisable(t *testing.T) {
	backend := &memoryStore{clients: map[string]*store.OAuthClient{}, codes: map[string]store.OAuthAuthorization{}}
	server, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, func(context.Context, *http.Request) (Subject, error) { return Subject{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	input := ClientInput{Name: "MCP test", ClientType: "confidential", OwnerUserID: "owner-123", OrgID: "org-123", TeamID: "team-123", RedirectURIs: []string{"https://client.example/callback"}, GrantTypes: []string{GrantAuthorizationCode}, Resources: []string{"https://api.example"}, Scopes: []string{"tool:execute"}, Active: true}
	client, secret, err := server.CreateClient(context.Background(), input)
	if err != nil || client.ClientID == "" || secret == "" {
		t.Fatalf("create client = %#v, secret=%q, err=%v", client, secret, err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(client.SecretHash), []byte(secret)); err != nil {
		t.Fatalf("returned secret does not match persisted verifier: %v", err)
	}
	client, rotated, err := server.UpdateClient(context.Background(), client.ClientID, input, true)
	if err != nil || rotated == "" || rotated == secret || bcrypt.CompareHashAndPassword([]byte(client.SecretHash), []byte(secret)) == nil {
		t.Fatalf("secret rotation failed: client=%#v secret=%q rotated=%q err=%v", client, secret, rotated, err)
	}
	input.Active = false
	client, noSecret, err := server.UpdateClient(context.Background(), client.ClientID, input, false)
	if err != nil || client.Active || noSecret != "" {
		t.Fatalf("disable client = %#v, secret=%q, err=%v", client, noSecret, err)
	}
	clients, err := server.ListClients(context.Background())
	if err != nil || len(clients) != 1 || clients[0].SecretHash == "" {
		t.Fatalf("list clients = %#v, err=%v", clients, err)
	}
	if err := server.DeleteClient(context.Background(), client.ClientID); err != nil {
		t.Fatalf("delete client: %v", err)
	}
	clients, err = server.ListClients(context.Background())
	if err != nil || len(clients) != 0 {
		t.Fatalf("list after delete = %#v, err=%v", clients, err)
	}
	if _, _, err := server.CreateClient(context.Background(), ClientInput{Name: "invalid public", ClientType: "public", OwnerUserID: "owner-123", OrgID: "org-123", TeamID: "team-123", GrantTypes: []string{GrantAuthorizationCode}, Resources: []string{"https://api.example"}, Active: true}); err == nil {
		t.Fatal("public client without redirect URI was accepted")
	}
}

func TestClientCredentials_RetainsClientTenantContext(t *testing.T) {
	backend := &memoryStore{clients: map[string]*store.OAuthClient{}, codes: map[string]store.OAuthAuthorization{}}
	server, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, func(context.Context, *http.Request) (Subject, error) { return Subject{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	client, secret, err := server.CreateClient(context.Background(), ClientInput{Name: "service", ClientType: "confidential", OwnerUserID: "owner-123", OrgID: "org-123", TeamID: "team-123", GrantTypes: []string{GrantClientCredentials}, Resources: []string{"https://api.example"}, Scopes: []string{"tool:execute"}, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=client_credentials&scope=tool%3Aexecute"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(client.ClientID, secret)
	res := httptest.NewRecorder()
	server.token(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("token status = %d: %s", res.Code, res.Body.String())
	}
	var response struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	claims := jwt.MapClaims{}
	if _, _, err := new(jwt.Parser).ParseUnverified(response.AccessToken, claims); err != nil {
		t.Fatal(err)
	}
	if claims["org_id"] != "org-123" || claims["team_id"] != "team-123" {
		t.Fatalf("tenant claims = %#v", claims)
	}
}
