package credentials

import (
	"fmt"
	"log"

	"github.com/SAP/astonish/pkg/config"
)

// secretKeyMapping defines which provider config keys contain sensitive values
// that should be migrated to the credential store. Only secret fields are listed
// here — non-sensitive config like base_url and resource_group stays in config.yaml.
var secretKeyMapping = map[string][]string{
	"anthropic":     {"api_key"},
	"gemini":        {"api_key"},
	"openai":        {"api_key"},
	"openrouter":    {"api_key"},
	"groq":          {"api_key"},
	"xai":           {"api_key"},
	"xai_oauth":     {"access_token", "refresh_token", "expires_at"},
	"grok":          {"api_key"},
	"poe":           {"api_key"},
	"litellm":       {"api_key"},
	"openai_compat": {"api_key"},
	"sap_ai_core":   {"client_id", "client_secret", "auth_url"},
}

// MigrateFromConfig moves sensitive values from a plaintext AppConfig into the
// encrypted credential store. It extracts provider API keys, the Telegram bot
// token, web server API keys, and the embedding API key.
//
// After extraction, the sensitive fields are cleared from the AppConfig struct.
// The caller is responsible for re-saving the scrubbed config.yaml.
//
// This function is idempotent — if HasMigrated() returns true, it returns
// immediately. After a successful migration it calls SetMigrated().
//
// Returns the number of secrets migrated, or an error.
func MigrateFromConfig(store *Store, appCfg *config.AppConfig, logger *log.Logger) (int, error) {
	if store == nil || appCfg == nil {
		return 0, nil
	}
	if store.HasMigrated() {
		return 0, nil
	}

	secrets := make(map[string]string)

	// --- 1. Provider credentials ---
	for instanceName, pCfg := range appCfg.Providers {
		provType := config.GetProviderType(instanceName, pCfg)
		if provType == "" {
			provType = instanceName
		}

		secretKeys, ok := secretKeyMapping[provType]
		if !ok {
			// Unknown provider type — check if it has an api_key field anyway
			if val, has := pCfg["api_key"]; has && val != "" {
				secretKeys = []string{"api_key"}
			} else {
				continue
			}
		}

		for _, key := range secretKeys {
			val, has := pCfg[key]
			if !has || val == "" {
				continue
			}
			storeKey := "provider." + instanceName + "." + key
			secrets[storeKey] = val
		}
	}

	// --- 2. Telegram bot token ---
	if appCfg.Channels.Telegram.BotToken != "" {
		secrets["channels.telegram.bot_token"] = appCfg.Channels.Telegram.BotToken
	}

	// --- 2b. Email channel password ---
	if appCfg.Channels.Email.Password != "" {
		secrets["channels.email.password"] = appCfg.Channels.Email.Password
	}

	// --- 2c. Slack channel tokens ---
	if appCfg.Channels.Slack.BotToken != "" {
		secrets["channels.slack.bot_token"] = appCfg.Channels.Slack.BotToken
	}
	if appCfg.Channels.Slack.AppToken != "" {
		secrets["channels.slack.app_token"] = appCfg.Channels.Slack.AppToken
	}
	if appCfg.Channels.Slack.SigningSecret != "" {
		secrets["channels.slack.signing_secret"] = appCfg.Channels.Slack.SigningSecret
	}
	if appCfg.Channels.Slack.ConfigToken != "" {
		secrets["channels.slack.config_token"] = appCfg.Channels.Slack.ConfigToken
	}
	if appCfg.Channels.Slack.ClientSecret != "" {
		secrets["channels.slack.client_secret"] = appCfg.Channels.Slack.ClientSecret
	}

	// --- 3. Web server API keys (Tavily, Brave, Firecrawl) ---
	for serverID, wsCfg := range appCfg.WebServers {
		if wsCfg.APIKey != "" {
			secrets["web_servers."+serverID+".api_key"] = wsCfg.APIKey
		}
	}

	// --- 4. Embedding API key ---
	if appCfg.Memory.Embedding.APIKey != "" {
		secrets["memory.embedding.api_key"] = appCfg.Memory.Embedding.APIKey
	}

	if len(secrets) == 0 {
		// Nothing to migrate, but mark as done to avoid re-scanning
		if err := store.SetMigrated(); err != nil {
			return 0, fmt.Errorf("failed to mark migration complete: %w", err)
		}
		if logger != nil {
			logger.Printf("[credentials] Migration: no secrets found in config.yaml")
		}
		return 0, nil
	}

	// Write all secrets in one batch
	if err := store.SetSecretBatch(secrets); err != nil {
		return 0, fmt.Errorf("failed to write secrets to credential store: %w", err)
	}

	// --- 5. Scrub secrets from the AppConfig struct ---
	scrubAppConfig(appCfg)

	// Mark migration complete
	if err := store.SetMigrated(); err != nil {
		return len(secrets), fmt.Errorf("secrets migrated but failed to mark complete: %w", err)
	}

	if logger != nil {
		logger.Printf("[credentials] Migration complete: %d secrets moved from config.yaml to encrypted store", len(secrets))
	}

	return len(secrets), nil
}

// scrubAppConfig removes all sensitive values from the AppConfig struct.
// After calling this, the struct is safe to write back to config.yaml.
func scrubAppConfig(appCfg *config.AppConfig) {
	// Provider secrets
	for instanceName, pCfg := range appCfg.Providers {
		provType := config.GetProviderType(instanceName, pCfg)
		if provType == "" {
			provType = instanceName
		}

		secretKeys, ok := secretKeyMapping[provType]
		if !ok {
			// Unknown provider — scrub api_key if present
			delete(pCfg, "api_key")
			continue
		}

		for _, key := range secretKeys {
			delete(pCfg, key)
		}
	}

	// Telegram bot token
	appCfg.Channels.Telegram.BotToken = ""

	// Email channel password
	appCfg.Channels.Email.Password = ""

	// Slack channel secrets
	appCfg.Channels.Slack.BotToken = ""
	appCfg.Channels.Slack.AppToken = ""
	appCfg.Channels.Slack.SigningSecret = ""
	appCfg.Channels.Slack.ConfigToken = ""
	appCfg.Channels.Slack.ClientSecret = ""

	// Web server API keys
	for id, wsCfg := range appCfg.WebServers {
		wsCfg.APIKey = ""
		appCfg.WebServers[id] = wsCfg
	}

	// Embedding API key
	appCfg.Memory.Embedding.APIKey = ""
}

// ScrubAppConfig is the exported version of scrubAppConfig for use by
// setup commands that need to remove secrets before saving config.yaml.
func ScrubAppConfig(appCfg *config.AppConfig) {
	scrubAppConfig(appCfg)
}
