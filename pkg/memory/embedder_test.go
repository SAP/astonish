package memory

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAIEmbeddingTimeoutIsExplicitAndNotRetried(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	embed := newOpenAIEmbeddingFunc(server.URL, "key", "model", 20*time.Millisecond)
	_, err := embed(context.Background(), "query")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "embedding request timed out after 20ms") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout does not wrap context deadline: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}

func TestOllamaEmbeddingTimeoutIsExplicitAndNotRetried(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	embed := newOllamaEmbeddingFunc(server.URL, "model", 20*time.Millisecond)
	_, err := embed(context.Background(), "query")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "ollama embedding request timed out after 20ms") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout does not wrap context deadline: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}
