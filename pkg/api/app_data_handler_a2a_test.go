package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveA2ASource_EmptyAgentName(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/apps/data", nil)

	_, err := resolveA2ASource(r, "", map[string]any{"message": "hello"})
	if err == nil {
		t.Fatal("expected error for empty agent name")
	}
	if !strings.Contains(err.Error(), "agent name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveA2ASource_MissingMessage(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/apps/data", nil)

	_, err := resolveA2ASource(r, "my-agent", map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing message")
	}
	if !strings.Contains(err.Error(), "'message' argument is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveA2ASource_EmptyMessage(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/apps/data", nil)

	_, err := resolveA2ASource(r, "my-agent", map[string]any{"message": ""})
	if err == nil {
		t.Fatal("expected error for empty message")
	}
	if !strings.Contains(err.Error(), "'message' argument is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveA2ASource_NoAgentsConfigured(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/apps/data", nil)

	_, err := resolveA2ASource(r, "my-agent", map[string]any{"message": "hello"})
	if err == nil {
		t.Fatal("expected error when no A2A agents are configured")
	}
	// In personal mode with no config file, this should fail with "no A2A agents configured"
	// or "failed to load config" depending on the environment.
	if !strings.Contains(err.Error(), "A2A") {
		t.Fatalf("expected A2A-related error, got: %v", err)
	}
}

func TestResolveDataSource_A2APrefix(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/apps/data", nil)

	// Test that the a2a: prefix routes correctly (will fail with config error,
	// but confirms routing works)
	_, err := resolveDataSource(r, "a2a:test-agent", map[string]any{"message": "hi"}, "")
	if err == nil {
		t.Fatal("expected error (no A2A config in test env)")
	}
	// Should NOT get "unknown source format" — that would mean routing failed
	if strings.Contains(err.Error(), "unknown source format") {
		t.Fatalf("a2a: prefix was not routed correctly, got: %v", err)
	}
	// Should get an A2A-related error (config not found, etc.)
	if !strings.Contains(err.Error(), "A2A") {
		t.Fatalf("expected A2A-related error from routing, got: %v", err)
	}
}

func TestResolveDataSource_A2APrefixNoMessage(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/apps/data", nil)

	_, err := resolveDataSource(r, "a2a:test-agent", map[string]any{}, "")
	if err == nil {
		t.Fatal("expected error for missing message")
	}
	if !strings.Contains(err.Error(), "'message' argument is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}
