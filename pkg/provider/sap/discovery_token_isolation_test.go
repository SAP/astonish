package sap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Provider-local reuse must not become a cross-instance credential cache, even
// when configurations match or only the secret rotates. Existing instances keep
// their own authority after a different provider is constructed.
func TestProviderDiscoveryTokenIsolation(t *testing.T) {
	var exchanges, discoveries, inferences atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/token":
			n := exchanges.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			secret := "old-secret"
			if n == 3 {
				secret = "rotated-secret"
			}
			if r.Form.Get("client_id") != "client" || r.Form.Get("client_secret") != secret {
				t.Error("wrong instance credentials")
			}
			fmt.Fprintf(w, `{"access_token":"instance-%d","expires_in":3600}`, n)
		case "/v2/lm/deployments":
			n := discoveries.Add(1)
			if r.Header.Get("Authorization") != fmt.Sprintf("Bearer instance-%d", n) {
				t.Error("discovery reused another instance's token")
			}
			fmt.Fprint(w, `{"resources":[{"id":"deployment","status":"RUNNING","details":{"resources":{"backendDetails":{"model":{"name":"gpt-4o"}}}}}]}`)
		case "/v2/inference/deployments/deployment/chat/completions":
			n := inferences.Add(1)
			// Invoke all three after constructing them, then invoke the first again.
			if n == 4 {
				n = 1
			}
			if r.Header.Get("Authorization") != fmt.Sprintf("Bearer instance-%d", n) {
				t.Error("inference lost provider-local discovery token")
			}
			fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var providers []model.LLM
	for _, secret := range []string{"old-secret", "old-secret", "rotated-secret"} {
		llm, err := NewProviderWithConfig(context.Background(), "gpt-4o", "client", secret, server.URL, server.URL, "default")
		if err != nil {
			t.Fatal(err)
		}
		providers = append(providers, llm)
	}
	providers = append(providers, providers[0])
	for _, llm := range providers {
		for _, err := range llm.GenerateContent(context.Background(), &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}}, false) {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if exchanges.Load() != 3 || discoveries.Load() != 3 || inferences.Load() != 4 {
		t.Fatalf("exchanges=%d discoveries=%d inferences=%d", exchanges.Load(), discoveries.Load(), inferences.Load())
	}
}
