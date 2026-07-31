package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// StandardServerResponse represents a standard server in the API response.
type StandardServerResponse struct {
	ID             string                     `json:"id"`
	DisplayName    string                     `json:"displayName"`
	Description    string                     `json:"description"`
	Installed      bool                       `json:"installed"`
	IsDefault      bool                       `json:"isDefault"`
	EnvVars        []config.StandardEnvVar    `json:"envVars"`
	Capabilities   StandardServerCapabilities `json:"capabilities"`
	WebSearchTool  string                     `json:"webSearchTool,omitempty"`
	WebExtractTool string                     `json:"webExtractTool,omitempty"`
}

// StandardServerCapabilities describes what a standard server can do.
type StandardServerCapabilities struct {
	WebSearch  bool `json:"webSearch"`
	WebExtract bool `json:"webExtract"`
}

// ListStandardServersHandler handles GET /api/standard-servers
// Returns all standard servers with their install status.
func ListStandardServersHandler(w http.ResponseWriter, r *http.Request) {
	servers := config.GetStandardServers()

	response := make([]StandardServerResponse, 0, len(servers))
	for _, srv := range servers {
		response = append(response, StandardServerResponse{
			ID:          srv.ID,
			DisplayName: srv.DisplayName,
			Description: srv.Description,
			Installed:   config.IsStandardServerInstalled(srv.ID),
			IsDefault:   srv.IsDefault,
			EnvVars:     srv.EnvVars,
			Capabilities: StandardServerCapabilities{
				WebSearch:  srv.WebSearchTool != "",
				WebExtract: srv.WebExtractTool != "",
			},
			WebSearchTool:  srv.WebSearchTool,
			WebExtractTool: srv.WebExtractTool,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"servers": response,
	})
}

// InstallStandardServerRequest is the request for POST /api/standard-servers/{id}/install
type InstallStandardServerRequest struct {
	Env map[string]string `json:"env"`
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

	// Persist WebSearchTool/WebExtractTool in the team settings so that
	// effectiveAppConfig() and MergeStandardServersWithConfig() can resolve
	// the active web tool from the database in platform mode (not config.yaml).
	if srv.WebSearchTool != "" || srv.WebExtractTool != "" {
		if svc := store.FromRequest(r); svc != nil && svc.Settings != nil {
			teamSettings, err := svc.Settings.Get(r.Context())
			if err != nil {
				slog.Warn("failed to read team settings for web tool update", "server", srv.ID, "error", err)
			} else {
				if teamSettings == nil {
					teamSettings = &store.TeamSettings{}
				}
				if srv.WebSearchTool != "" {
					teamSettings.WebSearchTool = srv.WebSearchTool
				}
				if srv.WebExtractTool != "" {
					teamSettings.WebExtractTool = srv.WebExtractTool
				}
				if err := svc.Settings.Save(r.Context(), teamSettings); err != nil {
					slog.Warn("failed to save team settings with web tool", "server", srv.ID, "error", err)
				}
			}
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

	// Keyless servers cannot be uninstalled
	if len(srv.EnvVars) == 0 {
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
