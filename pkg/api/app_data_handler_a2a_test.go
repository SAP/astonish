package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
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

// TestResolveA2ASource_PlatformStoresInjected verifies that when the request
// has platform-mode Services with A2A agent stores, the stores are propagated
// into the context so LoadA2AAgentConfig can find agents from the database.
// This is the fix for the "no A2A agents configured" error in app data requests.
func TestResolveA2ASource_PlatformStoresInjected(t *testing.T) {
	agentStore := &mockA2AAgentStore{
		agents: map[string]*store.A2AAgent{
			"sci-autonomous-operation": {
				Name: "sci-autonomous-operation",
				URL:  "http://localhost:9999/.well-known/agent.json",
			},
		},
	}

	r := httptest.NewRequest(http.MethodPost, "/api/apps/data", nil)
	svc := &store.Services{
		Mode:          store.ModePlatform,
		TeamA2AAgents: agentStore,
	}
	r = r.WithContext(store.WithServices(r.Context(), svc))

	// This will fail at the FetchAgentCard step (no real server), but it should
	// NOT fail with "no A2A agents configured" — that error means the stores
	// were not properly injected into the context.
	_, err := resolveA2ASource(r, "sci-autonomous-operation", map[string]any{"message": "hello"})
	if err == nil {
		t.Fatal("expected error (no real A2A server), but should not be 'no agents configured'")
	}
	// The error should be about connecting to the agent, NOT about missing config
	if strings.Contains(err.Error(), "no A2A agents configured") {
		t.Fatalf("stores were not injected into context: %v", err)
	}
	// Should find the agent (not "agent not found")
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("agent was not found despite being in store: %v", err)
	}
	// Expected: error about fetching agent card (network error to localhost:9999)
	if !strings.Contains(err.Error(), "failed to fetch agent card") {
		t.Fatalf("unexpected error (expected agent card fetch failure): %v", err)
	}
}
