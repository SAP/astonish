package oauthserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"golang.org/x/crypto/bcrypt"
)

func TestOAuthScopeRequestsMustBeExplicitlyGranted(t *testing.T) {
	secret, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryStore{clients: map[string]*store.OAuthClient{"client": {ClientID: "client", ClientType: "confidential", SecretHash: string(secret), Active: true, OwnerUserID: "owner", OrgID: "org", TeamID: "team", Scopes: []string{ScopeToolExecute, ScopeChat}, Resources: []string{"https://api.example"}}}, codes: map[string]store.OAuthAuthorization{}}
	server, err := New(Config{Issuer: "https://issuer.example", Resource: "https://api.example"}, backend, func(context.Context, *http.Request) (Subject, error) { return Subject{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	request := func(scope string) *httptest.ResponseRecorder {
		form := url.Values{"grant_type": {GrantClientCredentials}, "scope": {scope}}
		r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.SetBasicAuth("client", "secret")
		out := httptest.NewRecorder()
		server.Handler().ServeHTTP(out, r)
		return out
	}
	if result := request(ScopeToolExecute + " " + ScopeChat); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"scope":"tool:execute chat"`) {
		t.Fatalf("combined permitted scopes = %d %s", result.Code, result.Body.String())
	}
	if result := request(ScopeToolExecute + " unknown"); result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), `"error":"invalid_scope"`) {
		t.Fatalf("mixed invalid scopes = %d %s", result.Code, result.Body.String())
	}
}
