package a2aclient

import (
	"context"
	"log/slog"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
)

// LoadA2AAgentConfig loads A2A agent configuration with the platform cascade.
// In personal mode (platformMode=false), it loads from the file only.
// In platform mode, it cascades: file → platform → org → team (higher tier overrides by name).
func LoadA2AAgentConfig(ctx context.Context, platformMode bool) (*A2AClientConfig, error) {
	if !platformMode {
		return loadA2AAgentConfigFromFile()
	}
	return loadA2AAgentConfigPlatform(ctx)
}

// loadA2AAgentConfigFromFile loads A2A agents from the personal-mode config file.
func loadA2AAgentConfigFromFile() (*A2AClientConfig, error) {
	fileAgents := config.FileA2AAgents()

	agents := make(map[string]A2AAgentConfig, len(fileAgents))
	for name, fa := range fileAgents {
		if !fa.IsEnabled() {
			continue
		}
		agents[name] = fileAgentToConfig(name, fa)
	}

	return &A2AClientConfig{Agents: agents}, nil
}

// loadA2AAgentConfigPlatform loads A2A agents with 3-tier cascade from DB stores.
func loadA2AAgentConfigPlatform(ctx context.Context) (*A2AClientConfig, error) {
	merged := make(map[string]A2AAgentConfig)

	// Tier 0: File-based agents (cascade root)
	for name, fa := range config.FileA2AAgents() {
		if !fa.IsEnabled() {
			continue
		}
		merged[name] = fileAgentToConfig(name, fa)
	}

	// Load from context stores (platform → org → team)
	stores := store.A2AAgentStoresFromContext(ctx)
	if stores == nil {
		// Fallback: no stores in context, return file-only
		return &A2AClientConfig{Agents: merged}, nil
	}

	// Tier 1: Platform DB
	if stores.Platform != nil {
		agents, err := stores.Platform.List(ctx)
		if err != nil {
			slog.Warn("failed to load platform A2A agents", "error", err)
		} else {
			for _, a := range agents {
				if !a.IsEnabled() {
					continue
				}
				merged[a.Name] = storeAgentToConfig(a)
			}
		}
	}

	// Tier 2: Org DB (overrides platform)
	if stores.Org != nil {
		agents, err := stores.Org.List(ctx)
		if err != nil {
			slog.Warn("failed to load org A2A agents", "error", err)
		} else {
			for _, a := range agents {
				if !a.IsEnabled() {
					continue
				}
				merged[a.Name] = storeAgentToConfig(a)
			}
		}
	}

	// Tier 3: Team DB (overrides org)
	if stores.Team != nil {
		agents, err := stores.Team.List(ctx)
		if err != nil {
			slog.Warn("failed to load team A2A agents", "error", err)
		} else {
			for _, a := range agents {
				if !a.IsEnabled() {
					continue
				}
				merged[a.Name] = storeAgentToConfig(a)
			}
		}
	}

	return &A2AClientConfig{Agents: merged}, nil
}

// fileAgentToConfig converts a file-based agent config to the runtime config.
func fileAgentToConfig(name string, fa config.A2AAgentFileConfig) A2AAgentConfig {
	cfg := A2AAgentConfig{
		Name:           name,
		URL:            fa.URL,
		CredentialName: fa.CredentialName,
		AuthType:       fa.AuthType,
		Enabled:        fa.Enabled,
		Headers:        fa.Headers,
		Timeout:        30 * time.Second, // default
	}
	if fa.Timeout != "" {
		if d, err := time.ParseDuration(fa.Timeout); err == nil {
			cfg.Timeout = d
		}
	}
	return cfg
}

// storeAgentToConfig converts a DB-stored agent to the runtime config.
func storeAgentToConfig(a store.A2AAgent) A2AAgentConfig {
	cfg := A2AAgentConfig{
		Name:           a.Name,
		URL:            a.URL,
		CredentialName: a.CredentialName,
		AuthType:       a.AuthType,
		Enabled:        a.Enabled,
		Headers:        a.Headers,
		Timeout:        30 * time.Second, // default
	}
	if a.Timeout != "" {
		if d, err := time.ParseDuration(a.Timeout); err == nil {
			cfg.Timeout = d
		}
	}
	return cfg
}
