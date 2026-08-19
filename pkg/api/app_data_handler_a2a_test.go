package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/a2aclient"
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

func TestResolveA2ASource_AdvertisedInterfaceRouting(t *testing.T) {
	var discoveryRequests atomic.Int32
	var rpcRequests atomic.Int32

	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/invoke" {
			t.Errorf("RPC request = %s %s, want POST /invoke", r.Method, r.URL.Path)
		}
		var req a2a.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode RPC request: %v", err)
		}
		if req.Method != "SendMessage" {
			t.Errorf("RPC method = %q, want SendMessage", req.Method)
		}
		_ = json.NewEncoder(w).Encode(a2a.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{"task": map[string]any{
				"id": "task-1",
				"status": map[string]any{
					"state": "TASK_STATE_COMPLETED",
					"message": map[string]any{
						"role":  "ROLE_AGENT",
						"parts": []map[string]any{{"text": "routed"}},
					},
				},
			}},
		})
	}))
	defer rpcServer.Close()

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		discoveryRequests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/discovery/.well-known/agent-card.json" {
			t.Errorf("discovery request = %s %s, want GET /discovery/.well-known/agent-card.json", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(a2a.AgentCard{
			Name: "routed-agent",
			SupportedInterfaces: []a2a.AgentInterface{
				{URL: rpcServer.URL + "/ignored", ProtocolBinding: "GRPC", ProtocolVersion: "1.0"},
				{URL: rpcServer.URL + "/invoke", ProtocolBinding: "JSONRPC", ProtocolVersion: "1.0"},
			},
		})
	}))
	defer discoveryServer.Close()

	r := requestWithA2AAgent(t, "routed-agent", discoveryServer.URL+"/discovery")
	result, err := resolveA2ASource(r, "routed-agent", map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("resolveA2ASource failed: %v", err)
	}
	data, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", result)
	}
	if data["response"] != "routed" {
		t.Errorf("response = %v, want routed", data["response"])
	}
	if discoveryRequests.Load() != 1 || rpcRequests.Load() != 1 {
		t.Errorf("requests = discovery:%d rpc:%d, want 1 each", discoveryRequests.Load(), rpcRequests.Load())
	}
}

func TestResolveA2ASource_IncompatibleAdvertisedInterfaces(t *testing.T) {
	var rpcRequests atomic.Int32
	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcRequests.Add(1)
		http.Error(w, "unexpected RPC", http.StatusInternalServerError)
	}))
	defer rpcServer.Close()

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(a2a.AgentCard{
			Name: "incompatible-agent",
			URL:  rpcServer.URL,
			SupportedInterfaces: []a2a.AgentInterface{
				{URL: rpcServer.URL, ProtocolBinding: "GRPC", ProtocolVersion: "1.0"},
				{URL: rpcServer.URL, ProtocolBinding: "HTTP+JSON", ProtocolVersion: "1.0"},
			},
		})
	}))
	defer discoveryServer.Close()

	r := requestWithA2AAgent(t, "incompatible-agent", discoveryServer.URL)
	_, err := resolveA2ASource(r, "incompatible-agent", map[string]any{"message": "hello"})
	if err == nil {
		t.Fatal("expected incompatible interface error")
	}
	if !strings.Contains(err.Error(), "incompatible agent card") || !strings.Contains(err.Error(), "no compatible agent interface") {
		t.Fatalf("unexpected error: %v", err)
	}
	if rpcRequests.Load() != 0 {
		t.Errorf("RPC requests = %d, want 0", rpcRequests.Load())
	}
}

func requestWithA2AAgent(t *testing.T, name, url string) *http.Request {
	t.Helper()
	agentStore := &mockA2AAgentStore{agents: map[string]*store.A2AAgent{
		name: {Name: name, URL: url},
	}}
	r := httptest.NewRequest(http.MethodPost, "/api/apps/data", nil)
	return r.WithContext(store.WithServices(r.Context(), &store.Services{
		Mode:          store.ModePlatform,
		TeamA2AAgents: agentStore,
	}))
}

func TestNormalizeAgentName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"sci-autonomous-operation", "sci-autonomous-operation"},
		{"SCI Autonomous Operation", "sci-autonomous-operation"},
		{"SCI_Autonomous_Operation", "sci-autonomous-operation"},
		{"sci autonomous operation", "sci-autonomous-operation"},
		{"My--Agent", "my-agent"},
		{"  leading spaces  ", "leading-spaces"},
		{"UPPER", "upper"},
		{"already-kebab", "already-kebab"},
		{"mixed_Hyphens-And Spaces", "mixed-hyphens-and-spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeAgentName(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeAgentName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestFindAgentByNormalizedName(t *testing.T) {
	agents := map[string]a2aclient.A2AAgentConfig{
		"SCI Autonomous Operation": {Name: "SCI Autonomous Operation", URL: "http://example.com"},
		"my-other-agent":           {Name: "my-other-agent", URL: "http://other.com"},
	}

	tests := []struct {
		query    string
		wantOK   bool
		wantName string
	}{
		{"sci-autonomous-operation", true, "SCI Autonomous Operation"},
		{"SCI Autonomous Operation", true, "SCI Autonomous Operation"},
		{"sci_autonomous_operation", true, "SCI Autonomous Operation"},
		{"my-other-agent", true, "my-other-agent"},
		{"My Other Agent", true, "my-other-agent"},
		{"nonexistent", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			cfg, ok := findAgentByNormalizedName(agents, tt.query)
			if ok != tt.wantOK {
				t.Fatalf("findAgentByNormalizedName(%q) ok=%v, want %v", tt.query, ok, tt.wantOK)
			}
			if ok && cfg.Name != tt.wantName {
				t.Errorf("findAgentByNormalizedName(%q).Name = %q, want %q", tt.query, cfg.Name, tt.wantName)
			}
		})
	}
}
