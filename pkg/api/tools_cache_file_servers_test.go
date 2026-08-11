package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/store"
)

func mcpBoolPtr(b bool) *bool { return &b }

// TestGetCachedToolsForRequest_IncludesFileMCPServers verifies that the
// advertised tool list (the one the model sees via the system prompt and
// GET /api/tools) includes config-file (mcp_config.json) MCP servers, whose
// tools live in the shared on-disk tools cache rather than a per-tenant DB
// column. This is the base layer parity that GetMCPServersHandler,
// loadMCPConfigForRequest, and loadMCPConfig already have.
func TestGetCachedToolsForRequest_IncludesFileMCPServers(t *testing.T) {
	writeMCPConfigFile(t, `{
		"mcpServers": {
			"custom-a": {"command": "npx", "args": ["-y", "custom-a-mcp"], "transport": "stdio", "enabled": true}
		}
	}`)

	cache.SetCacheDir(t.TempDir())
	t.Cleanup(func() { cache.SetCacheDir("") })
	cache.AddServerTools("custom-a", []cache.ToolEntry{
		{Name: "custom_a_tool", Description: "Tool from config-file server", Source: "custom-a"},
	}, "checksum-a")

	svc := &store.Services{Mode: store.ModePlatform, TeamMCPServers: newMemMCPStore()}
	req := httptest.NewRequest("GET", "/api/tools", nil)
	req = req.WithContext(store.WithServices(req.Context(), svc))

	tools := GetCachedToolsForRequest(req)
	if !hasNamedTool(tools, "custom_a_tool", "custom-a") {
		t.Fatalf("expected config-file server tool 'custom_a_tool' (source custom-a) in advertised tools, got: %v", namedTools(tools))
	}
}

// TestGetCachedToolsForRequest_DBServerOverridesFileServer verifies a same-named
// DB (team) server overrides the config-file base entry by name.
func TestGetCachedToolsForRequest_DBServerOverridesFileServer(t *testing.T) {
	writeMCPConfigFile(t, `{
		"mcpServers": {
			"custom-a": {"command": "npx", "args": ["-y", "custom-a-mcp"], "transport": "stdio", "enabled": true}
		}
	}`)

	cache.SetCacheDir(t.TempDir())
	t.Cleanup(func() { cache.SetCacheDir("") })
	cache.AddServerTools("custom-a", []cache.ToolEntry{
		{Name: "file_tool", Description: "from file", Source: "custom-a"},
	}, "checksum-a")

	dbTools, _ := json.Marshal([]map[string]any{{"name": "db_tool", "description": "from db"}})
	svc := &store.Services{
		Mode: store.ModePlatform,
		TeamMCPServers: newMemMCPStore(store.MCPServer{
			Name: "custom-a", Enabled: mcpBoolPtr(true), CachedTools: dbTools,
		}),
	}
	req := httptest.NewRequest("GET", "/api/tools", nil)
	req = req.WithContext(store.WithServices(req.Context(), svc))

	tools := GetCachedToolsForRequest(req)
	if !hasNamedTool(tools, "db_tool", "custom-a") {
		t.Fatalf("expected DB server tool 'db_tool' to override file server, got: %v", namedTools(tools))
	}
	if hasNamedTool(tools, "file_tool", "custom-a") {
		t.Fatalf("file-server tool 'file_tool' should have been overridden by same-named DB server, got: %v", namedTools(tools))
	}
}

// TestGetCachedToolsForRequest_DisabledFileServerExcluded verifies a disabled
// config-file server does not contribute tools to the advertised list.
func TestGetCachedToolsForRequest_DisabledFileServerExcluded(t *testing.T) {
	writeMCPConfigFile(t, `{
		"mcpServers": {
			"custom-a": {"command": "npx", "transport": "stdio", "enabled": false}
		}
	}`)

	cache.SetCacheDir(t.TempDir())
	t.Cleanup(func() { cache.SetCacheDir("") })
	cache.AddServerTools("custom-a", []cache.ToolEntry{
		{Name: "custom_a_tool", Description: "should not appear", Source: "custom-a"},
	}, "checksum-a")

	svc := &store.Services{Mode: store.ModePlatform, TeamMCPServers: newMemMCPStore()}
	req := httptest.NewRequest("GET", "/api/tools", nil)
	req = req.WithContext(store.WithServices(req.Context(), svc))

	tools := GetCachedToolsForRequest(req)
	if hasNamedTool(tools, "custom_a_tool", "custom-a") {
		t.Fatalf("disabled config-file server tool should be excluded, got: %v", namedTools(tools))
	}
}

func hasNamedTool(tools []ToolInfo, name, source string) bool {
	for _, ti := range tools {
		if ti.Name == name && ti.Source == source {
			return true
		}
	}
	return false
}

func namedTools(tools []ToolInfo) []string {
	out := make([]string, 0, len(tools))
	for _, ti := range tools {
		out = append(out, ti.Name+"@"+ti.Source)
	}
	return out
}
