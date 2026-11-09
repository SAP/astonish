package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfiguredSearchSecureTransport(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer scoped-test-token" {
			t.Error("wrong search request")
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"Public answer"}}],"citations":["https://example.org/source"],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	}))
	defer server.Close()
	b := ConfiguredSearchBackend{Provider: "configured", Model: "sonar", Endpoint: server.URL + "/chat/completions", Credential: "vault-search"}
	origin, _ := RoutineReadOrigin(b.Endpoint)
	p := RoutineReadPolicy{Authorize: func(_ context.Context, s string) error {
		if s != origin {
			return fmt.Errorf("wrong origin")
		}
		return nil
	}, AllowIP: func(_ context.Context, address string, ip net.IP) error {
		if !ip.IsLoopback() {
			return fmt.Errorf("test loopback only")
		}
		return nil
	}}
	result, err := RunConfiguredSearch(context.Background(), "public weather", b, "scoped-test-token", p)
	if err != nil || result.Answer != "Public answer" || len(result.Citations) != 1 || result.Metadata["usage_metadata"] == nil || calls != 1 {
		t.Fatalf("%+v %v calls %d", result, err, calls)
	}
	for _, test := range []struct {
		name, token string
		p           RoutineReadPolicy
	}{
		{"missing credential", "", p},
		{"denied origin", "scoped-test-token", RoutineReadPolicy{Authorize: func(context.Context, string) error { return fmt.Errorf("denied") }, AllowIP: p.AllowIP}},
		{"private default denied", "scoped-test-token", RoutineReadPolicy{Authorize: p.Authorize}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RunConfiguredSearch(context.Background(), "public weather", b, test.token, test.p); err == nil {
				t.Fatal("accepted")
			}
			if calls != 1 {
				t.Fatal("denial after effect")
			}
		})
	}
}
func TestConfiguredSearchRedirectNeverDiscloses(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	b := ConfiguredSearchBackend{Provider: "configured", Model: "sonar", Endpoint: redirect.URL, Credential: "search"}
	p := RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }}
	if _, err := RunConfiguredSearch(context.Background(), "public", b, "test", p); err == nil || hits != 0 {
		t.Fatalf("redirect leaked: %v %d", err, hits)
	}
}
