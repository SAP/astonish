package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/flowstore"
	"github.com/SAP/astonish/pkg/mcpstore"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// MCPStoreListResponse is the response for GET /api/mcp-store
type MCPStoreListResponse struct {
	Servers []mcpstore.Server `json:"servers"`
	Sources []string          `json:"sources"` // Available sources for filtering
	Total   int               `json:"total"`
}

// MCPStoreInstallRequest is the request for POST /api/mcp-store/:id/install
type MCPStoreInstallRequest struct {
	ServerName string            `json:"serverName,omitempty"` // Optional: custom name for the server
	Env        map[string]string `json:"env,omitempty"`        // Optional: environment variable overrides
}

// loadAllServersFromTaps loads all MCP servers from taps (including official)
func loadAllServersFromTaps() ([]mcpstore.Server, error) {
	store, err := flowstore.NewStore()
	if err != nil {
		return nil, err
	}

	if err := store.UpdateAllManifests(); err != nil {
		slog.Warn("failed to update MCP manifests", "error", err)
	}
	tappedMCPs := store.ListAllMCPs()

	var inputs []mcpstore.TappedMCPInput
	for _, mcp := range tappedMCPs {
		// Skip MCPs that have neither command nor URL (not installable)
		if mcp.Command == "" && mcp.URL == "" {
			continue
		}
		inputs = append(inputs, mcpstore.TappedMCPInput{
			ID:             mcp.ID,
			Name:           mcp.Name,
			Description:    mcp.Description,
			Author:         mcp.Author,
			GithubUrl:      mcp.GithubUrl,
			GithubStars:    mcp.GithubStars,
			RequiresApiKey: mcp.RequiresApiKey,
			Command:        mcp.Command,
			Args:           mcp.Args,
			Env:            mcp.Env,
			Tags:           mcp.Tags,
			Transport:      mcp.Transport,
			URL:            mcp.URL,
			TapName:        mcp.TapName,
		})
	}

	return mcpstore.ListServers(inputs), nil
}

// ListMCPStoreHandler handles GET /api/mcp-store
// Supports query parameters:
// - ?q=<search query> for text search
// - ?source=<source name> to filter by source (e.g., "official", tap name, or "all")
// Only returns servers with valid configs (installable)
// All MCPs come from tapped repos (including official tap)
func ListMCPStoreHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	sourceFilter := r.URL.Query().Get("source")

	// Load all servers from taps
	servers, err := loadAllServersFromTaps()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load servers: "+err.Error())
		return
	}

	// Apply search filter if specified
	if query != "" {
		servers = mcpstore.SearchServers(servers, query)
	}

	// Collect unique sources for the dropdown
	sourceSet := make(map[string]bool)
	for _, srv := range servers {
		if srv.Source != "" {
			sourceSet[srv.Source] = true
		}
	}
	sources := make([]string, 0, len(sourceSet))
	for source := range sourceSet {
		sources = append(sources, source)
	}

	// Apply source filter if specified (and not "all")
	if sourceFilter != "" && sourceFilter != "all" {
		var filtered []mcpstore.Server
		for _, srv := range servers {
			if srv.Source == sourceFilter {
				filtered = append(filtered, srv)
			}
		}
		servers = filtered
	}

	response := MCPStoreListResponse{
		Servers: servers,
		Sources: sources,
		Total:   len(servers),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// GetMCPStoreServerHandler handles GET /api/mcp-store/{id}
func GetMCPStoreServerHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	// URL decode the ID (it may contain slashes encoded as %2F)
	id = strings.ReplaceAll(id, "%2F", "/")

	// Load all servers from taps
	servers, err := loadAllServersFromTaps()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load servers: "+err.Error())
		return
	}

	// Search by ID first
	server := mcpstore.GetServer(servers, id)
	if server == nil {
		// Try by name
		server = mcpstore.GetServerByName(servers, id)
	}

	if server == nil {
		respondError(w, http.StatusNotFound, "Server not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(server)
}

// InstallMCPStoreServerHandler handles POST /api/mcp-store/{id}/install
// Adds the MCP server configuration to the user's MCP config
func InstallMCPStoreServerHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	// URL decode the ID
	id = strings.ReplaceAll(id, "%2F", "/")

	// Load all servers from taps
	servers, err := loadAllServersFromTaps()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load servers: "+err.Error())
		return
	}

	// Search by ID first
	server := mcpstore.GetServer(servers, id)
	if server == nil {
		// Try by name
		server = mcpstore.GetServerByName(servers, id)
	}

	if server == nil {
		respondError(w, http.StatusNotFound, "Server not found")
		return
	}

	if server.Config == nil {
		respondError(w, http.StatusBadRequest, "Server has no configuration available")
		return
	}

	// Parse request body for optional overrides
	var installReq MCPStoreInstallRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&installReq); err != nil {
			respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}
	}

	// Determine server name
	serverName := strings.ToLower(strings.ReplaceAll(server.Name, " ", "-"))
	if installReq.ServerName != "" {
		serverName = installReq.ServerName
	}

	// Create the server config
	newConfig := config.MCPServerConfig{
		Command:   server.Config.Command,
		Args:      server.Config.Args,
		Transport: "stdio",
	}

	// Merge environment variables
	if server.Config.Env != nil || installReq.Env != nil {
		newConfig.Env = make(map[string]string)
		// Copy from store config
		for k, v := range server.Config.Env {
			newConfig.Env[k] = v
		}
		// Override with request values
		for k, v := range installReq.Env {
			newConfig.Env[k] = v
		}
	}

	newConfig.Env = omitEmptySensitiveMCPEnv(newConfig.Env)

	// Handle transport
	if server.Config.Transport != "" {
		newConfig.Transport = server.Config.Transport
	}
	if server.Config.URL != "" {
		newConfig.URL = server.Config.URL
	}

	// Platform mode: save to DB store, discover tools async
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		// Team-scoped install requires team admin privileges
		if !RequireTeamAdmin(w, r) {
			return
		}
		installMCPStoreServerPlatform(w, r, mcpStore, serverName, newConfig)
		return
	}

	// No MCP store available — platform mode requires the DB store
	respondError(w, http.StatusServiceUnavailable, "MCP server store not available")
}

// installMCPStoreServerPlatform handles MCP Store server installation in platform mode.
// Saves the server config to the DB store and discovers tools synchronously.
func installMCPStoreServerPlatform(w http.ResponseWriter, r *http.Request, mcpStore store.MCPServerStore, serverName string, newConfig config.MCPServerConfig) {
	// Pre-flight: ensure stdio servers can be installed (sandbox must be enabled)
	if err := checkStdioMCPInstallable(newConfig.Transport); err != nil {
		respondError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	userID := effectiveUserID(r)

	s := &store.MCPServer{
		Name:      serverName,
		Command:   newConfig.Command,
		Args:      newConfig.Args,
		Env:       newConfig.Env,
		Transport: newConfig.Transport,
		URL:       newConfig.URL,
		Enabled:   newConfig.Enabled,
		CreatedBy: userID,
	}

	if err := mcpStore.Save(r.Context(), s); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save MCP server: "+err.Error())
		return
	}

	// Discover tools asynchronously — sandbox discovery can take 30-120s.
	runtimeCtx := detachedRuntimeNetworkPolicyContext(r, effectiveAppConfig(r))
	asyncDiscoverAndCacheTools(runtimeCtx, mcpStore, serverName, newConfig, buildPGSessionRegistry(r.Context()))

	// Reset the Studio chat agent so the next request picks up the new MCP server.
	GetChatManager().Reset()

	slog.Info("installed MCP store server (platform)", "server", serverName)

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"status":         "ok",
		"serverName":     serverName,
		"message":        "Server installed successfully",
		"toolsDiscovery": "pending",
	}
	json.NewEncoder(w).Encode(response)
}

// GetMCPStoreTagsHandler handles GET /api/mcp-store/tags
func GetMCPStoreTagsHandler(w http.ResponseWriter, r *http.Request) {
	// Load all servers from taps
	servers, err := loadAllServersFromTaps()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load servers: "+err.Error())
		return
	}

	// Extract unique tags
	tagSet := make(map[string]bool)
	for _, srv := range servers {
		for _, tag := range srv.Tags {
			tagSet[tag] = true
		}
	}
	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tags": tags,
	})
}
