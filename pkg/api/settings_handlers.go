package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/anthropic"
	"github.com/SAP/astonish/pkg/provider/google"
	"github.com/SAP/astonish/pkg/provider/groq"
	"github.com/SAP/astonish/pkg/provider/lmstudio"
	"github.com/SAP/astonish/pkg/provider/ollama"
	openai_provider "github.com/SAP/astonish/pkg/provider/openai"
	"github.com/SAP/astonish/pkg/provider/openai_compat"
	"github.com/SAP/astonish/pkg/provider/openrouter"
	"github.com/SAP/astonish/pkg/provider/poe"
	"github.com/SAP/astonish/pkg/provider/sap"
	"github.com/SAP/astonish/pkg/provider/xai"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/version"
	"github.com/gorilla/mux"
)

// --- Package-level credential store for API handlers ---

var (
	apiCredStoreMu sync.RWMutex
	apiCredStore   *credentials.Store
)

// SetAPICredentialStore registers the credential store for use by API handlers.
// Called during startup (daemon/factory).
func SetAPICredentialStore(s *credentials.Store) {
	apiCredStoreMu.Lock()
	defer apiCredStoreMu.Unlock()
	apiCredStore = s
}

// getAPICredentialStore returns the registered credential store (or nil).
func getAPICredentialStore() *credentials.Store {
	apiCredStoreMu.RLock()
	defer apiCredStoreMu.RUnlock()
	return apiCredStore
}

// injectProviderSecrets injects secrets from the credential store into a
// freshly-loaded AppConfig. API handlers load config from disk per request
// (to pick up changes), but the on-disk config has scrubbed API keys after
// credential migration. This re-hydrates them from the encrypted store.
func injectProviderSecrets(appCfg *config.AppConfig) {
	store := getAPICredentialStore()
	if store == nil || appCfg == nil {
		return
	}
	config.InjectProviderSecretsToConfig(appCfg, store.GetSecret)
}

// resolveProviderSecret looks up a provider secret from the credential store,
// falling back to the config map and then to an environment variable.
func resolveProviderSecret(instanceName, cfgKey, envVar string, providerCfg config.ProviderConfig) string {
	// 1. Credential store
	if store := getAPICredentialStore(); store != nil {
		storeKey := "provider." + instanceName + "." + cfgKey
		if val := store.GetSecret(storeKey); val != "" {
			return val
		}
	}
	// 2. Config map
	if val, ok := providerCfg[cfgKey]; ok && val != "" {
		return val
	}
	// 3. Environment variable
	if envVar != "" {
		return os.Getenv(envVar)
	}
	return ""
}

// isProviderConfigured checks if a provider has any secret configured
// (either in the credential store or in the config map).
func isProviderConfigured(instanceName string, providerCfg config.ProviderConfig) bool {
	// Check credential store for any secrets for this provider
	if store := getAPICredentialStore(); store != nil {
		provType := config.GetProviderType(instanceName, providerCfg)
		if provType == "" {
			provType = instanceName
		}
		// Check common secret keys for this provider type
		secretKeys := providerSecretKeys(provType)
		for _, key := range secretKeys {
			storeKey := "provider." + instanceName + "." + key
			if store.GetSecret(storeKey) != "" {
				return true
			}
		}
	}

	// Check config map (non-secret fields like base_url also count as "configured")
	for key, val := range providerCfg {
		if key != "type" && val != "" {
			return true
		}
	}
	return false
}

// providerSecretKeys returns the secret field names for a given provider type.
func providerSecretKeys(provType string) []string {
	switch provType {
	case "sap_ai_core":
		return []string{"client_id", "client_secret", "auth_url"}
	default:
		return []string{"api_key"}
	}
}

// GeneralSettings represents the general app settings
type GeneralSettings struct {
	DefaultProvider            string `json:"default_provider"`
	DefaultProviderDisplayName string `json:"default_provider_display_name"`
	DefaultModel               string `json:"default_model"`
	WebSearchTool              string `json:"web_search_tool"`
	WebExtractTool             string `json:"web_extract_tool"`
	ContextLength              int    `json:"context_length,omitempty"`
}

// ProviderSettings represents a provider's configuration (masked)
type ProviderSettings struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	DisplayName string            `json:"display_name"`
	Configured  bool              `json:"configured"`
	Fields      map[string]string `json:"fields"` // Masked values for display
}

// AppSettingsResponse is the response for GET /api/settings/config
type AppSettingsResponse struct {
	General   GeneralSettings    `json:"general"`
	Providers []ProviderSettings `json:"providers"`
}

// UpdateAppSettingsRequest is the request for PUT /api/settings/config
type UpdateAppSettingsRequest struct {
	General   *GeneralSettings             `json:"general,omitempty"`
	Providers map[string]map[string]string `json:"providers,omitempty"`
}

// GetSettingsHandler handles GET /api/settings/config
func GetSettingsHandler(w http.ResponseWriter, r *http.Request) {
	cfg := effectiveAppConfig(r)

	// Build response with all provider instances
	providers := []ProviderSettings{}

	// List all provider instances (instance names are keys in cfg.Providers)
	for instanceName, instanceConfig := range cfg.Providers {
		// Determine provider type and display name
		providerType := config.GetProviderType(instanceName, instanceConfig)
		displayName := provider.GetProviderDisplayName(providerType)
		if displayName == "" {
			displayName = instanceName
		}

		// Filter out "type" field from display (it's metadata, not a credential)
		fields := make(map[string]string)
		configured := false

		for key, val := range instanceConfig {
			if key != "type" && val != "" {
				fields[key] = val
				configured = true
			}
		}

		// Also check credential store for secret fields not in config
		configured = configured || isProviderConfigured(instanceName, instanceConfig)

		providers = append(providers, ProviderSettings{
			Name:        instanceName,
			Type:        providerType,
			DisplayName: displayName,
			Configured:  configured,
			Fields:      fields,
		})
	}

	response := AppSettingsResponse{
		General: GeneralSettings{
			DefaultProvider:            cfg.General.DefaultProvider,
			DefaultProviderDisplayName: provider.GetProviderDisplayName(cfg.General.DefaultProvider),
			DefaultModel:               cfg.General.DefaultModel,
			WebSearchTool:              cfg.General.WebSearchTool,
			WebExtractTool:             cfg.General.WebExtractTool,
			ContextLength:              cfg.General.ContextLength,
		},
		Providers: providers,
	}

	respondJSON(w, http.StatusOK, response)
}

// UpdateSettingsHandler handles PUT /api/settings/config
func UpdateSettingsHandler(w http.ResponseWriter, r *http.Request) {
	// Team admins (or org admins) can modify settings.
	if !RequireTeamAdmin(w, r) {
		return
	}

	var req UpdateAppSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	svc := store.FromRequest(r)
	if svc == nil || svc.Settings == nil {
		respondError(w, http.StatusServiceUnavailable, "Settings store not available")
		return
	}

	updateSettingsPlatform(w, r, svc, req)
}

// updateSettingsPlatform handles the PUT in platform mode, persisting settings
// to the team's database record instead of the host filesystem.
func updateSettingsPlatform(w http.ResponseWriter, r *http.Request, svc *store.Services, req UpdateAppSettingsRequest) {
	settings, err := svc.Settings.Get(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load team settings: "+err.Error())
		return
	}
	if settings == nil {
		settings = &store.TeamSettings{}
	}

	// Update general settings if provided
	if req.General != nil {
		settings.DefaultProvider = req.General.DefaultProvider
		settings.DefaultModel = req.General.DefaultModel
		settings.WebSearchTool = req.General.WebSearchTool
		settings.WebExtractTool = req.General.WebExtractTool
		if req.General.ContextLength > 0 {
			settings.ContextLength = req.General.ContextLength
		}
	}

	// Update provider settings if provided
	if req.Providers != nil {
		if len(req.Providers) > 0 {
			firstKey := ""
			for k := range req.Providers {
				firstKey = k
				break
			}
			if firstKey == "__replace_all__" {
				var newProviders []map[string]string
				if err := json.Unmarshal([]byte(req.Providers["__replace_all__"]["__array__"]), &newProviders); err == nil {
					newProvidersMap := make(map[string]map[string]string)
					for _, p := range newProviders {
						name := p["name"]
						if name != "" {
							newProvidersMap[name] = make(map[string]string)
							for k, v := range p {
								if k != "name" {
									newProvidersMap[name][k] = v
								}
							}
						}
					}
					settings.Providers = newProvidersMap
				}
			} else {
				if settings.Providers == nil {
					settings.Providers = make(map[string]map[string]string)
				}
				for providerName, providerFields := range req.Providers {
					if settings.Providers[providerName] == nil {
						settings.Providers[providerName] = make(map[string]string)
					}
					for key, value := range providerFields {
						if value != "" && !isMaskedValue(value) {
							settings.Providers[providerName][key] = value
						}
					}
				}
			}
		}
	}

	if err := svc.Settings.Save(r.Context(), settings); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save team settings: "+err.Error())
		return
	}

	cm := GetChatManager()

	// Provider config changes need pool invalidation. General web search/extract
	// selection also requires Reset so the next chat rebuilds tools/prompt with
	// only the selected web search tool (General → Web Tools).
	if req.Providers != nil {
		slog.Info("[settings] team provider configs changed (platform mode), invalidating + reset")
		cm.InvalidateLLMPool()
		cm.Reset()
	} else if req.General != nil {
		slog.Info("[settings] team general settings changed (platform mode), resetting chat",
			"web_search_tool", req.General.WebSearchTool,
			"web_extract_tool", req.General.WebExtractTool,
			"provider", req.General.DefaultProvider,
			"model", req.General.DefaultModel)
		cm.InvalidateLLMPool()
		cm.Reset()
	} else {
		cm.InvalidateLLMPool()
	}

	// Invalidate channel LLM pool so channels pick up the new provider config
	if cm := GetChannelManager(); cm != nil {
		cm.InvalidateLLMPool()
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// isMaskedValue checks if a value is a masked placeholder
func isMaskedValue(val string) bool {
	return len(val) >= 4 && val[:4] == "****"
}

// GetMCPSettingsHandler handles GET /api/settings/mcp
func GetMCPSettingsHandler(w http.ResponseWriter, r *http.Request) {
	// Platform mode: read from DB store (org or team based on ?scope=)
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		servers, err := mcpStore.List(r.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to load MCP servers: "+err.Error())
			return
		}
		// Convert store.MCPServer list to config.MCPConfig map format for frontend
		cfg := config.MCPConfig{MCPServers: make(map[string]config.MCPServerConfig, len(servers))}
		for _, s := range servers {
			cfg.MCPServers[s.Name] = config.MCPServerConfig{
				Command:   s.Command,
				Args:      s.Args,
				Env:       s.Env,
				Transport: s.Transport,
				URL:       s.URL,
				Enabled:   s.Enabled,
			}
		}
		respondJSON(w, http.StatusOK, cfg)
		return
	}

	respondError(w, http.StatusServiceUnavailable, "MCP server store not available")
}

// UpdateMCPSettingsHandler handles PUT /api/settings/mcp
func UpdateMCPSettingsHandler(w http.ResponseWriter, r *http.Request) {
	// Team admins (or org admins) can modify MCP settings.
	if !RequireTeamAdmin(w, r) {
		return
	}

	var newCfg config.MCPConfig
	if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Platform mode: write to DB store
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		updateMCPSettingsPlatform(w, r, mcpStore, newCfg)
		return
	}

	// Personal mode no longer supported
	respondError(w, http.StatusServiceUnavailable, "MCP server store not available")
}

// updateMCPSettingsPlatform handles the PUT in platform mode (DB store).
func updateMCPSettingsPlatform(w http.ResponseWriter, r *http.Request, mcpStore store.MCPServerStore, newCfg config.MCPConfig) {
	userID := effectiveUserID(r)

	// Set default transport to "stdio" if not specified
	for name, server := range newCfg.MCPServers {
		if server.Transport == "" {
			server.Transport = "stdio"
			newCfg.MCPServers[name] = server
		}
	}

	// Pre-flight: ensure all stdio servers can be installed (sandbox must be enabled)
	for name, server := range newCfg.MCPServers {
		if err := checkStdioMCPInstallable(server.Transport); err != nil {
			respondError(w, http.StatusUnprocessableEntity, fmt.Sprintf("server %q: %s", name, err.Error()))
			return
		}
	}

	// Load existing servers from store
	existing, err := mcpStore.List(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load existing MCP servers: "+err.Error())
		return
	}
	existingMap := make(map[string]*store.MCPServer, len(existing))
	for i := range existing {
		existingMap[existing[i].Name] = &existing[i]
	}

	// Delete removed servers
	for name := range existingMap {
		if _, exists := newCfg.MCPServers[name]; !exists {
			if err := mcpStore.Delete(r.Context(), name); err != nil {
				slog.Warn("failed to delete MCP server from store", "name", name, "error", err)
			}
		}
	}

	// Save new/changed servers
	for name, serverCfg := range newCfg.MCPServers {
		s := &store.MCPServer{
			Name:      name,
			Command:   serverCfg.Command,
			Args:      serverCfg.Args,
			Env:       serverCfg.Env,
			Transport: serverCfg.Transport,
			URL:       serverCfg.URL,
			Enabled:   serverCfg.Enabled,
			CreatedBy: userID,
		}
		if err := mcpStore.Save(r.Context(), s); err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to save MCP server: "+err.Error())
			return
		}

		// Trigger async tool discovery for new or changed servers.
		// The detached runtime context carries Astonish Network Policy stores so
		// disposable MCP discovery sandboxes receive Settings allow rules.
		existing := existingMap[name]
		if existing == nil || !mcpServerConfigUnchanged(existing, s) {
			runtimeCtx := detachedRuntimeNetworkPolicyContext(r, effectiveAppConfig(r))
			refreshMCPPlatformServer(runtimeCtx, mcpStore, s, buildPGSessionRegistry(r.Context()))
		}
	}

	// Reset the Studio chat agent so the next request picks up fresh MCP config.
	GetChatManager().Reset()

	// Invalidate channel LLM pool so channels pick up the new provider config
	if cm := GetChannelManager(); cm != nil {
		cm.InvalidateLLMPool()
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// InstallInlineMCPServerRequest is the request for POST /api/mcp/install-inline
type InstallInlineMCPServerRequest struct {
	ServerName string                 `json:"serverName"`
	Config     config.MCPServerConfig `json:"config"`
}

// InstallInlineMCPServerHandler handles POST /api/mcp/install-inline
// Adds an inline MCP server configuration to the user's MCP config
func InstallInlineMCPServerHandler(w http.ResponseWriter, r *http.Request) {
	var req InstallInlineMCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.ServerName == "" {
		respondError(w, http.StatusBadRequest, "Server name is required")
		return
	}

	if req.Config.Command == "" && req.Config.URL == "" {
		respondError(w, http.StatusBadRequest, "Server config must have either command or URL")
		return
	}

	// Set default transport
	if req.Config.Transport == "" {
		req.Config.Transport = "stdio"
	}
	req.Config.Env = omitEmptySensitiveMCPEnv(req.Config.Env)

	// Platform mode: save directly to DB store
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		// Pre-flight: ensure stdio servers can be installed (sandbox must be enabled)
		if err := checkStdioMCPInstallable(req.Config.Transport); err != nil {
			respondError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}

		// Team-scoped install requires team admin privileges
		if !RequireTeamAdmin(w, r) {
			return
		}
		userID := effectiveUserID(r)
		s := &store.MCPServer{
			Name:      req.ServerName,
			Command:   req.Config.Command,
			Args:      req.Config.Args,
			Env:       req.Config.Env,
			Transport: req.Config.Transport,
			URL:       req.Config.URL,
			Enabled:   req.Config.Enabled,
			CreatedBy: userID,
		}
		if err := mcpStore.Save(r.Context(), s); err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to save MCP server: "+err.Error())
			return
		}

		// Discover tools asynchronously with timeout.
		runtimeCtx := detachedRuntimeNetworkPolicyContext(r, effectiveAppConfig(r))
		asyncDiscoverAndCacheTools(runtimeCtx, mcpStore, req.ServerName, req.Config, buildPGSessionRegistry(r.Context()))

		GetChatManager().Reset()

		// Invalidate channel LLM pool so channels pick up the new config
		if cm := GetChannelManager(); cm != nil {
			cm.InvalidateLLMPool()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":     "installed",
			"serverName": req.ServerName,
		})
		return
	}

	// Personal mode no longer supported
	respondError(w, http.StatusServiceUnavailable, "MCP server store not available")
}

// ListProviderModelsHandler handles GET /api/providers/{providerId}/models
func ListProviderModelsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	providerID := vars["providerId"]

	cfg := effectiveAppConfig(r)

	// Resolve secrets from credential store into the provider config so that
	// ListModelsForProvider sees the actual API key (secrets are scrubbed from
	// the YAML config file and stored in the credential store).
	if instance, exists := cfg.Providers[providerID]; exists {
		providerType := config.GetProviderType(providerID, instance)
		if providerType == "" {
			providerType = providerID
		}
		for _, key := range providerSecretKeys(providerType) {
			resolved := resolveProviderSecret(providerID, key, "", instance)
			if resolved != "" {
				instance[key] = resolved
			}
		}
	}

	models, err := provider.ListModelsForProvider(r.Context(), providerID, cfg)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"provider": providerID,
		"models":   models,
	})
}

// SetupStatusResponse represents the setup status for the wizard
type SetupStatusResponse struct {
	SetupRequired       bool     `json:"setupRequired"`
	HasDefaultProvider  bool     `json:"hasDefaultProvider"`
	HasDefaultModel     bool     `json:"hasDefaultModel"`
	ConfiguredProviders []string `json:"configuredProviders"`
}

// GetSetupStatusHandler handles GET /api/settings/status
func GetSetupStatusHandler(w http.ResponseWriter, r *http.Request) {
	cfg := effectiveAppConfig(r)

	// Check for configured providers
	// For multi-instance support, check all provider instances in config
	var configuredProviders []string

	for instanceName, providerCfg := range cfg.Providers {
		if providerCfg == nil {
			continue
		}

		// Check if this provider has a valid type
		providerType := config.GetProviderType(instanceName, providerCfg)
		if providerType == "" {
			continue
		}

		// Check if provider is configured (config map + credential store)
		if isProviderConfigured(instanceName, providerCfg) {
			configuredProviders = append(configuredProviders, instanceName)
		}
	}

	hasDefaultProvider := cfg.General.DefaultProvider != ""
	hasDefaultModel := cfg.General.DefaultModel != ""

	// Setup is required only if there are no configured providers at all.
	// Having a default_provider explicitly set is a convenience, not a prerequisite.
	setupRequired := len(configuredProviders) == 0

	// In platform mode, the platform is already bootstrapped (DB connected,
	// user authenticated). The setup wizard is not needed — providers can be
	// configured through Settings at platform/org/team level.
	svc := store.FromRequest(r)
	if svc != nil && svc.Mode == store.ModePlatform {
		setupRequired = false
	}

	respondJSON(w, http.StatusOK, SetupStatusResponse{
		SetupRequired:       setupRequired,
		HasDefaultProvider:  hasDefaultProvider,
		HasDefaultModel:     hasDefaultModel,
		ConfiguredProviders: configuredProviders,
	})
}

// ListProviderModelsWithMetadataHandler handles GET /api/providers/{providerId}/models-metadata
// Returns enhanced model information with pricing for OpenRouter, basic info for others
func ListProviderModelsWithMetadataHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	providerID := vars["providerId"]

	cfg := effectiveAppConfig(r)

	// Resolve provider instance config and type. The providerID is the instance
	// name (e.g. "SAP AI Core"), not necessarily the type slug ("sap_ai_core").
	pCfg := cfg.Providers[providerID]
	if pCfg == nil {
		pCfg = make(config.ProviderConfig)
	}
	providerType := config.GetProviderType(providerID, pCfg)

	switch providerType {
	case "openrouter":
		apiKey := resolveProviderSecret(providerID, "api_key", "OPENROUTER_API_KEY", pCfg)
		models, err := openrouter.ListModelsWithMetadata(r.Context(), apiKey)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": true, "models": models})
		return

	case "anthropic":
		apiKey := resolveProviderSecret(providerID, "api_key", "ANTHROPIC_API_KEY", pCfg)
		models, err := anthropic.ListModelsWithMetadata(r.Context(), apiKey)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": true, "models": models})
		return

	case "gemini":
		apiKey := resolveProviderSecret(providerID, "api_key", "GOOGLE_API_KEY", pCfg)
		models, err := google.ListModelsWithMetadata(r.Context(), apiKey)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": true, "models": models})
		return

	case "groq":
		apiKey := resolveProviderSecret(providerID, "api_key", "GROQ_API_KEY", pCfg)
		models, err := groq.ListModelsWithMetadata(r.Context(), apiKey)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": true, "models": models})
		return

	case "openai":
		apiKey := resolveProviderSecret(providerID, "api_key", "OPENAI_API_KEY", pCfg)
		models, err := openai_provider.ListModelsWithMetadata(r.Context(), apiKey)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": true, "models": models})
		return

	case "poe":
		apiKey := resolveProviderSecret(providerID, "api_key", "POE_API_KEY", pCfg)
		models, err := poe.ListModelsWithMetadata(r.Context(), apiKey)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": true, "models": models})
		return

	case "sap_ai_core":
		clientID := resolveProviderSecret(providerID, "client_id", "AICORE_CLIENT_ID", pCfg)
		clientSecret := resolveProviderSecret(providerID, "client_secret", "AICORE_CLIENT_SECRET", pCfg)
		authURL := resolveProviderSecret(providerID, "auth_url", "AICORE_AUTH_URL", pCfg)
		baseURL := pCfg["base_url"]
		if baseURL == "" {
			baseURL = os.Getenv("AICORE_BASE_URL")
		}
		resourceGroup := pCfg["resource_group"]
		if resourceGroup == "" {
			resourceGroup = os.Getenv("AICORE_RESOURCE_GROUP")
		}
		models, err := sap.ListModelsWithMetadata(r.Context(), clientID, clientSecret, authURL, baseURL, resourceGroup)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": true, "models": models})
		return

	case "xai":
		apiKey := resolveProviderSecret(providerID, "api_key", "XAI_API_KEY", pCfg)
		models, err := xai.ListModelsWithMetadata(r.Context(), apiKey)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": true, "models": models})
		return

	case "lm_studio":
		baseURL := pCfg["base_url"]
		models, err := lmstudio.ListModelsWithMetadata(r.Context(), baseURL)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": true, "models": models})
		return

	case "ollama":
		baseURL := pCfg["base_url"]
		models, err := ollama.ListModelsWithMetadata(r.Context(), baseURL)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": true, "models": models})
		return

	case "openai_compat":
		apiKey := resolveProviderSecret(providerID, "api_key", "", pCfg)
		baseURL := pCfg["base_url"]
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		models, err := openai_compat.ListModels(r.Context(), apiKey, baseURL)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
			return
		}
		var modelInfos []map[string]interface{}
		for _, m := range models {
			modelInfos = append(modelInfos, map[string]interface{}{"id": m, "name": m})
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": providerID, "has_metadata": false, "models": modelInfos})
		return
	}

	// For other providers, return basic model list wrapped as ModelInfo
	models, err := provider.ListModelsForProvider(r.Context(), providerID, cfg)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fetch models: "+err.Error())
		return
	}

	// Convert string list to basic ModelInfo format
	var modelInfos []map[string]interface{}
	for _, m := range models {
		modelInfos = append(modelInfos, map[string]interface{}{
			"id":   m,
			"name": m,
		})
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"provider":     providerID,
		"has_metadata": false,
		"models":       modelInfos,
	})
}

// VersionResponse represents the version API response
type VersionResponse struct {
	Version string `json:"version"`
}

// GetVersionHandler handles GET /api/version
func GetVersionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	v := version.GetVersion()
	json.NewEncoder(w).Encode(VersionResponse{
		Version: v,
	})
}
