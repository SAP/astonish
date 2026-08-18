package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// --- Channel Configuration Types ---

// channelFullInfo describes a channel's full configuration and secret status.
type channelFullInfo struct {
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Enabled     bool              `json:"enabled"`
	Config      map[string]any    `json:"config"`             // non-secret config fields
	Secrets     []channelSecretAt `json:"secrets"`            // which secrets are set (no values)
	SecretsSet  bool              `json:"secrets_configured"` // all required secrets present
}

type channelSecretAt struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Configured bool   `json:"configured"`
}

// channelSecretDef defines a single secret field for a channel.
type channelSecretDef struct {
	key      string
	label    string
	optional bool
}

// channelDefinition defines the metadata for each channel type.
type channelDefinition struct {
	description string
	secrets     []channelSecretDef
}

var channelDefinitions = map[string]channelDefinition{
	"telegram": {
		description: "Telegram Bot (via BotFather)",
		secrets: []channelSecretDef{
			{key: "channels.telegram.bot_token", label: "Bot Token"},
		},
	},
	"email": {
		description: "Email (IMAP/SMTP)",
		secrets: []channelSecretDef{
			{key: "channels.email.password", label: "IMAP/SMTP Password"},
		},
	},
	"slack": {
		description: "Slack App",
		secrets: []channelSecretDef{
			{key: "channels.slack.bot_token", label: "Bot Token (xoxb-...)"},
			{key: "channels.slack.app_token", label: "App-Level Token (xapp-...)"},
			{key: "channels.slack.signing_secret", label: "Signing Secret"},
			{key: "channels.slack.config_token", label: "App Configuration Token (not xapp/xoxb)"},
			{key: "channels.slack.client_id", label: "OAuth Client ID"},
			{key: "channels.slack.client_secret", label: "OAuth Client Secret"},
		},
	},
	"a2a": {
		description: "A2A Protocol (Agent-to-Agent)",
		secrets:     []channelSecretDef{}, // A2A uses per-agent API keys managed via admin endpoints
	},
}

// emailMSGraphSecrets defines the secrets needed for Microsoft Graph provider.
var emailMSGraphSecrets = []channelSecretDef{
	{key: "channels.email.tenant_id", label: "Tenant ID"},
	{key: "channels.email.client_id", label: "Client ID"},
	{key: "channels.email.client_secret", label: "Client Secret (optional)", optional: true},
	{key: "channels.email.refresh_token", label: "Refresh Token"},
}

// PlatformAdminListChannelsHandler handles GET /api/platform/admin/channels.
// Returns the full configuration and secret status of each channel adapter.
func PlatformAdminListChannelsHandler(w http.ResponseWriter, r *http.Request) {
	if RequirePlatformAdmin(w, r) == nil {
		return
	}

	backend := getPlatformBackend()
	if backend == nil {
		respondError(w, http.StatusInternalServerError, "platform store not available")
		return
	}

	secrets := getPlatformSecrets()
	if secrets == nil {
		respondError(w, http.StatusInternalServerError, "platform secrets not available")
		return
	}

	// Load platform settings for channel config
	settingsStore := backend.PlatformSettings()
	settings, _ := settingsStore.Get(r.Context())
	if settings == nil {
		settings = &store.PlatformSettings{}
	}
	channels := settings.Channels
	if channels == nil {
		channels = &store.PlatformChannelSettings{}
	}

	var result []channelFullInfo

	// Telegram
	{
		def := channelDefinitions["telegram"]
		info := channelFullInfo{
			Type:        "telegram",
			Description: def.description,
			Config:      map[string]any{},
		}
		if channels.Telegram != nil {
			info.Enabled = channels.Telegram.Enabled
		}
		allSecretsSet := true
		for _, s := range def.secrets {
			configured := secrets.GetSecret(s.key) != ""
			info.Secrets = append(info.Secrets, channelSecretAt{Key: s.key, Label: s.label, Configured: configured})
			if !configured {
				allSecretsSet = false
			}
		}
		info.SecretsSet = allSecretsSet
		result = append(result, info)
	}

	// Email
	{
		def := channelDefinitions["email"]
		info := channelFullInfo{
			Type:        "email",
			Description: def.description,
			Config:      map[string]any{},
			Secrets:     []channelSecretAt{},
		}
		emailProvider := ""
		if channels.Email != nil {
			info.Enabled = channels.Email.Enabled
			emailProvider = channels.Email.Provider
			info.Config["provider"] = channels.Email.Provider
			info.Config["imap_server"] = channels.Email.IMAPServer
			info.Config["smtp_server"] = channels.Email.SMTPServer
			info.Config["address"] = channels.Email.Address
			info.Config["username"] = channels.Email.Username
			info.Config["credential"] = channels.Email.Credential
			info.Config["poll_interval"] = channels.Email.PollInterval
			info.Config["folder"] = channels.Email.Folder
			info.Config["max_body_chars"] = channels.Email.MaxBodyChars
			if channels.Email.MarkRead != nil {
				info.Config["mark_read"] = *channels.Email.MarkRead
			} else {
				info.Config["mark_read"] = true
			}
		}
		// Pick the right secrets list based on provider
		emailSecrets := def.secrets
		if emailProvider == "msgraph" {
			emailSecrets = emailMSGraphSecrets
		}
		allSecretsSet := true
		for _, s := range emailSecrets {
			configured := secrets.GetSecret(s.key) != ""
			info.Secrets = append(info.Secrets, channelSecretAt{Key: s.key, Label: s.label, Configured: configured})
			if !configured && !s.optional {
				allSecretsSet = false
			}
		}
		info.SecretsSet = allSecretsSet
		result = append(result, info)
	}

	// Slack
	{
		def := channelDefinitions["slack"]
		info := channelFullInfo{
			Type:        "slack",
			Description: def.description,
			Config:      map[string]any{},
		}
		if channels.Slack != nil {
			info.Enabled = channels.Slack.Enabled
			info.Config["mode"] = channels.Slack.Mode
			info.Config["app_id"] = channels.Slack.AppID
			info.Config["command_url"] = channels.Slack.CommandURL
		}
		allSecretsSet := true
		// For Slack, bot_token is always required. app_token required for socket mode,
		// signing_secret required for events mode. Manifest/OAuth secrets are optional.
		requiredSecrets := map[string]bool{
			"channels.slack.bot_token": true,
		}
		if channels.Slack != nil && channels.Slack.Mode == "events" {
			requiredSecrets["channels.slack.signing_secret"] = true
		} else {
			// socket mode (default)
			requiredSecrets["channels.slack.app_token"] = true
		}
		for _, s := range def.secrets {
			configured := secrets.GetSecret(s.key) != ""
			info.Secrets = append(info.Secrets, channelSecretAt{Key: s.key, Label: s.label, Configured: configured})
			if requiredSecrets[s.key] && !configured {
				allSecretsSet = false
			}
		}
		info.SecretsSet = allSecretsSet
		result = append(result, info)
	}

	// A2A (Agent-to-Agent protocol)
	{
		info := channelFullInfo{
			Type:        "a2a",
			Description: "A2A Protocol (Agent-to-Agent)",
			Config:      map[string]any{},
			Secrets:     []channelSecretAt{}, // A2A uses per-agent API keys, not global secrets
			SecretsSet:  true,                // No global secrets needed
		}
		if channels.A2A != nil {
			info.Enabled = channels.A2A.Enabled
			info.Config["base_url"] = channels.A2A.BaseURL
			info.Config["description"] = channels.A2A.Description
			info.Config["rate_limit"] = channels.A2A.RateLimit
			info.Config["max_concurrent_tasks"] = channels.A2A.MaxConcurrentTasks
			info.Config["task_ttl"] = channels.A2A.TaskTTL
			info.Config["default_audience"] = channels.A2A.DefaultAudience
			info.Config["auto_link_by_email"] = channels.A2A.AutoLinkByEmail
			info.Config["require_actor_claim"] = channels.A2A.RequireActorClaim
		}
		result = append(result, info)
	}

	respondJSON(w, http.StatusOK, result)
}
// Saves both config fields and secrets for a channel adapter in one request.
func PlatformAdminSaveChannelHandler(w http.ResponseWriter, r *http.Request) {
	if RequirePlatformAdmin(w, r) == nil {
		return
	}

	backend := getPlatformBackend()
	if backend == nil {
		respondError(w, http.StatusInternalServerError, "platform store not available")
		return
	}

	channelType := mux.Vars(r)["type"]
	if _, ok := channelDefinitions[channelType]; !ok {
		respondError(w, http.StatusBadRequest, "unknown channel type: "+channelType)
		return
	}

	// Parse unified request body
	var body struct {
		Enabled bool              `json:"enabled"`
		Config  map[string]any    `json:"config"`
		Secrets map[string]string `json:"secrets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Save non-secret config to PlatformSettings
	settingsStore := backend.PlatformSettings()
	settings, _ := settingsStore.Get(r.Context())
	if settings == nil {
		settings = &store.PlatformSettings{}
	}
	if settings.Channels == nil {
		settings.Channels = &store.PlatformChannelSettings{}
	}

	switch channelType {
	case "telegram":
		settings.Channels.Telegram = &store.PlatformTelegramConfig{
			Enabled: body.Enabled,
		}
	case "email":
		cfg := &store.PlatformEmailConfig{
			Enabled: body.Enabled,
		}
		if v, ok := body.Config["provider"].(string); ok {
			cfg.Provider = v
		}
		if v, ok := body.Config["imap_server"].(string); ok {
			cfg.IMAPServer = v
		}
		if v, ok := body.Config["smtp_server"].(string); ok {
			cfg.SMTPServer = v
		}
		if v, ok := body.Config["address"].(string); ok {
			cfg.Address = v
		}
		if v, ok := body.Config["username"].(string); ok {
			cfg.Username = v
		}
		if v, ok := body.Config["credential"].(string); ok {
			cfg.Credential = v
		}
		if v, ok := body.Config["poll_interval"]; ok {
			cfg.PollInterval = toInt(v)
		}
		if v, ok := body.Config["folder"].(string); ok {
			cfg.Folder = v
		}
		if v, ok := body.Config["mark_read"].(bool); ok {
			cfg.MarkRead = &v
		}
		if v, ok := body.Config["max_body_chars"]; ok {
			cfg.MaxBodyChars = toInt(v)
		}
		settings.Channels.Email = cfg
	case "slack":
		cfg := &store.PlatformSlackConfig{
			Enabled: body.Enabled,
		}
		if v, ok := body.Config["mode"].(string); ok {
			cfg.Mode = v
		}
		if v, ok := body.Config["app_id"].(string); ok {
			cfg.AppID = v
		}
		if v, ok := body.Config["command_url"].(string); ok {
			cfg.CommandURL = v
		}
		settings.Channels.Slack = cfg
	case "a2a":
		cfg := &store.PlatformA2AConfig{
			Enabled: body.Enabled,
		}
		if v, ok := body.Config["base_url"].(string); ok {
			cfg.BaseURL = v
		}
		if v, ok := body.Config["description"].(string); ok {
			cfg.Description = v
		}
		if v, ok := body.Config["rate_limit"]; ok {
			cfg.RateLimit = toInt(v)
		}
		if v, ok := body.Config["max_concurrent_tasks"]; ok {
			cfg.MaxConcurrentTasks = toInt(v)
		}
		if v, ok := body.Config["task_ttl"].(string); ok {
			cfg.TaskTTL = v
		}
		if v, ok := body.Config["default_audience"].(string); ok {
			cfg.DefaultAudience = v
		}
		if v, ok := body.Config["auto_link_by_email"].(bool); ok {
			cfg.AutoLinkByEmail = v
		}
		if v, ok := body.Config["require_actor_claim"].(bool); ok {
			cfg.RequireActorClaim = v
		}
		if v, ok := body.Config["trusted_issuers"]; ok {
			cfg.TrustedIssuers = parseTrustedIssuers(v)
		}
		if v, ok := body.Config["allowed_agents"]; ok {
			cfg.AllowedAgents = parseAllowedAgents(v)
		}
		settings.Channels.A2A = cfg
	}

	if err := settingsStore.Save(r.Context(), settings); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save channel config: "+err.Error())
		return
	}

	// Save secrets (only non-empty values; empty means "keep existing")
	if len(body.Secrets) > 0 {
		// Determine allowed secret keys based on channel type and provider
		var secretDefs []channelSecretDef
		if channelType == "email" {
			provider, _ := body.Config["provider"].(string)
			if provider == "msgraph" {
				secretDefs = emailMSGraphSecrets
			} else {
				secretDefs = channelDefinitions["email"].secrets
			}
		} else {
			secretDefs = channelDefinitions[channelType].secrets
		}
		allowedKeys := make(map[string]bool, len(secretDefs))
		for _, s := range secretDefs {
			allowedKeys[s.key] = true
		}

		secretStore := getPlatformSecrets()
		if secretStore == nil {
			respondError(w, http.StatusInternalServerError, "platform secrets not available")
			return
		}
		for key, value := range body.Secrets {
			if !allowedKeys[key] {
				respondError(w, http.StatusBadRequest, "key "+key+" is not valid for channel type "+channelType)
				return
			}
			if value == "" {
				continue // empty = keep existing
			}
			if value == "__CLEAR__" {
				// Explicitly remove the secret
				_ = secretStore.RemoveSecret(key)
				continue
			}
			if err := secretStore.SetSecret(key, value); err != nil {
				respondError(w, http.StatusInternalServerError, "failed to save secret: "+err.Error())
				return
			}
		}
	}

	// Trigger channel reload
	reloadMsg := "Channel configuration saved."
	if reload := getChannelReloadFunc(); reload != nil {
		if err := reload(); err != nil {
			reloadMsg = "Configuration saved, but channel reload failed: " + err.Error()
		} else {
			reloadMsg = "Channel configuration saved and channels reloaded successfully."
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"message": reloadMsg,
	})
}

// PlatformAdminDeleteChannelHandler handles DELETE /api/platform/admin/channels/{type}.
// Removes all secrets and disables the channel.
func PlatformAdminDeleteChannelHandler(w http.ResponseWriter, r *http.Request) {
	if RequirePlatformAdmin(w, r) == nil {
		return
	}

	backend := getPlatformBackend()
	if backend == nil {
		respondError(w, http.StatusInternalServerError, "platform store not available")
		return
	}

	secrets := getPlatformSecrets()
	if secrets == nil {
		respondError(w, http.StatusInternalServerError, "platform secrets not available")
		return
	}

	channelType := mux.Vars(r)["type"]
	def, ok := channelDefinitions[channelType]
	if !ok {
		respondError(w, http.StatusBadRequest, "unknown channel type: "+channelType)
		return
	}

	// Remove all secrets
	for _, s := range def.secrets {
		_ = secrets.RemoveSecret(s.key)
	}

	// Disable the channel in PlatformSettings
	settingsStore := backend.PlatformSettings()
	settings, _ := settingsStore.Get(r.Context())
	if settings != nil && settings.Channels != nil {
		switch channelType {
		case "telegram":
			settings.Channels.Telegram = nil
		case "email":
			settings.Channels.Email = nil
		case "slack":
			settings.Channels.Slack = nil
		}
		_ = settingsStore.Save(r.Context(), settings)
	}

	// Trigger channel reload
	reloadMsg := "Channel " + channelType + " removed."
	if reload := getChannelReloadFunc(); reload != nil {
		if err := reload(); err != nil {
			reloadMsg += " Reload failed: " + err.Error()
		} else {
			reloadMsg += " Channels reloaded."
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"message": reloadMsg,
	})
}

// --- Standard MCP Servers (Web Services: Tavily, Brave, Firecrawl) ---

// webServiceInfo describes a standard MCP server's configuration status.
type webServiceInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Configured  bool   `json:"configured"`
	SecretKey   string `json:"secret_key"` // platform_secrets key used
}

// PlatformAdminListWebServicesHandler handles GET /api/platform/admin/web-services.
// Returns the configuration status of standard MCP servers (web services).
func PlatformAdminListWebServicesHandler(w http.ResponseWriter, r *http.Request) {
	if RequirePlatformAdmin(w, r) == nil {
		return
	}

	secrets := getPlatformSecrets()
	if secrets == nil {
		respondError(w, http.StatusInternalServerError, "platform secrets not available")
		return
	}

	stdServers := config.GetStandardServers()
	var services []webServiceInfo

	for _, srv := range stdServers {
		secretKey := "web_servers." + srv.ID + ".api_key"
		services = append(services, webServiceInfo{
			ID:          srv.ID,
			Name:        srv.DisplayName,
			Description: srv.Description,
			Category:    srv.Category,
			Configured:  secrets.GetSecret(secretKey) != "",
			SecretKey:   secretKey,
		})
	}

	respondJSON(w, http.StatusOK, services)
}

// PlatformAdminSetWebServiceKeyHandler handles PUT /api/platform/admin/web-services/{id}.
// Sets the API key for a standard MCP server.
func PlatformAdminSetWebServiceKeyHandler(w http.ResponseWriter, r *http.Request) {
	if RequirePlatformAdmin(w, r) == nil {
		return
	}

	secrets := getPlatformSecrets()
	if secrets == nil {
		respondError(w, http.StatusInternalServerError, "platform secrets not available")
		return
	}

	serverID := mux.Vars(r)["id"]
	srv := config.GetStandardServer(serverID)
	if srv == nil {
		respondError(w, http.StatusBadRequest, "unknown web service: "+serverID)
		return
	}

	var body struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	secretKey := "web_servers." + serverID + ".api_key"

	if body.APIKey == "" {
		_ = secrets.RemoveSecret(secretKey)
		respondJSON(w, http.StatusOK, map[string]any{
			"message": "API key removed for " + srv.DisplayName,
		})
		return
	}

	if err := secrets.SetSecret(secretKey, body.APIKey); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save secret: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"message": "API key saved for " + srv.DisplayName + ". Restart the daemon for changes to take effect.",
	})
}

// PlatformAdminDeleteWebServiceHandler handles DELETE /api/platform/admin/web-services/{id}.
// Removes the API key for a standard MCP server.
func PlatformAdminDeleteWebServiceHandler(w http.ResponseWriter, r *http.Request) {
	if RequirePlatformAdmin(w, r) == nil {
		return
	}

	secrets := getPlatformSecrets()
	if secrets == nil {
		respondError(w, http.StatusInternalServerError, "platform secrets not available")
		return
	}

	serverID := mux.Vars(r)["id"]
	srv := config.GetStandardServer(serverID)
	if srv == nil {
		respondError(w, http.StatusBadRequest, "unknown web service: "+serverID)
		return
	}

	secretKey := "web_servers." + serverID + ".api_key"
	_ = secrets.RemoveSecret(secretKey)

	respondJSON(w, http.StatusOK, map[string]any{
		"message": "API key removed for " + srv.DisplayName,
	})
}

// --- Helper: Load platform channel config for use by daemon ---

// GetPlatformChannelSettings loads the channel configuration from PlatformSettings.
// Returns nil if not in platform mode or if no channel config exists.
func GetPlatformChannelSettings(ctx context.Context) *store.PlatformChannelSettings {
	backend := getPlatformBackend()
	if backend == nil {
		return nil
	}
	settingsStore := backend.PlatformSettings()
	settings, err := settingsStore.Get(ctx)
	if err != nil || settings == nil {
		return nil
	}
	return settings.Channels
}

// PlatformAdminTestEmailHandler handles POST /api/platform/admin/channels/email/test.
// It verifies the email credential can be resolved and the mailbox is accessible.
// Accepts an optional JSON body with { "secrets": { "key": "value" } } to test
// with unsaved form values (falls back to stored secrets for any missing keys).
func PlatformAdminTestEmailHandler(w http.ResponseWriter, r *http.Request) {
	if RequirePlatformAdmin(w, r) == nil {
		return
	}

	secrets := getPlatformSecrets()
	if secrets == nil {
		respondError(w, http.StatusServiceUnavailable, "secret store not available")
		return
	}

	// Parse optional request body with secret overrides from the form
	var body struct {
		Secrets map[string]string `json:"secrets"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // ignore error — body may be empty

	// Helper: get secret from form override first, then stored value
	getSecret := func(key string) string {
		if v, ok := body.Secrets[key]; ok && v != "" && v != "__CLEAR__" {
			return v
		}
		return secrets.GetSecret(key)
	}

	backend := getPlatformBackend()
	if backend == nil {
		respondError(w, http.StatusServiceUnavailable, "platform backend not available")
		return
	}

	// Load the email config
	settingsStore := backend.PlatformSettings()
	settings, err := settingsStore.Get(r.Context())
	if err != nil || settings == nil || settings.Channels == nil || settings.Channels.Email == nil {
		respondError(w, http.StatusBadRequest, "no email channel configured")
		return
	}

	emailCfg := settings.Channels.Email

	if emailCfg.Provider != "msgraph" {
		// For IMAP/SMTP, just confirm the password secret is configured
		password := getSecret("channels.email.password")
		if password == "" {
			respondJSON(w, http.StatusOK, map[string]any{
				"status":  "error",
				"message": "Email password not configured",
			})
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"message": "IMAP/SMTP credentials are configured (connection not tested)",
		})
		return
	}

	// Microsoft Graph: read secrets from platform secrets (with form overrides) and do token exchange
	tenantID := getSecret("channels.email.tenant_id")
	clientID := getSecret("channels.email.client_id")
	clientSecret := getSecret("channels.email.client_secret") // optional for public clients
	refreshToken := getSecret("channels.email.refresh_token")

	if tenantID == "" || clientID == "" || refreshToken == "" {
		missing := []string{}
		if tenantID == "" {
			missing = append(missing, "Tenant ID")
		}
		if clientID == "" {
			missing = append(missing, "Client ID")
		}
		if refreshToken == "" {
			missing = append(missing, "Refresh Token")
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"status":  "error",
			"message": fmt.Sprintf("Missing secrets: %s", missing),
		})
		return
	}

	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)
	tokenResp, err := email.ExchangeMSGraphToken(r.Context(), tokenURL, clientID, clientSecret, refreshToken)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"status":  "error",
			"message": "Token exchange failed: " + err.Error(),
		})
		return
	}

	// Persist rotated refresh token if Microsoft returned a new one
	if tokenResp.RefreshToken != "" && tokenResp.RefreshToken != refreshToken {
		_ = secrets.SetSecret("channels.email.refresh_token", tokenResp.RefreshToken)
	}

	// Test the Graph API connection with /me endpoint
	req, err := http.NewRequestWithContext(r.Context(), "GET", "https://graph.microsoft.com/v1.0/me", nil)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"status":  "error",
			"message": "Connection failed: " + err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respondJSON(w, http.StatusOK, map[string]any{
			"status":  "error",
			"message": "Graph API returned " + resp.Status,
		})
		return
	}

	// Decode user info for confirmation
	var meResp struct {
		Mail        string `json:"mail"`
		DisplayName string `json:"displayName"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&meResp)

	respondJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"message":     "Connected successfully",
		"email":       meResp.Mail,
		"displayName": meResp.DisplayName,
	})
}

// parseTrustedIssuers parses the trusted_issuers field from the API config map.
func parseTrustedIssuers(v any) []store.PlatformTrustedIssuer {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	var issuers []store.PlatformTrustedIssuer
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		iss := store.PlatformTrustedIssuer{}
		if s, ok := m["name"].(string); ok {
			iss.Name = s
		}
		if s, ok := m["issuer"].(string); ok {
			iss.Issuer = s
		}
		if s, ok := m["jwks_url"].(string); ok {
			iss.JWKSURL = s
		}
		if s, ok := m["audience"].(string); ok {
			iss.Audience = s
		}
		if s, ok := m["user_claim"].(string); ok {
			iss.UserClaim = s
		}
		if iss.Issuer != "" {
			issuers = append(issuers, iss)
		}
	}
	return issuers
}

// parseAllowedAgents parses the allowed_agents field from the API config map.
func parseAllowedAgents(v any) []store.PlatformAllowedAgent {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	var agents []store.PlatformAllowedAgent
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ag := store.PlatformAllowedAgent{}
		if s, ok := m["name"].(string); ok {
			ag.Name = s
		}
		if s, ok := m["actor_sub"].(string); ok {
			ag.ActorSub = s
		}
		if s, ok := m["issuer"].(string); ok {
			ag.Issuer = s
		}
		if rl, ok := m["rate_limit"]; ok {
			ag.RateLimit = toInt(rl)
		}
		if mt, ok := m["max_tasks"]; ok {
			ag.MaxTasks = toInt(mt)
		}
		if ag.ActorSub != "" {
			agents = append(agents, ag)
		}
	}
	return agents
}
