package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// drainTurn runs a single GenerateContent turn to completion, returning any
// error observed while ranging the response iterator.
func drainTurn(t *testing.T, llm model.LLM) error {
	t.Helper()
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "hello"}}},
		},
	}
	var lastErr error
	for _, err := range llm.GenerateContent(context.Background(), req, false) {
		if err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// chatCompletionJSON is a minimal valid non-streaming ChatCompletion response.
const chatCompletionJSON = `{
  "id": "cmpl-test",
  "object": "chat.completion",
  "created": 0,
  "model": "test-model",
  "choices": [
    {"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}
  ],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`

// TestOpenCodeSessionHeaderStableAcrossTurns proves that every request to the
// opencode endpoint carries a non-empty x-opencode-session header and that the
// value is identical across two separate conversation turns — the fix for the
// missing_session_id rejection.
func TestOpenCodeSessionHeaderStableAcrossTurns(t *testing.T) {
	var mu sync.Mutex
	var captured []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		captured = append(captured, r.Header.Get("x-opencode-session"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionJSON))
	}))
	defer server.Close()

	llm := newProviderWithBaseURL("k", server.URL, "test-model")

	if err := drainTurn(t, llm); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if err := drainTurn(t, llm); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("expected 2 captured requests, got %d: %v", len(captured), captured)
	}
	for i, got := range captured {
		if got == "" {
			t.Fatalf("request %d had empty x-opencode-session header", i)
		}
		if !strings.HasPrefix(got, "ses_") {
			t.Errorf("request %d session header = %q, want ses_ prefix", i, got)
		}
	}
	if captured[0] != captured[1] {
		t.Fatalf("session header changed across turns: %q != %q", captured[0], captured[1])
	}
}

// TestOpenCodeSetsUserAgent verifies the provider identifies itself with the
// descriptive user agent opencode go asks clients to send.
func TestOpenCodeSetsUserAgent(t *testing.T) {
	var mu sync.Mutex
	var gotUA string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		gotUA = r.Header.Get("User-Agent")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionJSON))
	}))
	defer server.Close()

	llm := newProviderWithBaseURL("k", server.URL, "test-model")
	if err := drainTurn(t, llm); err != nil {
		t.Fatalf("turn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotUA != userAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, userAgent)
	}
}

// TestNewSessionIDFormat sanity-checks the generated session identifier format.
func TestNewSessionIDFormat(t *testing.T) {
	id := newSessionID()
	if !strings.HasPrefix(id, "ses_") {
		t.Fatalf("session id = %q, want ses_ prefix", id)
	}
	if id == newSessionID() {
		t.Error("two generated session ids should differ")
	}
}
