package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/gorilla/mux"
	"google.golang.org/adk/tool/mcptoolset"
)

// mcpDiscoveryTimeout is the maximum duration for MCP tool discovery.
// This covers container creation + wait-for-ready + MCP server startup +
// tool listing. Generous to account for npm package downloads on first use.
const mcpDiscoveryTimeout = 5 * time.Minute

// discoveryInFlight tracks servers currently being discovered to prevent
// concurrent duplicate discovery goroutines within the same process. When
// multiple API pods or concurrent HTTP requests trigger discovery for the
// same server, only the first goroutine runs; subsequent calls are skipped.
var discoveryInFlight sync.Map

// asyncDiscoverAndCacheTools runs MCP tool discovery in a background goroutine
// with a dedicated timeout (not tied to any HTTP request lifecycle). On success,
// it writes the discovered tools to the DB via mcpStore.UpdateCachedTools.
//
// This is the standard pattern for all install and refresh paths. Callers save
// the server config to the DB first, then fire this async. The HTTP response
// returns immediately — tools appear in cached_tools within seconds to minutes.
//
// Discovery is deduplicated per server name: if a goroutine is already running
// for the given server, subsequent calls are no-ops.
//
// sessRegistry may be nil; when non-nil, the sandbox backend uses it for
// cross-replica session consistency (platform mode with PG-backed store).
// Callers should build it from the HTTP request context BEFORE spawning the
// goroutine (the request context is unavailable inside the goroutine).
func asyncDiscoverAndCacheTools(mcpStore store.MCPServerStore, serverName string, serverCfg config.MCPServerConfig, sessRegistry *sandbox.SessionRegistry) {
	if _, loaded := discoveryInFlight.LoadOrStore(serverName, struct{}{}); loaded {
		slog.Info("MCP discovery already in progress, skipping duplicate", "server", serverName)
		return
	}
	go func() {
		defer discoveryInFlight.Delete(serverName)

		ctx, cancel := context.WithTimeout(context.Background(), mcpDiscoveryTimeout)
		defer cancel()

		servers := map[string]config.MCPServerConfig{serverName: serverCfg}
		discoveredTools := discoverMCPToolsForPlatform(ctx, serverName, servers, sessRegistry)
		if discoveredTools == nil {
			slog.Warn("async MCP discovery: no tools discovered", mcp.ServerConfigLogAttrs(serverName, serverCfg)...)
			return
		}

		if err := mcpStore.UpdateCachedTools(ctx, serverName, discoveredTools); err != nil {
			slog.Warn("async MCP discovery: failed to cache tools", "server", serverName, "error", err)
			return
		}

		var toolList []json.RawMessage
		if json.Unmarshal(discoveredTools, &toolList) == nil {
			slog.Info("async MCP discovery: tools cached", "server", serverName, "count", len(toolList))
		}

		// Reset the chat agent so the next session picks up the newly-discovered tools.
		GetChatManager().Reset()
	}()
}

// GetServerStatus returns the status of all MCP servers
func GetServerStatus() []cache.ServerStatus {
	statusesMap := cache.GetServerStatuses()

	statuses := make([]cache.ServerStatus, 0, len(statusesMap))
	for _, status := range statusesMap {
		statuses = append(statuses, status)
	}
	return statuses
}

// SetServerStatus updates the status of a specific MCP server
func SetServerStatus(name string, status cache.ServerStatus) {
	cache.UpdateServerStatus(status)
	// Persist the cache
	if err := cache.SaveCache(); err != nil {
		slog.Warn("failed to save tools cache after status update", "error", err)
	}
}

// ClearServerStatus removes a server from the status map
// Note: This only clears status, not tools. For full removal use cache.RemoveServer
func ClearServerStatus(name string) {
	// We don't have a direct method to clear just status in cache yet,
	// but we can set it to a zero value or "loading" if needed.
	// For now, let's just leave it as this function might not be used or we can implement explicit clear if needed.
	// Actually, let's implement a way to clear status if really needed, but it's mostly used for cleanup.
}

// MCPStatusResponse is the response for GET /api/mcp/status
type MCPStatusResponse struct {
	Servers []cache.ServerStatus `json:"servers"`
}

// MCPStatusHandler handles GET /api/mcp/status
func MCPStatusHandler(w http.ResponseWriter, r *http.Request) {
	// Platform mode: return status from DB store
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		servers, err := mcpStore.List(r.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to load MCP servers: "+err.Error())
			return
		}
		statuses := make([]cache.ServerStatus, 0, len(servers))
		for _, s := range servers {
			toolCount := 0
			if s.CachedTools != nil && len(s.CachedTools) > 2 { // not empty "[]" or "null"
				// Count tools from cached_tools JSON array
				var tools []json.RawMessage
				if json.Unmarshal(s.CachedTools, &tools) == nil {
					toolCount = len(tools)
				}
			}

			status := "configured"
			if !s.IsEnabled() {
				status = "disabled"
			} else if toolCount > 0 {
				status = "healthy"
			}

			statuses = append(statuses, cache.ServerStatus{
				Name:      s.Name,
				Status:    status,
				ToolCount: toolCount,
			})
		}
		response := MCPStatusResponse{Servers: statuses}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// No store available — return empty status
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"servers": []interface{}{}})
}

// InitToolsCache is a no-op retained for backward compatibility.
// In platform mode, tools are loaded per-request from the DB via GetCachedToolsForRequest.
func InitToolsCache(ctx context.Context) {}

// RefreshMCPServerHandler handles POST /api/mcp/{server}/refresh
func RefreshMCPServerHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["name"] // "name" to match definition in handlers.go
	if serverName == "" {
		respondError(w, http.StatusBadRequest, "Server name is required")
		return
	}

	// Platform mode: refresh by starting the MCP process and discovering tools
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		server, err := mcpStore.Get(r.Context(), serverName)
		if err != nil || server == nil {
			respondError(w, http.StatusNotFound, "Server not found")
			return
		}

		// Build a config.MCPServerConfig and use the bridge to discover tools
		serverCfg := config.MCPServerConfig{
			Command:   server.Command,
			Args:      server.Args,
			Env:       server.Env,
			Transport: server.Transport,
			URL:       server.URL,
			Enabled:   server.Enabled,
		}

		// Run tool discovery asynchronously with a dedicated timeout
		asyncDiscoverAndCacheTools(mcpStore, serverName, serverCfg, buildPGSessionRegistry(r.Context()))

		respondJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	// No MCP store available — platform mode requires the DB store
	respondJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
		"success": false,
		"error":   "MCP server store not available",
	})
}

// checkStdioMCPInstallable verifies that a stdio-transport MCP server can be
// installed. Stdio servers require sandbox mode to be enabled because their
// runtimes (npx, node, uv, python) only exist inside the sandbox overlay.
// SSE/remote servers always pass this check.
//
// Call this BEFORE saving the server config to the DB. Returns nil if the
// server can be installed, or an error describing why not.
func checkStdioMCPInstallable(transport string) error {
	t := strings.ToLower(transport)
	if t == "sse" || t == "streamable-http" {
		return nil // network-based, no sandbox needed
	}

	// Stdio: requires sandbox
	appCfg, err := config.LoadAppConfig()
	if err != nil || appCfg == nil {
		return fmt.Errorf("cannot install stdio MCP server: failed to load app config")
	}
	if !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		return fmt.Errorf("cannot install stdio MCP server: sandbox mode is not enabled (stdio servers require sandbox because their runtimes like npx/node/uv only exist inside the sandbox overlay)")
	}
	return nil
}

// discoverMCPToolsForPlatform discovers an MCP server's tools and returns
// them as a JSON array. The strategy depends on transport type and sandbox
// configuration:
//
//   - SSE/remote transport: connect directly over the network (no sandbox needed).
//   - Stdio transport + sandbox enabled: spin up a temporary sandbox container,
//     start the MCP server inside it, discover tools, then destroy the container.
//   - Stdio transport + sandbox disabled: error — stdio servers cannot be
//     installed without sandbox (they require npx/node/uv which live inside
//     the sandbox).
//
// Returns nil on error (logged). On hard failures (sandbox unavailable),
// returns nil and logs a warning; callers surface this as a toolError.
func discoverMCPToolsForPlatform(ctx context.Context, serverName string, servers map[string]config.MCPServerConfig, sessRegistry *sandbox.SessionRegistry) json.RawMessage {
	serverCfg, ok := servers[serverName]
	if !ok {
		slog.Warn("MCP discovery: server config not found", "component", "mcp", "server", serverName)
		return nil
	}

	// SSE/streamable-http servers: connect over the network (no sandbox)
	transport := strings.ToLower(serverCfg.Transport)
	if transport == "sse" || transport == "streamable-http" {
		return discoverMCPToolsOnHost(ctx, serverName, servers)
	}

	// Stdio servers: must run in sandbox
	data, err := discoverMCPToolsInSandbox(ctx, serverName, serverCfg, sessRegistry)
	if err != nil {
		slog.Warn("MCP sandbox discovery failed", mcp.FailureLogAttrs(serverName, serverCfg, err, nil)...)
		return nil
	}
	return data
}

// discoverMCPToolsInSandbox spins up a temporary sandbox container, starts
// the MCP server inside it, lists its tools, then destroys the container.
// This is the correct discovery path for stdio-based MCP servers when sandbox
// is enabled (npx, node, uv, etc. only exist inside the sandbox overlay).
//
// The flow mirrors invokeMCPToolInContainer but uses a disposable session
// (not per-user, not tracked by the idle watchdog). The container is always
// destroyed on return regardless of success/failure.
func discoverMCPToolsInSandbox(ctx context.Context, serverName string, serverCfg config.MCPServerConfig, sessRegistry *sandbox.SessionRegistry) (json.RawMessage, error) {
	// Check sandbox availability
	appCfg, err := config.LoadAppConfig()
	if err != nil || appCfg == nil {
		return nil, fmt.Errorf("cannot load app config: %w", err)
	}
	if !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		return nil, fmt.Errorf("sandbox is not enabled — stdio MCP servers require sandbox for tool discovery (npx/node/uv are only available inside the sandbox)")
	}

	// Use PG-backed session registry when available (platform mode) for
	// cross-replica consistency. Falls back to local JSON if nil.
	backend, cleanup, err := sandbox.BackendFromAppConfigWithSessions(appCfg, sessRegistry)
	if err != nil {
		return nil, fmt.Errorf("sandbox backend unavailable: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Resolve base layer chain so the container has Node.js, Python, etc.
	// installed via "Configure Base".
	layerChain := resolveBaseLayerChain(ctx)

	// Include the top layer hash in the session ID so that a stale pod
	// (created before Configure Base installed Node.js) is never reused.
	// K8s CreateSession is idempotent: if a pod with the same name exists
	// it is reused regardless of its layer chain. By incorporating the
	// layer hash, a new Configure Base build naturally creates a different
	// pod name, bypassing any stale/terminating pod.
	sessionID := "mcp-discover-" + serverName
	if len(layerChain) > 0 {
		topLayer := layerChain[len(layerChain)-1]
		if len(topLayer) > 8 {
			topLayer = topLayer[:8]
		}
		sessionID += "-" + topLayer
	}

	// Best-effort destroy of any existing pod with this ID (e.g. leftover
	// from a prior discovery attempt that was interrupted).
	_ = backend.DestroySession(ctx, sessionID)

	_, err = backend.CreateSession(ctx, sandbox.SessionSpec{
		SessionID:  sessionID,
		Type:       sandbox.SessionTypeChat,
		TemplateID: sandbox.BaseTemplateID,
		LayerChain: layerChain,
		Labels:     map[string]string{"purpose": "mcp-discovery"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery session: %w", err)
	}

	// Always destroy the temporary container on exit
	defer func() {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := backend.DestroySession(destroyCtx, sessionID); err != nil {
			slog.Warn("failed to destroy MCP discovery session", "session", sessionID, "error", err)
		}
	}()

	// Wait for the container/pod to be running
	if err := backend.WaitForSessionReady(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("discovery session not ready: %w", err)
	}

	// Create the backend-agnostic MCP transport
	transport, stderrBuf := sandbox.NewBackendMCPTransport(backend, sessionID, serverCfg)
	defer transport.Close()

	// Connect via ADK mcptoolset (same pattern as invokeMCPToolInContainer)
	toolset, err := mcptoolset.New(mcptoolset.Config{
		Transport: transport,
	})
	if err != nil {
		stderrStr := stderrBuf.String()
		return nil, fmt.Errorf("failed to start MCP server %q in sandbox: %w (stderr: %s)", serverName, err, stderrStr)
	}

	// List tools
	toolCtx := &minimalReadonlyContext{Context: ctx}
	mcpTools, err := toolset.Tools(toolCtx)
	if err != nil {
		stderrStr := stderrBuf.String()
		return nil, fmt.Errorf("failed to list tools from MCP server %q: %w (stderr: %s)", serverName, err, stderrStr)
	}

	// Marshal tool declarations to JSON (same format as discoverMCPToolsOnHost)
	type toolEntry struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	}
	toolEntries := make([]toolEntry, 0, len(mcpTools))
	for _, t := range mcpTools {
		toolEntries = append(toolEntries, toolEntry{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: common.ExtractToolInputSchema(t),
		})
	}

	data, err := json.Marshal(toolEntries)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal discovered tools: %w", err)
	}

	slog.Info("MCP sandbox discovery: discovered tools", "server", serverName, "count", len(toolEntries))
	return data, nil
}

// discoverMCPToolsOnHost runs MCP tool discovery on the host (no sandbox).
// Used for SSE/streamable-http transport servers that connect over the network.
func discoverMCPToolsOnHost(ctx context.Context, serverName string, servers map[string]config.MCPServerConfig) json.RawMessage {
	cfg := &config.MCPConfig{MCPServers: servers}
	mgr := mcp.NewManagerFromConfig(cfg)
	defer mgr.Cleanup()

	if err := mgr.InitializeToolsets(ctx); err != nil {
		slog.Warn("platform MCP refresh: failed to initialize", mcp.FailureLogAttrs(serverName, servers[serverName], err, nil)...)
		return nil
	}

	// Get the tool declarations from the named toolsets
	type toolEntry struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	}
	var toolEntries []toolEntry

	minimalCtx := &minimalReadonlyContext{Context: ctx}
	for _, namedToolset := range mgr.GetNamedToolsets() {
		if namedToolset.Name != serverName {
			continue
		}
		mcpTools, err := namedToolset.Toolset.Tools(minimalCtx)
		if err != nil {
			slog.Warn("platform MCP refresh: failed to get tools", mcp.FailureLogAttrs(serverName, servers[serverName], err, namedToolset.Stderr)...)
			return nil
		}
		for _, t := range mcpTools {
			toolEntries = append(toolEntries, toolEntry{
				Name:        t.Name(),
				Description: t.Description(),
				InputSchema: common.ExtractToolInputSchema(t),
			})
		}
	}

	if toolEntries == nil {
		toolEntries = []toolEntry{} // ensure valid JSON "[]"
	}

	data, err := json.Marshal(toolEntries)
	if err != nil {
		slog.Warn("platform MCP refresh: failed to marshal tools", "server", serverName, "error", err)
		return nil
	}

	slog.Info("platform MCP refresh: discovered tools (host)", "server", serverName, "count", len(toolEntries))
	return data
}

// GetCachedToolsForRequest returns the cached tools appropriate for the request context.
// In platform mode, it reads from the org+team DB stores' CachedTools.
func GetCachedToolsForRequest(r *http.Request) []ToolInfo {
	svc := store.FromRequest(r)
	if svc == nil || svc.Mode != store.ModePlatform {
		return nil
	}

	// Platform mode: build merged tool list from DB stores. Cascade is
	// platform → org → team, with later tiers overriding earlier on name.
	// Team-disabled servers explicitly delete the entry so a team can hide a
	// platform/org-tier server it doesn't want exposed.
	type toolEntry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	// Collect servers: platform → org → team
	serverMap := make(map[string]json.RawMessage) // name -> cachedTools
	if svc.PlatformMCPServers != nil {
		platformServers, err := svc.PlatformMCPServers.List(r.Context())
		if err == nil {
			for _, s := range platformServers {
				if s.IsEnabled() && len(s.CachedTools) > 0 {
					serverMap[s.Name] = s.CachedTools
				}
			}
		}
	}
	if svc.MCPServers != nil {
		orgServers, err := svc.MCPServers.List(r.Context())
		if err == nil {
			for _, s := range orgServers {
				if s.IsEnabled() && len(s.CachedTools) > 0 {
					serverMap[s.Name] = s.CachedTools
				} else if !s.IsEnabled() {
					// Org disabled server overrides platform enabled
					delete(serverMap, s.Name)
				}
			}
		}
	}
	if svc.TeamMCPServers != nil {
		teamServers, err := svc.TeamMCPServers.List(r.Context())
		if err == nil {
			for _, s := range teamServers {
				if s.IsEnabled() && len(s.CachedTools) > 0 {
					serverMap[s.Name] = s.CachedTools
				} else {
					// Team disabled server overrides org+platform enabled
					delete(serverMap, s.Name)
				}
			}
		}
	}

	var result []ToolInfo
	// Add all built-in tools (internal + all category tools)
	for _, decl := range tools.GetAllFlowToolDeclarations() {
		result = append(result, ToolInfo{
			Name:        decl.Name,
			Description: decl.Description,
			Source:      decl.Category,
		})
	}

	// Parse each server's cached tools from DB
	for serverName, cachedData := range serverMap {
		var entries []toolEntry
		if json.Unmarshal(cachedData, &entries) == nil {
			for _, e := range entries {
				result = append(result, ToolInfo{
					Name:        e.Name,
					Description: e.Description,
					Source:      serverName,
				})
			}
		}
	}

	// Include installed standard servers (Tavily, Brave, Firecrawl, etc.)
	// that are NOT already in the DB stores. These are configured via the
	// filesystem credential store + config.yaml and may not have been
	// explicitly installed into the team DB.
	for _, srv := range config.GetStandardServers() {
		if serverMap[srv.ID] != nil {
			continue // Already in DB stores — skip
		}
		if !config.IsStandardServerInstalled(srv.ID) {
			continue // Not installed — skip
		}
		// Try to get tool info from the persistent file-based cache
		cachedEntries := cache.GetToolsForServer(srv.ID)
		if len(cachedEntries) > 0 {
			for _, e := range cachedEntries {
				result = append(result, ToolInfo{
					Name:        e.Name,
					Description: e.Description,
					Source:      srv.ID,
				})
			}
		} else {
			// Fallback: use the known web tool names from the standard server definition
			if srv.WebSearchTool != "" {
				if parts := strings.SplitN(srv.WebSearchTool, ":", 2); len(parts) == 2 {
					result = append(result, ToolInfo{
						Name:        parts[1],
						Description: srv.DisplayName + " web search",
						Source:      srv.ID,
					})
				}
			}
			if srv.WebExtractTool != "" {
				if parts := strings.SplitN(srv.WebExtractTool, ":", 2); len(parts) == 2 {
					result = append(result, ToolInfo{
						Name:        parts[1],
						Description: srv.DisplayName + " content extraction",
						Source:      srv.ID,
					})
				}
			}
		}
	}

	return result
}
