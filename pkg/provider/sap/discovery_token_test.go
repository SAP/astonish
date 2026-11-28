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

// Discovery already obtained a valid token. A second exchange before the first
// inference adds an unnecessary dependency and can fail an otherwise ready run.
func TestProviderReusesDiscoveryTokenForInference(t *testing.T) {
	var exchanges atomic.Int32
	var inferences atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/token":
			if exchanges.Add(1) != 1 {
				http.Error(w, "redundant exchange", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"access_token":"discovery-token","expires_in":3600}`)
		case "/v2/lm/deployments":
			fmt.Fprint(w, `{"resources":[{"id":"deployment","status":"RUNNING","details":{"resources":{"backendDetails":{"model":{"name":"gpt-4o"}}}}}]}`)
		case "/v2/inference/deployments/deployment/chat/completions":
			if r.Header.Get("Authorization") != "Bearer discovery-token" {
				t.Error("inference lost discovery token")
			}
			inferences.Add(1)
			fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	llm, err := NewProviderWithConfig(context.Background(), "gpt-4o", "id", "secret", server.URL, server.URL, "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range llm.GenerateContent(context.Background(), &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}}, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if exchanges.Load() != 1 || inferences.Load() != 1 {
		t.Fatalf("exchanges=%d inference=%d", exchanges.Load(), inferences.Load())
	}
}
