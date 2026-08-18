package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/SAP/astonish/pkg/store"
)

func TestResolveAgentCardURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "base URL without trailing slash",
			input:    "https://example.com",
			expected: "https://example.com/.well-known/agent-card.json",
		},
		{
			name:     "base URL with trailing slash",
			input:    "https://example.com/",
			expected: "https://example.com/.well-known/agent-card.json",
		},
		{
			name:     "base URL with path",
			input:    "https://example.com/agents/myagent",
			expected: "https://example.com/agents/myagent/.well-known/agent-card.json",
		},
		{
			name:     "base URL with path and trailing slash",
			input:    "https://example.com/agents/myagent/",
			expected: "https://example.com/agents/myagent/.well-known/agent-card.json",
		},
		{
			name:     "already contains well-known path",
			input:    "https://example.com/.well-known/agent-card.json",
			expected: "https://example.com/.well-known/agent-card.json",
		},
		{
			name:     "already contains well-known path with trailing slash",
			input:    "https://example.com/.well-known/agent-card.json/",
			expected: "https://example.com/.well-known/agent-card.json",
		},
		{
			name:     "well-known path under a subpath",
			input:    "https://example.com/v1/.well-known/agent-card.json",
			expected: "https://example.com/v1/.well-known/agent-card.json",
		},
		{
			name:     "multiple trailing slashes",
			input:    "https://example.com///",
			expected: "https://example.com/.well-known/agent-card.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAgentCardURL(tt.input)
			if got != tt.expected {
				t.Errorf("resolveAgentCardURL(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// --- Mock A2A agent store for handler tests ---

type mockA2AAgentStore struct {
	agents map[string]*store.A2AAgent
}

func (m *mockA2AAgentStore) List(_ context.Context) ([]store.A2AAgent, error) {
	var result []store.A2AAgent
	for _, a := range m.agents {
		result = append(result, *a)
	}
	return result, nil
}

func (m *mockA2AAgentStore) Get(_ context.Context, name string) (*store.A2AAgent, error) {
	if a, ok := m.agents[name]; ok {
		return a, nil
	}
	return nil, nil
}

func (m *mockA2AAgentStore) Save(_ context.Context, _ *store.A2AAgent) error { return nil }
func (m *mockA2AAgentStore) Delete(_ context.Context, _ string) error        { return nil }
func (m *mockA2AAgentStore) UpdateCachedCard(_ context.Context, _ string, _ json.RawMessage, _ json.RawMessage) error {
	return nil
}

// --- Credential store that resolves bearer tokens ---

type bearerCredentialStore struct {
	mockCredentialStore
	creds map[string]*store.Credential
}

func (b *bearerCredentialStore) Get(_ context.Context, name string) *store.Credential {
	if b.creds == nil {
		return nil
	}
	return b.creds[name]
}

func (b *bearerCredentialStore) Resolve(_ context.Context, name string) (string, string, error) {
	cred := b.creds[name]
	if cred == nil {
		return "", "", nil
	}
	return store.ResolveCredentialHeader(name, cred, nil)
}

// --- Handler tests ---

func TestTestA2AAgentHandler_ResolvesCredential(t *testing.T) {
	// Mock HTTP server that requires Authorization: Bearer test-token
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":        "test-agent",
			"description": "A test agent",
			"version":     "1.0",
			"skills":      []any{map[string]string{"name": "echo"}},
		})
	}))
	defer agentServer.Close()

	// Set up the A2A agent store with an agent pointing to our mock server
	agentStore := &mockA2AAgentStore{
		agents: map[string]*store.A2AAgent{
			"test-agent": {
				Name:           "test-agent",
				URL:            agentServer.URL,
				CredentialName: "my-bearer-cred",
				AuthType:       "bearer",
			},
		},
	}

	// Set up credential store that resolves "my-bearer-cred" to Bearer test-token
	credStore := &bearerCredentialStore{
		creds: map[string]*store.Credential{
			"my-bearer-cred": {Type: store.CredBearer, Token: "test-token"},
		},
	}

	// Build request with services context
	r := httptest.NewRequest("POST", "/api/a2a-agents/test-agent/test", nil)
	r = mux.SetURLVars(r, map[string]string{"name": "test-agent"})

	svc := &store.Services{
		Mode:          store.ModePlatform,
		TeamA2AAgents: agentStore,
		Credentials:   credStore,
	}
	r = r.WithContext(store.WithServices(r.Context(), svc))

	w := httptest.NewRecorder()
	TestA2AAgentHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v (full response: %s)", resp["status"], w.Body.String())
	}
	if resp["agent_name"] != "test-agent" {
		t.Errorf("expected agent_name=test-agent, got %v", resp["agent_name"])
	}
}

func TestTestA2AAgentHandler_NoCredentialStillWorks(t *testing.T) {
	// Mock HTTP server that does NOT require auth
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":        "open-agent",
			"description": "An open agent",
			"version":     "2.0",
			"skills":      []any{},
		})
	}))
	defer agentServer.Close()

	// Agent without CredentialName
	agentStore := &mockA2AAgentStore{
		agents: map[string]*store.A2AAgent{
			"open-agent": {
				Name: "open-agent",
				URL:  agentServer.URL,
			},
		},
	}

	// Build request — no credential store needed
	r := httptest.NewRequest("POST", "/api/a2a-agents/open-agent/test", nil)
	r = mux.SetURLVars(r, map[string]string{"name": "open-agent"})

	svc := &store.Services{
		Mode:          store.ModePlatform,
		TeamA2AAgents: agentStore,
	}
	r = r.WithContext(store.WithServices(r.Context(), svc))

	w := httptest.NewRecorder()
	TestA2AAgentHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v (full response: %s)", resp["status"], w.Body.String())
	}
	if resp["agent_name"] != "open-agent" {
		t.Errorf("expected agent_name=open-agent, got %v", resp["agent_name"])
	}
}

func TestRefreshA2AAgentHandler_ResolvesCredential(t *testing.T) {
	// Mock HTTP server that requires Authorization: Bearer refresh-token
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer refresh-token" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":        "secure-agent",
			"description": "A secured agent",
			"version":     "1.0",
			"skills": []any{
				map[string]string{"name": "skill-a"},
				map[string]string{"name": "skill-b"},
			},
		})
	}))
	defer agentServer.Close()

	agentStore := &mockA2AAgentStore{
		agents: map[string]*store.A2AAgent{
			"secure-agent": {
				Name:           "secure-agent",
				URL:            agentServer.URL,
				CredentialName: "refresh-cred",
				AuthType:       "bearer",
			},
		},
	}

	credStore := &bearerCredentialStore{
		creds: map[string]*store.Credential{
			"refresh-cred": {Type: store.CredBearer, Token: "refresh-token"},
		},
	}

	// RefreshA2AAgentHandler uses resolveA2AStoreForWrite which requires admin auth.
	// Use scope=platform and inject a superadmin PlatformUser.
	r := httptest.NewRequest("POST", "/api/a2a-agents/secure-agent/refresh?scope=platform", nil)
	r = mux.SetURLVars(r, map[string]string{"name": "secure-agent"})

	svc := &store.Services{
		Mode:              store.ModePlatform,
		PlatformA2AAgents: agentStore,
		Credentials:       credStore,
	}
	ctx := store.WithServices(r.Context(), svc)
	// Inject superadmin user for RequirePlatformAdmin check
	ctx = WithPlatformUser(ctx, &PlatformUser{
		ID:           "admin-user",
		Email:        "admin@example.com",
		PlatformRole: "superadmin",
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	RefreshA2AAgentHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v (full response: %s)", resp["status"], w.Body.String())
	}
	if resp["name"] != "secure-agent" {
		t.Errorf("expected name=secure-agent, got %v", resp["name"])
	}
	// The mock returns 2 skills
	if skillCount, ok := resp["skill_count"].(float64); !ok || skillCount != 2 {
		t.Errorf("expected skill_count=2, got %v", resp["skill_count"])
	}
}
