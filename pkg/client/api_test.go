package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPingUsesStudioSessionsEndpoint(t *testing.T) {
	var gotPath, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		if r.URL.Path != "/api/studio/sessions" {
			http.Error(w, "legacy auth endpoint does not accept OAuth bearer", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	client := NewWithConfig(server.URL)
	client.cached = &Tokens{AccessToken: "oauth-access-token", Kind: TokenKindOAuth}

	if err := client.Ping(); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if gotPath != "/api/studio/sessions" {
		t.Errorf("Ping() path = %q, want %q", gotPath, "/api/studio/sessions")
	}
	if gotAuthorization != "Bearer oauth-access-token" {
		t.Errorf("authorization = %q", gotAuthorization)
	}
}

func TestPingReportsUnauthorizedStudioSessionRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/studio/sessions" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewWithConfig(server.URL)
	if err := client.Ping(); err == nil || err.Error() != "not authenticated" {
		t.Fatalf("Ping() error = %v, want not authenticated", err)
	}
}
