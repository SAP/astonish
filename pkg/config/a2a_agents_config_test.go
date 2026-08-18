package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setupTempA2AConfigDir creates a temporary directory structure that GetConfigDir() will use.
// Returns the config directory path.
func setupTempA2AConfigDir(t *testing.T) string {
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

func TestLoadA2AAgentsConfig_Empty(t *testing.T) {
	setupTempA2AConfigDir(t)

	config, err := LoadA2AAgentsConfig()
	if err != nil {
		t.Fatalf("LoadA2AAgentsConfig() error: %v", err)
	}
	if config == nil {
		t.Fatal("expected non-nil config")
	}
	if config.Agents == nil {
		t.Fatal("expected non-nil Agents map")
	}
	if len(config.Agents) != 0 {
		t.Fatalf("expected empty Agents map, got %d entries", len(config.Agents))
	}
}

func TestLoadA2AAgentsConfig_Valid(t *testing.T) {
	configDir := setupTempA2AConfigDir(t)

	// Write a valid config file
	configData := `{
  "a2aAgents": {
    "code-reviewer": {
      "url": "https://example.com/a2a/code-reviewer",
      "credential_name": "my-cred",
      "auth_type": "bearer",
      "headers": {"X-Custom": "value"},
      "timeout": "30s"
    },
    "translator": {
      "url": "https://example.com/a2a/translator",
      "enabled": false
    }
  }
}`
	configPath := filepath.Join(configDir, "a2a_agents.json")
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	config, err := LoadA2AAgentsConfig()
	if err != nil {
		t.Fatalf("LoadA2AAgentsConfig() error: %v", err)
	}

	if len(config.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(config.Agents))
	}

	reviewer, ok := config.Agents["code-reviewer"]
	if !ok {
		t.Fatal("expected 'code-reviewer' agent")
	}
	if reviewer.URL != "https://example.com/a2a/code-reviewer" {
		t.Fatalf("unexpected URL: %s", reviewer.URL)
	}
	if reviewer.CredentialName != "my-cred" {
		t.Fatalf("unexpected CredentialName: %s", reviewer.CredentialName)
	}
	if reviewer.AuthType != "bearer" {
		t.Fatalf("unexpected AuthType: %s", reviewer.AuthType)
	}
	if reviewer.Headers["X-Custom"] != "value" {
		t.Fatalf("unexpected header: %v", reviewer.Headers)
	}
	if reviewer.Timeout != "30s" {
		t.Fatalf("unexpected Timeout: %s", reviewer.Timeout)
	}
	if !reviewer.IsEnabled() {
		t.Fatal("reviewer should be enabled (nil Enabled)")
	}

	translator, ok := config.Agents["translator"]
	if !ok {
		t.Fatal("expected 'translator' agent")
	}
	if translator.URL != "https://example.com/a2a/translator" {
		t.Fatalf("unexpected URL: %s", translator.URL)
	}
	if translator.IsEnabled() {
		t.Fatal("translator should be disabled")
	}
}

func TestSaveA2AAgentsConfig(t *testing.T) {
	configDir := setupTempA2AConfigDir(t)

	enabled := true
	config := &A2AAgentsConfig{
		Agents: map[string]A2AAgentFileConfig{
			"test-agent": {
				URL:            "https://example.com/a2a/test",
				CredentialName: "cred1",
				AuthType:       "api_key",
				Enabled:        &enabled,
				Headers:        map[string]string{"Authorization": "key123"},
				Timeout:        "1m",
			},
		},
	}

	if err := SaveA2AAgentsConfig(config); err != nil {
		t.Fatalf("SaveA2AAgentsConfig() error: %v", err)
	}

	// Verify file was written
	configPath := filepath.Join(configDir, "a2a_agents.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read saved config: %v", err)
	}

	var reloaded A2AAgentsConfig
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("failed to unmarshal saved config: %v", err)
	}

	agent, ok := reloaded.Agents["test-agent"]
	if !ok {
		t.Fatal("expected 'test-agent' in reloaded config")
	}
	if agent.URL != "https://example.com/a2a/test" {
		t.Fatalf("unexpected URL after reload: %s", agent.URL)
	}
	if agent.CredentialName != "cred1" {
		t.Fatalf("unexpected CredentialName after reload: %s", agent.CredentialName)
	}
	if agent.AuthType != "api_key" {
		t.Fatalf("unexpected AuthType after reload: %s", agent.AuthType)
	}
	if agent.Enabled == nil || !*agent.Enabled {
		t.Fatal("expected Enabled=true after reload")
	}
	if agent.Headers["Authorization"] != "key123" {
		t.Fatalf("unexpected headers after reload: %v", agent.Headers)
	}
	if agent.Timeout != "1m" {
		t.Fatalf("unexpected Timeout after reload: %s", agent.Timeout)
	}
}

func TestA2AAgentFileConfig_IsEnabled(t *testing.T) {
	// nil Enabled → true (default)
	agent := A2AAgentFileConfig{URL: "http://example.com"}
	if !agent.IsEnabled() {
		t.Fatal("nil Enabled should default to true")
	}

	// Enabled = true
	enabled := true
	agent.Enabled = &enabled
	if !agent.IsEnabled() {
		t.Fatal("Enabled=true should return true")
	}

	// Enabled = false
	disabled := false
	agent.Enabled = &disabled
	if agent.IsEnabled() {
		t.Fatal("Enabled=false should return false")
	}
}

func TestFileA2AAgents(t *testing.T) {
	configDir := setupTempA2AConfigDir(t)

	// Write a config file
	configData := `{
  "a2aAgents": {
    "agent1": {"url": "https://example.com/a2a/agent1"},
    "agent2": {"url": "https://example.com/a2a/agent2"}
  }
}`
	configPath := filepath.Join(configDir, "a2a_agents.json")
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	agents := FileA2AAgents()
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if agents["agent1"].URL != "https://example.com/a2a/agent1" {
		t.Fatalf("unexpected agent1 URL: %s", agents["agent1"].URL)
	}
	if agents["agent2"].URL != "https://example.com/a2a/agent2" {
		t.Fatalf("unexpected agent2 URL: %s", agents["agent2"].URL)
	}

	// Verify it's a copy - mutating the returned map shouldn't affect a subsequent call
	agents["agent1"] = A2AAgentFileConfig{URL: "mutated"}
	agents2 := FileA2AAgents()
	if agents2["agent1"].URL != "https://example.com/a2a/agent1" {
		t.Fatal("FileA2AAgents should return a copy; mutation should not persist")
	}
}

func TestFileA2AAgents_NoFile(t *testing.T) {
	setupTempA2AConfigDir(t)

	// No file written - should return empty map, not nil
	agents := FileA2AAgents()
	if agents == nil {
		t.Fatal("FileA2AAgents should never return nil")
	}
	if len(agents) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(agents))
	}
}

func TestSetA2AAgentEnabled(t *testing.T) {
	configDir := setupTempA2AConfigDir(t)

	// Write initial config
	configData := `{
  "a2aAgents": {
    "my-agent": {"url": "https://example.com/a2a/my-agent"}
  }
}`
	configPath := filepath.Join(configDir, "a2a_agents.json")
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// Disable the agent
	if err := SetA2AAgentEnabled("my-agent", false); err != nil {
		t.Fatalf("SetA2AAgentEnabled(false) error: %v", err)
	}

	// Reload and verify
	config, err := LoadA2AAgentsConfig()
	if err != nil {
		t.Fatalf("LoadA2AAgentsConfig() error: %v", err)
	}
	agent := config.Agents["my-agent"]
	if agent.Enabled == nil || *agent.Enabled {
		t.Fatal("expected agent to be disabled")
	}

	// Re-enable the agent
	if err := SetA2AAgentEnabled("my-agent", true); err != nil {
		t.Fatalf("SetA2AAgentEnabled(true) error: %v", err)
	}

	config, err = LoadA2AAgentsConfig()
	if err != nil {
		t.Fatalf("LoadA2AAgentsConfig() error: %v", err)
	}
	agent = config.Agents["my-agent"]
	if agent.Enabled == nil || !*agent.Enabled {
		t.Fatal("expected agent to be enabled")
	}
}

func TestSetA2AAgentEnabled_NotFound(t *testing.T) {
	configDir := setupTempA2AConfigDir(t)

	// Write config without the target agent
	configData := `{"a2aAgents": {}}`
	configPath := filepath.Join(configDir, "a2a_agents.json")
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	err := SetA2AAgentEnabled("nonexistent", true)
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestGetA2AAgentsConfigPath(t *testing.T) {
	setupTempA2AConfigDir(t)

	path, err := GetA2AAgentsConfigPath()
	if err != nil {
		t.Fatalf("GetA2AAgentsConfigPath() error: %v", err)
	}
	if filepath.Base(path) != "a2a_agents.json" {
		t.Fatalf("expected filename 'a2a_agents.json', got '%s'", filepath.Base(path))
	}
}
