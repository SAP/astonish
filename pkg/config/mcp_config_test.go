package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func boolPtr(b bool) *bool {
	return &b
}

// TestMergeStandardServers_PreservesDisabledKeyBased verifies that a key-based server
// with Enabled=false keeps that flag when re-merged with a valid API key.
func TestMergeStandardServers_PreservesDisabledKeyBased(t *testing.T) {
	originalGetter := getInstalledSecretGetter()
	SetInstalledSecretGetter(func(key string) string {
		if key == "web_servers.tavily.api_key" {
			return "test-key"
		}
		return ""
	})
	defer SetInstalledSecretGetter(originalGetter)

	cfg := &MCPConfig{
		MCPServers: map[string]MCPServerConfig{
			"tavily": {
				Command: "npx",
				Args:    []string{"-y", "tavily-mcp@latest"},
				Enabled: boolPtr(false),
			},
		},
	}

	// General selects tavily so it remains after merge; Enabled=false must be preserved.
	mergeStandardServersWithConfig(cfg, &AppConfig{
		General: GeneralConfig{WebSearchTool: "tavily:tavily_search"},
	})

	srv, ok := cfg.MCPServers["tavily"]
	if !ok {
		t.Fatal("selected tavily should remain after merge")
	}
	if srv.Enabled == nil || *srv.Enabled {
		t.Fatal("Enabled flag should stay false after merge")
	}
}

// TestMergeStandardServers_CustomServerUntouched verifies that mergeStandardServers
// does not modify entries for non-standard (custom) servers.
func TestMergeStandardServers_CustomServerUntouched(t *testing.T) {
	cfg := &MCPConfig{
		MCPServers: map[string]MCPServerConfig{
			"my-custom-server": {
				Command: "node",
				Args:    []string{"server.js"},
				Enabled: boolPtr(false),
			},
		},
	}

	mergeStandardServers(cfg)

	srv, ok := cfg.MCPServers["my-custom-server"]
	if !ok {
		t.Fatal("custom server entry missing after merge")
	}
	if srv.Enabled == nil {
		t.Fatal("Enabled flag was reset to nil (should be false)")
	}
	if *srv.Enabled {
		t.Fatal("Enabled flag was flipped to true (should be false)")
	}
}

// setupTempConfigDir creates a temporary directory structure that GetConfigDir() will use.
// Returns the config directory path.
func setupTempConfigDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	if runtime.GOOS == "darwin" {
		// macOS: UserConfigDir() returns $HOME/Library/Application Support
		configDir := filepath.Join(tmpDir, "Library", "Application Support", "astonish")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			t.Fatalf("failed to create temp config dir: %v", err)
		}
		t.Setenv("HOME", tmpDir)
		return configDir
	}
	// Linux/other: UserConfigDir() returns $XDG_CONFIG_HOME or $HOME/.config
	configDir := filepath.Join(tmpDir, "astonish")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	return configDir
}

// TestSaveMCPConfig_PersistsDisabledStandardServers verifies that SaveMCPConfig keeps
// disabled standard server entries in mcp_config.json.
func TestSaveMCPConfig_PersistsDisabledStandardServers(t *testing.T) {
	configDir := setupTempConfigDir(t)

	cfg := &MCPConfig{
		MCPServers: map[string]MCPServerConfig{
			"tavily": {
				Command: "npx",
				Args:    []string{"-y", "tavily-mcp@latest"},
				Enabled: boolPtr(false), // explicitly disabled
			},
			"my-custom-server": {
				Command: "node",
				Args:    []string{"server.js"},
			},
		},
	}

	if err := SaveMCPConfig(cfg); err != nil {
		t.Fatalf("SaveMCPConfig failed: %v", err)
	}

	// Verify file was written
	data, err := os.ReadFile(filepath.Join(configDir, "mcp_config.json"))
	if err != nil {
		t.Fatalf("failed to read saved config: %v", err)
	}

	// Load it back raw (no merge)
	loaded, err := LoadMCPConfigRaw()
	if err != nil {
		t.Fatalf("LoadMCPConfigRaw failed: %v", err)
	}

	// Disabled standard server should be persisted
	srv, ok := loaded.MCPServers["tavily"]
	if !ok {
		t.Fatalf("disabled tavily should be persisted, got: %s", string(data))
	}
	if srv.Enabled == nil || *srv.Enabled {
		t.Fatal("tavily Enabled should be false after round-trip")
	}

	// Custom server should always be persisted
	if _, ok := loaded.MCPServers["my-custom-server"]; !ok {
		t.Fatal("custom server should be persisted")
	}
}

// TestSaveMCPConfig_StripsEnabledStandardServers verifies that enabled (or nil-Enabled)
// standard servers are stripped from mcp_config.json since they're managed by config.yaml.
func TestSaveMCPConfig_StripsEnabledStandardServers(t *testing.T) {
	setupTempConfigDir(t)

	cfg := &MCPConfig{
		MCPServers: map[string]MCPServerConfig{
			"tavily": {
				Command: "npx",
				Args:    []string{"-y", "tavily-mcp@latest"},
				Enabled: boolPtr(true), // explicitly enabled → should be stripped
			},
			"brave-search": {
				Command: "npx",
				Args:    []string{"-y", "@brave/brave-search-mcp-server"},
				// Enabled is nil → defaults to true → should be stripped
			},
			"my-custom-server": {
				Command: "node",
				Args:    []string{"server.js"},
			},
		},
	}

	if err := SaveMCPConfig(cfg); err != nil {
		t.Fatalf("SaveMCPConfig failed: %v", err)
	}

	loaded, err := LoadMCPConfigRaw()
	if err != nil {
		t.Fatalf("LoadMCPConfigRaw failed: %v", err)
	}

	if _, ok := loaded.MCPServers["tavily"]; ok {
		t.Fatal("explicitly enabled tavily should be stripped from saved config")
	}
	if _, ok := loaded.MCPServers["brave-search"]; ok {
		t.Fatal("nil-enabled brave-search should be stripped from saved config")
	}
	if _, ok := loaded.MCPServers["my-custom-server"]; !ok {
		t.Fatal("custom server should always be persisted")
	}
}

// TestSaveMCPConfig_RoundTripPreservesDisabledFlag tests the full save → load (raw) → merge cycle:
// a disabled standard server persists through save, is present in raw load, and after merge
// still has Enabled=false when it is the General-selected web tool.
func TestSaveMCPConfig_RoundTripPreservesDisabledFlag(t *testing.T) {
	setupTempConfigDir(t)

	originalGetter := getInstalledSecretGetter()
	SetInstalledSecretGetter(func(key string) string {
		if key == "web_servers.tavily.api_key" {
			return "test-key"
		}
		return ""
	})
	defer SetInstalledSecretGetter(originalGetter)

	original := &MCPConfig{
		MCPServers: map[string]MCPServerConfig{
			"tavily": {
				Command: "npx",
				Args:    []string{"-y", "tavily-mcp@latest"},
				Enabled: boolPtr(false),
			},
		},
	}

	// Save
	if err := SaveMCPConfig(original); err != nil {
		t.Fatalf("SaveMCPConfig failed: %v", err)
	}

	// Load raw (simulates reading mcp_config.json)
	raw, err := LoadMCPConfigRaw()
	if err != nil {
		t.Fatalf("LoadMCPConfigRaw failed: %v", err)
	}

	// Merge with General selecting tavily
	mergeStandardServersWithConfig(raw, &AppConfig{
		General: GeneralConfig{WebSearchTool: "tavily:tavily_search"},
	})

	srv, ok := raw.MCPServers["tavily"]
	if !ok {
		t.Fatal("tavily should exist after merge when selected in General")
	}
	if srv.Enabled == nil {
		t.Fatal("Enabled should not be nil after round-trip + merge")
	}
	if *srv.Enabled {
		t.Fatal("Enabled should be false after round-trip + merge")
	}
}

// TestIsEnabled_DefaultsToTrue tests the IsEnabled method
func TestIsEnabled_DefaultsToTrue(t *testing.T) {
	tests := []struct {
		name     string
		enabled  *bool
		expected bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := MCPServerConfig{Enabled: tc.enabled}
			if got := cfg.IsEnabled(); got != tc.expected {
				t.Errorf("IsEnabled() = %v, want %v", got, tc.expected)
			}
		})
	}
}

// TestMergeStandardServersWithConfig_TeamWebSearchTool verifies that passing an
// AppConfig with WebSearchTool set causes the matching standard server to be
// injected into the MCPConfig, even when no on-disk config.yaml has the setting.
// This is the core regression test for the K8s platform-mode fix where
// WebSearchTool is sourced from team settings (DB) rather than config.yaml.
func TestMergeStandardServersWithConfig_TeamWebSearchTool(t *testing.T) {
	// Register a test secret getter that returns a fake API key for tavily.
	// This simulates the platform_secrets store having the key.
	originalGetter := getInstalledSecretGetter()
	SetInstalledSecretGetter(func(key string) string {
		if key == "web_servers.tavily.api_key" {
			return "test-tavily-key-12345"
		}
		return ""
	})
	defer SetInstalledSecretGetter(originalGetter)

	// Simulate platform mode: AppConfig with WebSearchTool from team settings DB.
	// The on-disk config.yaml would have no web_search_tool (K8s ephemeral fs).
	appCfg := &AppConfig{
		General: GeneralConfig{
			WebSearchTool:  "tavily:tavily_search",
			WebExtractTool: "tavily:tavily_extract",
		},
	}

	cfg := &MCPConfig{MCPServers: make(map[string]MCPServerConfig)}
	MergeStandardServersWithConfig(cfg, appCfg)

	// Tavily should be injected because appCfg.General.WebSearchTool is set.
	srv, ok := cfg.MCPServers["tavily"]
	if !ok {
		t.Fatal("tavily should be injected when WebSearchTool is set in passed appCfg")
	}
	if srv.Command != "npx" {
		t.Errorf("expected command 'npx', got %q", srv.Command)
	}
	// Verify the API key was resolved from the getter
	if srv.Env == nil || srv.Env["TAVILY_API_KEY"] != "test-tavily-key-12345" {
		t.Errorf("expected TAVILY_API_KEY to be set from getter, got env: %v", srv.Env)
	}
}

// TestMergeStandardServersWithConfig_NoWebSearchTool verifies that when
// AppConfig has no WebSearchTool set, web-category standard servers are NOT injected.
func TestMergeStandardServersWithConfig_NoWebSearchTool(t *testing.T) {
	originalGetter := getInstalledSecretGetter()
	SetInstalledSecretGetter(func(key string) string {
		if key == "web_servers.tavily.api_key" {
			return "test-tavily-key-12345"
		}
		return ""
	})
	defer SetInstalledSecretGetter(originalGetter)

	appCfg := &AppConfig{}

	cfg := &MCPConfig{MCPServers: make(map[string]MCPServerConfig)}
	MergeStandardServersWithConfig(cfg, appCfg)

	if _, ok := cfg.MCPServers["tavily"]; ok {
		t.Fatal("tavily should NOT be injected when WebSearchTool is empty")
	}
}

// TestMergeStandardServersWithConfig_OnlySelectedWebProvider verifies that only the
// General-selected web provider is exposed even when multiple API keys are installed.
func TestMergeStandardServersWithConfig_OnlySelectedWebProvider(t *testing.T) {
	originalGetter := getInstalledSecretGetter()
	SetInstalledSecretGetter(func(key string) string {
		switch key {
		case "web_servers.tavily.api_key":
			return "tavily-key"
		case "web_servers.brave-search.api_key":
			return "brave-key"
		default:
			return ""
		}
	})
	defer SetInstalledSecretGetter(originalGetter)

	appCfg := &AppConfig{
		General: GeneralConfig{
			WebSearchTool: "tavily:tavily_search",
		},
	}
	cfg := &MCPConfig{
		MCPServers: map[string]MCPServerConfig{
			// Simulate a previously installed provider still present in the MCP store.
			"brave-search": {Command: "npx", Args: []string{"brave"}},
		},
	}
	MergeStandardServersWithConfig(cfg, appCfg)

	if _, ok := cfg.MCPServers["tavily"]; !ok {
		t.Fatal("expected selected tavily provider")
	}
	if _, ok := cfg.MCPServers["brave-search"]; ok {
		t.Fatal("brave-search must be filtered out when General selects tavily")
	}
}

// TestMergeStandardServersWithConfig_HyphenatedToolName verifies that the
// team-settings value with a hyphenated tool name (e.g., "tavily:tavily-search")
// still correctly identifies the server ID for injection.
func TestMergeStandardServersWithConfig_HyphenatedToolName(t *testing.T) {
	originalGetter := getInstalledSecretGetter()
	SetInstalledSecretGetter(func(key string) string {
		if key == "web_servers.tavily.api_key" {
			return "test-key"
		}
		return ""
	})
	defer SetInstalledSecretGetter(originalGetter)

	// Hyphenated tool name as found in some DB entries (migration residue).
	appCfg := &AppConfig{
		General: GeneralConfig{
			WebSearchTool: "tavily:tavily-search",
		},
	}

	cfg := &MCPConfig{MCPServers: make(map[string]MCPServerConfig)}
	MergeStandardServersWithConfig(cfg, appCfg)

	// Should still inject tavily — prefix match is "tavily".
	if _, ok := cfg.MCPServers["tavily"]; !ok {
		t.Fatal("tavily should be injected even with hyphenated tool name (prefix match)")
	}
}

// TestMergeStandardServersWithConfig_NilAppCfgNoFileFallback verifies that
// when nil is passed (platform contexts that didn't supply effective config),
// we do NOT fall back to file-based config for secret-requiring servers (no more
// file-based implementation in platform mode). Only keyless servers are injected.
func TestMergeStandardServersWithConfig_NilAppCfgNoFileFallback(t *testing.T) {
	cfg := &MCPConfig{MCPServers: make(map[string]MCPServerConfig)}
	MergeStandardServersWithConfig(cfg, nil)

	// Tavily (secret-based web) should NOT be present.
	if _, ok := cfg.MCPServers["tavily"]; ok {
		t.Fatal("tavily (secret server) should NOT be injected when nil appCfg is passed — no file fallback")
	}
}
