package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// StandardServerResponse represents a standard server in the API response.
type StandardServerResponse struct {
	ID             string                     `json:"id"`
	DisplayName    string                     `json:"displayName"`
	Description    string                     `json:"description"`
	Kind           string                     `json:"kind,omitempty"`
	Category       string                     `json:"category,omitempty"`
	Installed      bool                       `json:"installed"`
	Active         bool                       `json:"active"`
	IsDefault      bool                       `json:"isDefault"`
	EnvVars        []config.StandardEnvVar    `json:"envVars"`
	Capabilities   StandardServerCapabilities `json:"capabilities"`
	WebSearchTool  string                     `json:"webSearchTool,omitempty"`
	WebExtractTool string                     `json:"webExtractTool,omitempty"`
	Details        *StandardServerDetails     `json:"details,omitempty"`
}

// StandardServerCapabilities describes what a standard server can do.
type StandardServerCapabilities struct {
	WebSearch  bool `json:"webSearch"`
	WebExtract bool `json:"webExtract"`
}

// StandardServerDetails holds optional configured-state summary for a standard server card.
type StandardServerDetails struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// ListStandardServersHandler handles GET /api/standard-servers
// Returns all standard servers with their install status.
// Query scope=platform forces platform-level active/installed resolution for web tools.
func ListStandardServersHandler(w http.ResponseWriter, r *http.Request) {
	servers := config.GetStandardServers()
	appCfg := standardServersAppConfig(r)
	activeSearch := appCfg.General.WebSearchTool

	response := make([]StandardServerResponse, 0, len(servers))
	for _, srv := range servers {
		if srv.Category != "web" {
			continue
		}
		installed := config.IsStandardServerInstalled(srv.ID)
		var details *StandardServerDetails
		if srv.Kind == "model" && srv.ID == "perplexity" {
			installed = appCfg.PerplexityWebSearch.Provider != "" && appCfg.PerplexityWebSearch.Model != ""
			if installed {
				details = &StandardServerDetails{
					Provider: appCfg.PerplexityWebSearch.Provider,
					Model:    appCfg.PerplexityWebSearch.Model,
				}
			}
		}
		kind := srv.Kind
		if kind == "" {
			kind = "mcp"
		}
		response = append(response, StandardServerResponse{
			ID:          srv.ID,
			DisplayName: srv.DisplayName,
			Description: srv.Description,
			Kind:        kind,
			Category:    srv.Category,
			Installed:   installed,
			Active:      srv.WebSearchTool != "" && srv.WebSearchTool == activeSearch,
			IsDefault:   srv.IsDefault,
			EnvVars:     srv.EnvVars,
			Capabilities: StandardServerCapabilities{
				WebSearch:  srv.WebSearchTool != "",
				WebExtract: srv.WebExtractTool != "",
			},
			WebSearchTool:  srv.WebSearchTool,
			WebExtractTool: srv.WebExtractTool,
			Details:        details,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"servers":         response,
		"activeWebSearch": activeSearch,
	})
}

// standardServersAppConfig returns the app config used for standard server list status.
// scope=platform reads only platform settings so the Platform MCP tab is not skewed by team overrides.
func standardServersAppConfig(r *http.Request) *config.AppConfig {
	if r.URL.Query().Get("scope") != "platform" {
		return effectiveAppConfig(r)
	}
	svc := store.FromRequest(r)
	if svc == nil || svc.Mode != store.ModePlatform || svc.PlatformSettings == nil {
		return effectiveAppConfig(r)
	}
	return provider.ResolveEffectiveConfig(r.Context(), svc.PlatformSettings, nil, nil)
}

// InstallStandardServerRequest is the request for POST /api/standard-servers/{id}/install
type InstallStandardServerRequest struct {
	Env map[string]string `json:"env"`
}

// ActivateStandardServerHandler handles POST /api/standard-servers/{id}/activate
// Marks an already-configured standard web provider as the active web search tool.
func ActivateStandardServerHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverID := vars["id"]
	scope := r.URL.Query().Get("scope")

	if scope == "platform" && RequirePlatformAdmin(w, r) == nil {
		return
	}

	srv := config.GetStandardServer(serverID)
	if srv == nil {
		respondError(w, http.StatusNotFound, "Unknown standard server: "+serverID)
		return
	}
	if srv.WebSearchTool == "" {
		respondError(w, http.StatusBadRequest, "Server does not provide a web search tool")
		return
	}

	appCfg := standardServersAppConfig(r)

	if srv.Kind == "model" && srv.ID == "perplexity" {
		if appCfg.PerplexityWebSearch.Provider == "" || appCfg.PerplexityWebSearch.Model == "" {
			respondError(w, http.StatusBadRequest, "Configure a Perplexity provider and model first")
			return
		}
	} else if !config.IsStandardServerInstalled(srv.ID) {
		respondError(w, http.StatusBadRequest, "Server is not installed")
		return
	}

	if err := persistActiveWebSearchTools(r, scope, srv.WebSearchTool, srv.WebExtractTool); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	GetChatManager().Reset()
	respondJSON(w, http.StatusOK, map[string]any{
		"status":         "activated",
		"serverName":     srv.ID,
		"webSearchTool":  srv.WebSearchTool,
		"webExtractTool": srv.WebExtractTool,
	})
}

// persistActiveWebSearchTools stores the active web search/extract tool refs.
// scope=platform writes platform settings; otherwise team settings (legacy install path).
func persistActiveWebSearchTools(r *http.Request, scope, webSearchTool, webExtractTool string) error {
	svc := store.FromRequest(r)
	if svc == nil {
		return fmt.Errorf("settings not available")
	}

	if scope == "platform" {
		if svc.PlatformSettings == nil {
			return fmt.Errorf("Platform settings not available")
		}
		settings, err := svc.PlatformSettings.Get(r.Context())
		if err != nil {
			return fmt.Errorf("Failed to load platform settings: %w", err)
		}
		if settings == nil {
			settings = &store.PlatformSettings{}
		}
		settings.WebSearchTool = webSearchTool
		settings.WebExtractTool = webExtractTool
		if err := svc.PlatformSettings.Save(r.Context(), settings); err != nil {
			return fmt.Errorf("Failed to save platform settings: %w", err)
		}
		return nil
	}

	if svc.Settings == nil {
		return fmt.Errorf("Team settings not available")
	}
	teamSettings, err := svc.Settings.Get(r.Context())
	if err != nil {
		return fmt.Errorf("Failed to load team settings: %w", err)
	}
	if teamSettings == nil {
		teamSettings = &store.TeamSettings{}
	}
	teamSettings.WebSearchTool = webSearchTool
	teamSettings.WebExtractTool = webExtractTool
	if err := svc.Settings.Save(r.Context(), teamSettings); err != nil {
		return fmt.Errorf("Failed to save team settings: %w", err)
	}
	return nil
}

// InstallStandardServerHandler handles POST /api/standard-servers/{id}/install
// Installs a standard MCP server, configures web tools, and loads its tools into cache.
func InstallStandardServerHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverID := vars["id"]

	srv := config.GetStandardServer(serverID)
	if srv == nil {
		respondError(w, http.StatusNotFound, "Unknown standard server: "+serverID)
		return
	}

	var req InstallStandardServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if srv.Kind == "model" {
		respondError(w, http.StatusBadRequest, "Model-backed providers are configured from the Perplexity web search settings")
		return
	}

	mcpStore := effectiveMCPStore(r)
	if mcpStore == nil {
		respondError(w, http.StatusInternalServerError, "No MCP store available")
		return
	}
	installStandardServerPlatform(w, r, mcpStore, srv, req)
}

// installStandardServerPlatform handles standard server install in platform mode.
func installStandardServerPlatform(w http.ResponseWriter, r *http.Request, mcpStore store.MCPServerStore, srv *config.StandardMCPServer, req InstallStandardServerRequest) {
	userID := effectiveUserID(r)

	// Build the server config from the standard server definition
	newConfig := config.MCPServerConfig{
		Command:   srv.Command,
		Args:      srv.Args,
		Transport: "stdio",
		Env:       req.Env,
	}

	// Pre-flight: ensure stdio servers can be installed (sandbox must be enabled)
	if err := checkStdioMCPInstallable(newConfig.Transport); err != nil {
		respondError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	s := &store.MCPServer{
		Name:      srv.ID,
		Command:   newConfig.Command,
		Args:      newConfig.Args,
		Env:       newConfig.Env,
		Transport: newConfig.Transport,
		CreatedBy: userID,
	}

	if err := mcpStore.Save(r.Context(), s); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save standard server: "+err.Error())
		return
	}

	// Write the API key to platform_secrets so that IsStandardServerInstalled()
	// and MergeStandardServers() can resolve it via the registered SecretGetter.
	// This is the critical bridge between DB MCP config and the credential resolution path.
	if secrets := getPlatformSecrets(); secrets != nil {
		for _, ev := range srv.EnvVars {
			if val, ok := req.Env[ev.Name]; ok && val != "" {
				storeKey := "web_servers." + srv.ID + ".api_key"
				if err := secrets.SetSecret(storeKey, val); err != nil {
					slog.Warn("failed to write standard server API key to platform_secrets",
						"server", srv.ID, "key", storeKey, "error", err)
				}
				break
			}
		}
	}

	// Persist WebSearchTool/WebExtractTool so effectiveAppConfig() can resolve the
	// active web tool from the database. Platform-scope installs write platform
	// settings; otherwise team settings (legacy/personal platform-mode install).
	if srv.WebSearchTool != "" || srv.WebExtractTool != "" {
		scope := r.URL.Query().Get("scope")
		if err := persistActiveWebSearchTools(r, scope, srv.WebSearchTool, srv.WebExtractTool); err != nil {
			slog.Warn("failed to persist web tool settings on install", "server", srv.ID, "scope", scope, "error", err)
		}
	}

	// Discover tools asynchronously — the sandbox discovery (container creation +
	// MCP server startup + tool listing) can take 30-120s. Running this on the
	// HTTP request path caused timeouts and context-cancellation failures.
	// Tools appear in cached_tools within seconds to minutes after install.
	runtimeCtx := detachedRuntimeNetworkPolicyContext(r, effectiveAppConfig(r))
	asyncDiscoverAndCacheTools(runtimeCtx, mcpStore, srv.ID, newConfig, buildPGSessionRegistry(r.Context()))

	GetChatManager().Reset()

	slog.Info("installed standard server (platform)", "server", srv.ID, "displayName", srv.DisplayName)

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"status":         "installed",
		"serverName":     srv.ID,
		"toolsDiscovery": "pending",
		"webSearchTool":  srv.WebSearchTool,
		"webExtractTool": srv.WebExtractTool,
	}
	json.NewEncoder(w).Encode(response)
}

// UninstallStandardServerHandler handles DELETE /api/standard-servers/{id}
// Removes a standard MCP server's configuration, credentials, and cached tools.
func UninstallStandardServerHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverID := vars["id"]

	srv := config.GetStandardServer(serverID)
	if srv == nil {
		respondError(w, http.StatusNotFound, "Unknown standard server: "+serverID)
		return
	}

	// Keyless MCP servers cannot be uninstalled. Model-backed entries are disabled
	// through their own configuration endpoint.
	if len(srv.EnvVars) == 0 || srv.Kind == "model" {
		respondError(w, http.StatusBadRequest, "Server does not require configuration")
		return
	}

	mcpStore := effectiveMCPStore(r)
	if mcpStore == nil {
		respondError(w, http.StatusServiceUnavailable, "MCP server store not available")
		return
	}

	// Remove from MCP store (DB)
	if err := mcpStore.Delete(r.Context(), serverID); err != nil {
		slog.Warn("failed to delete standard server from store", "server", serverID, "error", err)
	}

	// Remove API key from platform secrets (DB)
	storeKey := "web_servers." + serverID + ".api_key"
	if secrets := getPlatformSecrets(); secrets != nil {
		if err := secrets.RemoveSecret(storeKey); err != nil {
			slog.Warn("failed to remove secret during uninstall", "key", storeKey, "error", err)
		}
	}

	// Also remove from file-based credential store (belt & suspenders: cleans up
	// any legacy key that daemonSecretGetter's fallback would otherwise still resolve).
	if cs := getAPICredentialStore(); cs != nil {
		if err := cs.RemoveSecret(storeKey); err != nil {
			slog.Warn("failed to remove secret from file credential store", "key", storeKey, "error", err)
		}
	}

	// Clear web tool settings if this server provided them
	if srv.WebSearchTool != "" || srv.WebExtractTool != "" {
		if svc := store.FromRequest(r); svc != nil && svc.Settings != nil {
			teamSettings, err := svc.Settings.Get(r.Context())
			if err == nil && teamSettings != nil {
				if teamSettings.WebSearchTool == srv.WebSearchTool {
					teamSettings.WebSearchTool = ""
				}
				if teamSettings.WebExtractTool == srv.WebExtractTool {
					teamSettings.WebExtractTool = ""
				}
				if err := svc.Settings.Save(r.Context(), teamSettings); err != nil {
					slog.Warn("failed to clear team web tool settings", "server", serverID, "error", err)
				}
			}
		}
	}

	// Reset chat agent to pick up removed server
	GetChatManager().Reset()

	slog.Info("uninstalled standard server", "component", "standard-server", "server", srv.ID, "displayName", srv.DisplayName)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "uninstalled",
		"serverName": srv.ID,
	})
}
