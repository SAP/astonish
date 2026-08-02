package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// --- Mock MCPServerStore ---

type mockMCPServerStore struct {
	servers map[string]*store.MCPServer
	deleted []string
}

func newMockMCPStore(servers ...*store.MCPServer) *mockMCPServerStore {
	m := &mockMCPServerStore{servers: make(map[string]*store.MCPServer)}
	for _, s := range servers {
		m.servers[s.Name] = s
	}
	return m
}

func (m *mockMCPServerStore) List(_ context.Context) ([]store.MCPServer, error) {
	var result []store.MCPServer
	for _, s := range m.servers {
		result = append(result, *s)
	}
	return result, nil
}

func (m *mockMCPServerStore) Get(_ context.Context, name string) (*store.MCPServer, error) {
	s, ok := m.servers[name]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (m *mockMCPServerStore) Save(_ context.Context, server *store.MCPServer) error {
	m.servers[server.Name] = server
	return nil
}

func (m *mockMCPServerStore) Delete(_ context.Context, name string) error {
	delete(m.servers, name)
	m.deleted = append(m.deleted, name)
	return nil
}

func (m *mockMCPServerStore) UpdateCachedTools(_ context.Context, name string, tools json.RawMessage) error {
	if s, ok := m.servers[name]; ok {
		s.CachedTools = tools
	}
	return nil
}

// --- Mock platformSecrets ---

type mockPlatformSecrets struct {
	secrets map[string]string
	removed []string
}

func newMockPlatformSecrets() *mockPlatformSecrets {
	return &mockPlatformSecrets{secrets: make(map[string]string)}
}

func (m *mockPlatformSecrets) GetSecret(key string) string {
	return m.secrets[key]
}

func (m *mockPlatformSecrets) SetSecret(key, value string) error {
	m.secrets[key] = value
	return nil
}

func (m *mockPlatformSecrets) RemoveSecret(key string) error {
	delete(m.secrets, key)
	m.removed = append(m.removed, key)
	return nil
}

// --- Helper: create request with platform services context ---

func platformRequest(t *testing.T, method, url string, mcpStore store.MCPServerStore) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, url, nil)
	svc := &store.Services{
		Mode:       store.ModePlatform,
		MCPServers: mcpStore, // org-level (default scope)
	}
	ctx := store.WithServices(r.Context(), svc)
	return r.WithContext(ctx)
}

func platformRequestWithScopes(t *testing.T, method, url string, org, team, platform store.MCPServerStore) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, url, nil)
	svc := &store.Services{
		Mode:               store.ModePlatform,
		MCPServers:         org,
		TeamMCPServers:     team,
		PlatformMCPServers: platform,
	}
	ctx := store.WithServices(r.Context(), svc)
	return r.WithContext(ctx)
}

// =============================================================================
// MCPStatusHandler tests
// =============================================================================

func TestMCPStatusHandler_HealthyWhenToolsExist(t *testing.T) {
	tools := json.RawMessage(`[{"name":"resolve","description":"Resolves a library ID"}]`)
	mcpStore := newMockMCPStore(&store.MCPServer{
		Name:        "context7",
		Command:     "npx",
		Transport:   "stdio",
		CachedTools: tools,
	})

	r := platformRequest(t, "GET", "/api/mcp/status", mcpStore)
	rr := httptest.NewRecorder()
	MCPStatusHandler(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp MCPStatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(resp.Servers))
	}
	if resp.Servers[0].Status != "healthy" {
		t.Errorf("expected status 'healthy', got %q", resp.Servers[0].Status)
	}
	if resp.Servers[0].ToolCount != 1 {
		t.Errorf("expected tool_count 1, got %d", resp.Servers[0].ToolCount)
	}
}

func TestMCPStatusHandler_ConfiguredWhenNoTools(t *testing.T) {
	mcpStore := newMockMCPStore(&store.MCPServer{
		Name:      "context7",
		Command:   "npx",
		Transport: "stdio",
		// No CachedTools
	})

	r := platformRequest(t, "GET", "/api/mcp/status", mcpStore)
	rr := httptest.NewRecorder()
	MCPStatusHandler(rr, r)

	var resp MCPStatusResponse
	json.NewDecoder(rr.Body).Decode(&resp)

	if len(resp.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(resp.Servers))
	}
	if resp.Servers[0].Status != "configured" {
		t.Errorf("expected status 'configured', got %q", resp.Servers[0].Status)
	}
}

func TestMCPStatusHandler_Disabled(t *testing.T) {
	disabled := false
	mcpStore := newMockMCPStore(&store.MCPServer{
		Name:        "context7",
		Command:     "npx",
		Transport:   "stdio",
		Enabled:     &disabled,
		CachedTools: json.RawMessage(`[{"name":"foo"}]`),
	})

	r := platformRequest(t, "GET", "/api/mcp/status", mcpStore)
	rr := httptest.NewRecorder()
	MCPStatusHandler(rr, r)

	var resp MCPStatusResponse
	json.NewDecoder(rr.Body).Decode(&resp)

	if len(resp.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(resp.Servers))
	}
	if resp.Servers[0].Status != "disabled" {
		t.Errorf("expected status 'disabled', got %q", resp.Servers[0].Status)
	}
}

func TestMCPStatusHandler_EmptyWithoutPlatformContext(t *testing.T) {
	// Without platform context, should return empty list (not error)
	r := httptest.NewRequest("GET", "/api/mcp/status", nil)
	rr := httptest.NewRecorder()
	MCPStatusHandler(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp struct {
		Servers []cache.ServerStatus `json:"servers"`
	}
	json.NewDecoder(rr.Body).Decode(&resp)
	if len(resp.Servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(resp.Servers))
	}
}

// =============================================================================
// mcpManagerForRequest scope tests
// =============================================================================

func TestMCPManagerForRequest_PlatformScope(t *testing.T) {
	platformStore := newMockMCPStore(&store.MCPServer{
		Name:    "context7",
		Command: "npx",
		Args:    []string{"-y", "@upstash/context7-mcp"},
	})
	orgStore := newMockMCPStore()  // empty
	teamStore := newMockMCPStore() // empty

	r := platformRequestWithScopes(t, "GET", "/api/mcp/context7/tools?scope=platform", orgStore, teamStore, platformStore)

	mgr, cfg, err := mcpManagerForRequest(r, "context7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Command != "npx" {
		t.Fatalf("expected config command npx, got %q", cfg.Command)
	}
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	mgr.Cleanup()
}

func TestMCPManagerForRequest_TeamScope(t *testing.T) {
	teamStore := newMockMCPStore(&store.MCPServer{
		Name:    "team-server",
		Command: "node",
	})
	orgStore := newMockMCPStore()
	platformStore := newMockMCPStore()

	r := platformRequestWithScopes(t, "GET", "/api/mcp/team-server/tools?scope=team", orgStore, teamStore, platformStore)

	mgr, cfg, err := mcpManagerForRequest(r, "team-server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Command != "node" {
		t.Fatalf("expected config command node, got %q", cfg.Command)
	}
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	mgr.Cleanup()
}

func TestMCPManagerForRequest_OrgDefault(t *testing.T) {
	orgStore := newMockMCPStore(&store.MCPServer{
		Name:    "org-server",
		Command: "python",
	})
	teamStore := newMockMCPStore()
	platformStore := newMockMCPStore()

	// No scope param → defaults to org
	r := platformRequestWithScopes(t, "GET", "/api/mcp/org-server/tools", orgStore, teamStore, platformStore)

	mgr, cfg, err := mcpManagerForRequest(r, "org-server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Command != "python" {
		t.Fatalf("expected config command python, got %q", cfg.Command)
	}
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	mgr.Cleanup()
}

func TestMCPManagerForRequest_NotFoundInScope(t *testing.T) {
	// Server exists in platform store, but request uses org scope (no scope param)
	platformStore := newMockMCPStore(&store.MCPServer{
		Name:    "context7",
		Command: "npx",
	})
	orgStore := newMockMCPStore() // empty — server NOT here

	r := platformRequestWithScopes(t, "GET", "/api/mcp/context7/tools", orgStore, newMockMCPStore(), platformStore)

	_, _, err := mcpManagerForRequest(r, "context7")
	if err == nil {
		t.Fatal("expected error when server not in the scoped store")
	}
	want := "server 'context7' not found in config"
	if err.Error() != want {
		t.Errorf("got error %q, want %q", err.Error(), want)
	}
}

func TestMCPManagerForRequest_NoStore(t *testing.T) {
	// No platform context at all
	r := httptest.NewRequest("GET", "/api/mcp/context7/tools", nil)
	r = mux.SetURLVars(r, map[string]string{"serverName": "context7"})

	_, _, err := mcpManagerForRequest(r, "context7")
	if err == nil {
		t.Fatal("expected error when no store available")
	}
	want := "MCP server store not available"
	if err.Error() != want {
		t.Errorf("got error %q, want %q", err.Error(), want)
	}
}

// =============================================================================
// UninstallStandardServerHandler tests
// =============================================================================

func TestUninstallStandardServer_RemovesFromBothStores(t *testing.T) {
	// Setup mock platform secrets
	secrets := newMockPlatformSecrets()
	secrets.secrets["web_servers.tavily.api_key"] = "tvly-abc123"

	// Install mocks
	prev := platformSecretsInstance
	platformSecretsInstance = secrets
	defer func() { platformSecretsInstance = prev }()

	// Setup mock MCP store with tavily
	mcpStore := newMockMCPStore(&store.MCPServer{
		Name:    "tavily",
		Command: "npx",
	})

	// Create request with platform context and ?scope=platform
	r := httptest.NewRequest("DELETE", "/api/standard-servers/tavily?scope=platform", nil)
	svc := &store.Services{
		Mode:               store.ModePlatform,
		PlatformMCPServers: mcpStore,
	}
	ctx := store.WithServices(r.Context(), svc)
	r = r.WithContext(ctx)
	r = mux.SetURLVars(r, map[string]string{"id": "tavily"})

	rr := httptest.NewRecorder()
	UninstallStandardServerHandler(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify: secret removed from platform secrets
	if _, exists := secrets.secrets["web_servers.tavily.api_key"]; exists {
		t.Error("expected secret to be removed from platform secrets")
	}
	if len(secrets.removed) == 0 || secrets.removed[0] != "web_servers.tavily.api_key" {
		t.Errorf("expected RemoveSecret called with 'web_servers.tavily.api_key', got %v", secrets.removed)
	}

	// Verify: server removed from MCP store
	if len(mcpStore.deleted) == 0 || mcpStore.deleted[0] != "tavily" {
		t.Errorf("expected Delete called with 'tavily', got %v", mcpStore.deleted)
	}
}

func TestUninstallStandardServer_NoStore_Returns503(t *testing.T) {
	// No platform context → effectiveMCPStore returns nil
	r := httptest.NewRequest("DELETE", "/api/standard-servers/tavily", nil)
	r = mux.SetURLVars(r, map[string]string{"id": "tavily"})

	rr := httptest.NewRecorder()
	UninstallStandardServerHandler(rr, r)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUninstallStandardServer_UnknownServer_Returns404(t *testing.T) {
	r := httptest.NewRequest("DELETE", "/api/standard-servers/nonexistent", nil)
	r = mux.SetURLVars(r, map[string]string{"id": "nonexistent"})

	rr := httptest.NewRecorder()
	UninstallStandardServerHandler(rr, r)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}
