package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
)

func TestHttpRequest_NodeDynamicCredentialRedaction(t *testing.T) {
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()
	const token = "fresh-node-token-123456789"
	authCalls, apiCalls := 0, 0
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, token)
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing refreshed token")
		}
		w.Header().Set("X-Echo", token)
		fmt.Fprint(w, token)
	}))
	defer api.Close()
	cs := sessioncreds.NewStore(map[string]*store.Credential{"dynamic": {Type: store.CredOAuthClientCreds, AuthURL: auth.URL, TokenURL: auth.URL, ClientID: "client", ClientSecret: "node-client-secret-123456"}})
	ctx := store.WithCredentialStore(context.Background(), cs)
	for _, args := range []HttpRequestArgs{
		{URL: api.URL, Credential: "dynamic"},
		{URL: api.URL, Headers: map[string]string{"Authorization": "Bearer {{CREDENTIAL:dynamic:token}}"}},
	} {
		result, err := httpRequest(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), token) || !strings.Contains(result.Body, "[REDACTED]") {
			t.Fatalf("token escaped node: %s", raw)
		}
	}
	if authCalls == 0 || apiCalls != 2 {
		t.Fatalf("auth=%d api=%d", authCalls, apiCalls)
	}
}

func TestHttpRequest_CrossOriginRedirectDoesNotLeak(t *testing.T) {
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()
	calls := 0
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer other.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := httpRequest(context.Background(), HttpRequestArgs{URL: source.URL, Method: "POST", Body: "secret-request-body", Headers: map[string]string{"X-API-Key": "secret-request-header"}})
	if err == nil || !strings.Contains(err.Error(), "cross-origin") || calls != 0 {
		t.Fatalf("redirect not blocked: %v calls=%d", err, calls)
	}
}

func TestHttpRequest_DialRejectsPrivateAndCancellation(t *testing.T) {
	for _, address := range []string{"127.0.0.1:80", "[::1]:80", "10.0.0.1:80", "169.254.169.254:80"} {
		conn, err := httpRequestPublicDial(context.Background(), "tcp", address)
		if conn != nil {
			conn.Close()
			t.Fatal("connected to blocked destination")
		}
		if err == nil || !strings.Contains(err.Error(), "private/loopback") {
			t.Fatalf("%s: %v", address, err)
		}
	}
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := httpRequest(ctx, HttpRequestArgs{URL: srv.URL})
	if err == nil || calls != 0 {
		t.Fatalf("canceled request dispatched: %v calls=%d", err, calls)
	}
}
