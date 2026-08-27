package config

import (
	"log/slog"
	"os"
)

// ProviderEnvMapping maps provider config keys to environment variable names
// This is the single source of truth for how config keys map to env vars
var ProviderEnvMapping = map[string]map[string]string{
	"anthropic": {
		"api_key": "ANTHROPIC_API_KEY",
	},
	"gemini": {
		"api_key": "GOOGLE_API_KEY",
	},
	"openai": {
		"api_key": "OPENAI_API_KEY",
	},
	"sap_ai_core": {
		"client_id":      "AICORE_CLIENT_ID",
		"client_secret":  "AICORE_CLIENT_SECRET",
		"auth_url":       "AICORE_AUTH_URL",
		"base_url":       "AICORE_BASE_URL",
		"resource_group": "AICORE_RESOURCE_GROUP",
	},
	"xai": {
		"api_key": "XAI_API_KEY",
	},
	"grok": {
		"api_key": "XAI_API_KEY",
	},
	"xai_oauth": {
		"client_id":      "XAI_OAUTH_CLIENT_ID",
		"access_token":   "XAI_OAUTH_ACCESS_TOKEN",
		"refresh_token":  "XAI_OAUTH_REFRESH_TOKEN",
	},
	"groq": {
		"api_key": "GROQ_API_KEY",
	},
	"openrouter": {
		"api_key": "OPENROUTER_API_KEY",
	},
	"poe": {
		"api_key": "POE_API_KEY",
	},
	"ollama": {
		"base_url": "OLLAMA_BASE_URL",
	},
	"lm_studio": {
		"base_url": "LMSTUDIO_BASE_URL",
	},
	"litellm": {
		"api_key":  "LITELLM_API_KEY",
		"base_url": "LITELLM_BASE_URL",
	},
}

// SecretGetter resolves a secret by key from an encrypted store.
// Returns empty string if the key is not found.
type SecretGetter func(key string) string

// SetupProviderEnv sets environment variables from config for a specific provider.
// This is the legacy path that reads from the plaintext ProviderConfig map.
func SetupProviderEnv(providerName string, providerCfg ProviderConfig) {
	if mapping, ok := ProviderEnvMapping[providerName]; ok {
		for cfgKey, envKey := range mapping {
			if val, ok := providerCfg[cfgKey]; ok && val != "" {
				if err := os.Setenv(envKey, val); err != nil {
					slog.Warn("failed to set env var", "key", envKey, "error", err)
				}
			}
		}
	}
}

// SetupAllProviderEnv sets environment variables for all configured providers.
// This is the legacy path that reads from the plaintext config.yaml.
// After migration, use SetupAllProviderEnvFromStore instead.
func SetupAllProviderEnv(appCfg *AppConfig) {
	if appCfg == nil || appCfg.Providers == nil {
		return
	}
	for providerName, providerCfg := range appCfg.Providers {
		SetupProviderEnv(providerName, providerCfg)
	}
}

// SetupAllProviderEnvFromStore sets environment variables for all configured
// providers, reading secret values from the encrypted credential store and
// non-secret values from the config map. Falls back to existing env vars.
//
// Resolution order per field:
//  1. Credential store (via getSecret)
//  2. Config map (for non-secret fields like base_url, resource_group)
//  3. Already-set env var (external environment)
func SetupAllProviderEnvFromStore(appCfg *AppConfig, getSecret SecretGetter) {
	if appCfg == nil || appCfg.Providers == nil {
		return
	}

	for instanceName, providerCfg := range appCfg.Providers {
		provType := GetProviderType(instanceName, providerCfg)
		if provType == "" {
			provType = instanceName
		}

		mapping, ok := ProviderEnvMapping[provType]
		if !ok {
			continue
		}

		for cfgKey, envVar := range mapping {
			// 1. Try credential store
			storeKey := "provider." + instanceName + "." + cfgKey
			if val := getSecret(storeKey); val != "" {
				if err := os.Setenv(envVar, val); err != nil {
					slog.Warn("failed to set env var from store", "key", envVar, "error", err)
				}
				continue
			}

			// 2. Try config map (non-secret fields like base_url stay here)
			if val, has := providerCfg[cfgKey]; has && val != "" {
				if err := os.Setenv(envVar, val); err != nil {
					slog.Warn("failed to set env var from config", "key", envVar, "error", err)
				}
				continue
			}

			// 3. Env var may already be set externally — leave it alone
		}
	}

	// Also set web server API keys as env vars for MCP servers
	if appCfg.WebServers != nil {
		webServerEnvMapping := map[string]string{
			"tavily":       "TAVILY_API_KEY",
			"brave-search": "BRAVE_API_KEY",
			"firecrawl":    "FIRECRAWL_API_KEY",
		}
		for serverID, envVar := range webServerEnvMapping {
			storeKey := "web_servers." + serverID + ".api_key"
			if val := getSecret(storeKey); val != "" {
				if err := os.Setenv(envVar, val); err != nil {
					slog.Warn("failed to set web server env var", "key", envVar, "error", err)
				}
				continue
			}
			// Fallback to config
			if wsCfg, ok := appCfg.WebServers[serverID]; ok && wsCfg.APIKey != "" {
				if err := os.Setenv(envVar, wsCfg.APIKey); err != nil {
					slog.Warn("failed to set web server env var from config", "key", envVar, "error", err)
				}
			}
		}
	}
}

// SetupDelegateEnv sets environment variables needed by fleet delegate tools.
// For each env var name, it tries the credential store first
// (key: "delegate.env.<NAME>"), then leaves any already-set env var in place.
// This should be called after fleet configs are loaded.
func SetupDelegateEnv(envNames []string, getSecret SecretGetter) {
	for _, name := range envNames {
		// 1. Try credential store
		if getSecret != nil {
			storeKey := "delegate.env." + name
			if val := getSecret(storeKey); val != "" {
				if err := os.Setenv(name, val); err != nil {
					slog.Warn("failed to set delegate env var", "key", name, "error", err)
				}
				continue
			}
		}
		// 2. Env var may already be set externally — leave it alone
	}
}

// SetupMCPEnv sets environment variables from MCP server configs
func SetupMCPEnv(mcpCfg *MCPConfig) {
	if mcpCfg == nil {
		return
	}
	for _, server := range mcpCfg.MCPServers {
		for k, v := range server.Env {
			if v != "" {
				if err := os.Setenv(k, v); err != nil {
					slog.Warn("failed to set MCP env var", "key", k, "error", err)
				}
			}
		}
	}
}

// providerSecretKeys lists config keys that may contain secrets for each provider type.
// This mirrors the secretKeyMapping in credentials/migrate.go. We maintain a copy here
// to avoid a circular import (config ← credentials → config).
var providerSecretKeys = map[string][]string{
	"anthropic":     {"api_key"},
	"gemini":        {"api_key"},
	"openai":        {"api_key"},
	"openrouter":    {"api_key"},
	"groq":          {"api_key"},
	"xai":           {"api_key"},
	"grok":          {"api_key"},
	"poe":           {"api_key"},
	"litellm":       {"api_key"},
	"openai_compat": {"api_key"},
	"sap_ai_core":   {"client_id", "client_secret", "auth_url"},
}

// InjectProviderSecretsToConfig reads secrets from the credential store and writes
// them back into the AppConfig.Providers map. This ensures that GetProvider() can
// read secrets from the config map regardless of provider type — including providers
// like openai_compat that have no env var fallback in ProviderEnvMapping.
//
// This must be called BEFORE GetProvider() and AFTER the credential store is opened.
func InjectProviderSecretsToConfig(appCfg *AppConfig, getSecret SecretGetter) {
	if appCfg == nil || appCfg.Providers == nil || getSecret == nil {
		return
	}

	for instanceName, providerCfg := range appCfg.Providers {
		provType := GetProviderType(instanceName, providerCfg)
		if provType == "" {
			provType = instanceName
		}

		secretKeys, ok := providerSecretKeys[provType]
		if !ok {
			// Unknown provider type — try api_key as a common default
			secretKeys = []string{"api_key"}
		}

		for _, key := range secretKeys {
			// Skip if config already has a value (not scrubbed)
			if val, has := providerCfg[key]; has && val != "" {
				continue
			}

			// Try the credential store
			storeKey := "provider." + instanceName + "." + key
			if val := getSecret(storeKey); val != "" {
				providerCfg[key] = val
			}
		}
	}
}
