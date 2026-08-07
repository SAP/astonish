package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
)

// memMCPStore is a minimal in-memory store.MCPServerStore for tests.
type memMCPStore struct {
	servers map[string]store.MCPServer
}

func newMemMCPStore(servers ...store.MCPServer) *memMCPStore {
	m := &memMCPStore{servers: make(map[string]store.MCPServer)}
	for _, s := range servers {
		m.servers[s.Name] = s
	}
	return m
}

func (m *memMCPStore) List(_ context.Context) ([]store.MCPServer, error) {
	out := make([]store.MCPServer, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, s)
	}
	return out, nil
}

func (m *memMCPStore) Get(_ context.Context, name string) (*store.MCPServer, error) {
	if s, ok := m.servers[name]; ok {
		return &s, nil
	}
	return nil, nil
}

func (m *memMCPStore) Save(_ context.Context, s *store.MCPServer) error {
	m.servers[s.Name] = *s
	return nil
}

func (m *memMCPStore) Delete(_ context.Context, name string) error {
	delete(m.servers, name)
	return nil
}

func (m *memMCPStore) UpdateCachedTools(_ context.Context, name string, tools json.RawMessage) error {
	if s, ok := m.servers[name]; ok {
		s.CachedTools = tools
		m.servers[name] = s
	}
	return nil
}

// writeMCPConfigFile writes an mcp_config.json into a temp config dir that
// GetConfigDir() will resolve, and returns nothing (env is set on t).
func writeMCPConfigFile(t *testing.T, jsonBody string) {
	t.Helper()
	tmpDir := t.TempDir()
	var configDir string
	if runtime.GOOS == "darwin" {
		configDir = filepath.Join(tmpDir, "Library", "Application Support", "astonish")
		t.Setenv("HOME", tmpDir)
	} else {
		configDir = filepath.Join(tmpDir, "astonish")
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "mcp_config.json"), []byte(jsonBody), 0o644); err != nil {
		t.Fatalf("write mcp_config.json: %v", err)
	}
}

// TestLoadMCPConfigForRequest_FileBaseMergedInPlatformMode verifies that
// config-file (mcp_config.json) servers are included as the base layer in
// platform mode, and that a same-named DB (platform/org/team) server overrides
// the file entry by name — mirroring the provider config+DB merge.
func TestLoadMCPConfigForRequest_FileBaseMergedInPlatformMode(t *testing.T) {
	writeMCPConfigFile(t, `{
		"mcpServers": {
			"file-only":  {"command": "node", "args": ["file-only.js"]},
			"overridden": {"command": "node", "args": ["file-version.js"]}
		}
	}`)

	// DB has an org-level server that shares a name with a file server.
	orgStore := newMemMCPStore(store.MCPServer{
		Name:    "overridden",
		Command: "node",
		Args:    []string{"db-version.js"},
	})

	svc := &store.Services{
		Mode:      store.ModePlatform,
		MCPServers: orgStore,
	}

	req := httptest.NewRequest("GET", "/api/mcp/servers", nil)
	req = req.WithContext(store.WithServices(req.Context(), svc))

	cfg := loadMCPConfigForRequest(req)
	if cfg == nil {
		t.Fatal("loadMCPConfigForRequest returned nil")
	}

	// File-only server must be present (base layer).
	if _, ok := cfg.MCPServers["file-only"]; !ok {
		t.Errorf("expected file-only server to be present in platform-mode config, got: %v", cfg.MCPServers)
	}

	// DB entry must override the file entry by name.
	overridden, ok := cfg.MCPServers["overridden"]
	if !ok {
		t.Fatalf("expected overridden server present, got: %v", cfg.MCPServers)
	}
	if len(overridden.Args) != 1 || overridden.Args[0] != "db-version.js" {
		t.Errorf("expected DB entry to override file entry by name, got args: %v", overridden.Args)
	}
}

// TestGetMCPSettingsHandler_IncludesFileServers verifies the Studio settings
// endpoint (GET /api/settings/mcp) surfaces config-file servers as defaults,
// with DB entries overriding same-named file entries.
func TestGetMCPSettingsHandler_IncludesFileServers(t *testing.T) {
	writeMCPConfigFile(t, `{
		"mcpServers": {
			"file-only":  {"command": "node", "args": ["file-only.js"]},
			"overridden": {"command": "node", "args": ["file-version.js"]}
		}
	}`)

	orgStore := newMemMCPStore(store.MCPServer{
		Name:    "overridden",
		Command: "node",
		Args:    []string{"db-version.js"},
	})
	svc := &store.Services{Mode: store.ModePlatform, MCPServers: orgStore}

	req := httptest.NewRequest("GET", "/api/settings/mcp", nil)
	req = req.WithContext(store.WithServices(req.Context(), svc))
	rec := httptest.NewRecorder()

	GetMCPSettingsHandler(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got.MCPServers["file-only"]; !ok {
		t.Errorf("expected file-only in settings config, got: %+v", got.MCPServers)
	}
	ov, ok := got.MCPServers["overridden"]
	if !ok {
		t.Fatalf("expected overridden present")
	}
	if len(ov.Args) != 1 || ov.Args[0] != "db-version.js" {
		t.Errorf("expected DB override, got args: %v", ov.Args)
	}
}

// TestMCPServerConfigMatchesFile verifies the unmodified-echo detection used to
// avoid persisting config-file servers as DB rows.
func TestMCPServerConfigMatchesFile(t *testing.T) {
	file := config.MCPServerConfig{Command: "node", Args: []string{"a.js"}, Transport: "stdio"}

	// Exact echo (transport defaulted to "" by the UI still matches "stdio").
	echo := config.MCPServerConfig{Command: "node", Args: []string{"a.js"}}
	if !mcpServerConfigMatchesFile(echo, file) {
		t.Errorf("expected unmodified echo to match file default")
	}

	// Changed args → a real override, must NOT match.
	changed := config.MCPServerConfig{Command: "node", Args: []string{"b.js"}}
	if mcpServerConfigMatchesFile(changed, file) {
		t.Errorf("expected changed args to be treated as an override")
	}
}

// surfaces config-file servers (Source="config") and that DB servers override
// same-named file entries.
func TestGetMCPServersHandler_ListsFileServers(t *testing.T) {
	writeMCPConfigFile(t, `{
		"mcpServers": {
			"file-only":  {"command": "node", "args": ["file-only.js"]},
			"overridden": {"command": "node", "args": ["file-version.js"]}
		}
	}`)

	orgStore := newMemMCPStore(store.MCPServer{
		Name:    "overridden",
		Command: "node",
		Args:    []string{"db-version.js"},
	})

	svc := &store.Services{
		Mode:       store.ModePlatform,
		MCPServers: orgStore,
	}

	req := httptest.NewRequest("GET", "/api/mcp/servers", nil)
	req = req.WithContext(store.WithServices(req.Context(), svc))
	rec := httptest.NewRecorder()

	GetMCPServersHandler(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp MCPServersListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	byName := map[string]MCPServerInfo{}
	for _, s := range resp.Servers {
		byName[s.Name] = s
	}

	fileOnly, ok := byName["file-only"]
	if !ok {
		t.Fatalf("expected file-only server in list, got: %+v", resp.Servers)
	}
	if fileOnly.Source != "config" {
		t.Errorf("expected file-only Source=config, got %q", fileOnly.Source)
	}

	overridden, ok := byName["overridden"]
	if !ok {
		t.Fatalf("expected overridden server in list")
	}
	if overridden.Source == "config" {
		t.Errorf("expected DB server to override file entry (Source should be empty), got Source=config")
	}
}
