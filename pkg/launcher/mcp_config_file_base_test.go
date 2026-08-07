package launcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// memMCPStore is a minimal in-memory store.MCPServerStore for launcher tests.
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

// TestLoadMCPConfig_FileBaseMergedInPlatformMode verifies loadMCPConfig includes
// config-file (mcp_config.json) servers as the base layer in platform mode, and
// that a same-named DB server overrides the file entry by name. This must match
// the behavior of loadMCPConfigForRequest in pkg/api.
func TestLoadMCPConfig_FileBaseMergedInPlatformMode(t *testing.T) {
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

	ctx := store.WithMCPServerStores(context.Background(), &store.MCPServerStores{
		Org: orgStore,
	})

	cfg, err := loadMCPConfig(ctx, true)
	if err != nil {
		t.Fatalf("loadMCPConfig failed: %v", err)
	}

	if _, ok := cfg.MCPServers["file-only"]; !ok {
		t.Errorf("expected file-only server present in platform-mode config, got: %v", cfg.MCPServers)
	}

	overridden, ok := cfg.MCPServers["overridden"]
	if !ok {
		t.Fatalf("expected overridden server present, got: %v", cfg.MCPServers)
	}
	if len(overridden.Args) != 1 || overridden.Args[0] != "db-version.js" {
		t.Errorf("expected DB entry to override file entry by name, got args: %v", overridden.Args)
	}
}
