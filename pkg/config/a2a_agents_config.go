package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// A2AAgentFileConfig represents the configuration for a single remote A2A agent connection.
type A2AAgentFileConfig struct {
	URL            string            `json:"url"`
	CredentialName string            `json:"credential_name,omitempty"`
	AuthType       string            `json:"auth_type,omitempty"` // bearer, api_key, oauth
	Enabled        *bool             `json:"enabled,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Timeout        string            `json:"timeout,omitempty"` // duration string e.g. "30s", "2m"
}

// IsEnabled returns true if the agent is enabled (defaults to true if not set).
func (c *A2AAgentFileConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// A2AAgentsConfig represents the entire A2A agents configuration file.
type A2AAgentsConfig struct {
	Agents map[string]A2AAgentFileConfig `json:"a2aAgents"`
}

// LoadA2AAgentsConfig loads the A2A agents configuration from the config directory.
func LoadA2AAgentsConfig() (*A2AAgentsConfig, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get config directory: %w", err)
	}

	configPath := filepath.Join(configDir, "a2a_agents.json")

	var config A2AAgentsConfig

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		config.Agents = make(map[string]A2AAgentFileConfig)
	} else {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read A2A agents config file: %w", err)
		}

		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("failed to parse A2A agents config: %w", err)
		}

		if config.Agents == nil {
			config.Agents = make(map[string]A2AAgentFileConfig)
		}
	}

	return &config, nil
}

// SaveA2AAgentsConfig saves the A2A agents configuration to the config directory.
func SaveA2AAgentsConfig(config *A2AAgentsConfig) error {
	configDir, err := GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	configPath := filepath.Join(configDir, "a2a_agents.json")

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal A2A agents config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write A2A agents config file: %w", err)
	}

	return nil
}

// FileA2AAgents returns the A2A agents declared in the local a2a_agents.json file.
// This is the base layer for the platform-mode cascade.
// Returns an empty map (never nil) on read errors.
func FileA2AAgents() map[string]A2AAgentFileConfig {
	cfg, err := LoadA2AAgentsConfig()
	if err != nil || cfg == nil || cfg.Agents == nil {
		return map[string]A2AAgentFileConfig{}
	}
	// Copy so callers can freely mutate
	out := make(map[string]A2AAgentFileConfig, len(cfg.Agents))
	for name, agent := range cfg.Agents {
		out[name] = agent
	}
	return out
}

// GetA2AAgentsConfigPath returns the path to the A2A agents config file.
func GetA2AAgentsConfigPath() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "a2a_agents.json"), nil
}

// SetA2AAgentEnabled sets the enabled status for a specific A2A agent.
func SetA2AAgentEnabled(agentName string, enabled bool) error {
	config, err := LoadA2AAgentsConfig()
	if err != nil {
		return fmt.Errorf("failed to load A2A agents config: %w", err)
	}

	agent, exists := config.Agents[agentName]
	if !exists {
		return fmt.Errorf("agent '%s' not found", agentName)
	}

	agent.Enabled = &enabled
	config.Agents[agentName] = agent

	if err := SaveA2AAgentsConfig(config); err != nil {
		return fmt.Errorf("failed to save A2A agents config: %w", err)
	}

	return nil
}
