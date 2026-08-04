package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

// buildRequestMCPToolGroups builds LazyMCP tool groups from the request-scoped
// MCP server stores (platform → org → team) plus any file-cache fallback.
//
// The Studio chat agent is a multi-tenant singleton pre-warmed with a default
// team context. Team-installed MCP servers (and tools discovered after pre-warm)
// are invisible to that singleton's ToolIndex. These groups are attached to the
// per-request runner context so search_tools / dynamic injection / resolveTools
// see the caller's actual MCP catalog.
func buildRequestMCPToolGroups(r *http.Request, pool sandbox.ToolNodePool, debug bool) map[string]*agent.ToolGroup {
	if r == nil {
		return nil
	}
	mcpCfg := loadMCPConfigForRequest(r)
	if mcpCfg == nil || len(mcpCfg.MCPServers) == 0 {
		return nil
	}

	resolver := credentialResolverForRequest(r)
	groups := make(map[string]*agent.ToolGroup)
	var skippedNoCache []string

	for name, serverCfg := range mcpCfg.MCPServers {
		if !serverCfg.IsEnabled() {
			continue
		}
		cachedTools := cachedToolsForMCPServer(r.Context(), r, name)
		if len(cachedTools) == 0 {
			skippedNoCache = append(skippedNoCache, name)
			continue
		}
		if excluded := config.GetExcludedTools(name); excluded != nil {
			filtered := make([]cache.ToolEntry, 0, len(cachedTools))
			for _, t := range cachedTools {
				if !excluded[t.Name] {
					filtered = append(filtered, t)
				}
			}
			cachedTools = filtered
		}
		if len(cachedTools) == 0 {
			continue
		}

		lt := agent.NewLazyMCPToolset(name, cachedTools, serverCfg, debug)
		if resolver != nil {
			lt.SetCredentialResolver(resolver)
		}
		if pool != nil {
			lt.SetSandboxPool(pool)
		}
		sanitized := agent.NewSanitizedToolset(lt, debug)
		groupName := "mcp:" + name
		groups[groupName] = &agent.ToolGroup{
			Name:        groupName,
			Description: fmt.Sprintf("MCP server: %s (%d tools)", name, lt.ToolCount()),
			Toolsets:    []tool.Toolset{sanitized},
		}
	}

	if len(skippedNoCache) > 0 {
		slog.Info("MCP servers configured but have no cached tools (run Settings → MCP → Test/Refresh)",
			"servers", skippedNoCache)
	}
	if len(groups) > 0 {
		slog.Info("built request MCP tool groups for chat", "count", len(groups), "groups", groupNames(groups))
	}
	return groups
}

func groupNames(groups map[string]*agent.ToolGroup) []string {
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	return names
}

// cachedToolsForMCPServer returns tool declarations from the request's MCP
// stores (team → org → platform), falling back to the file-based tools cache.
func cachedToolsForMCPServer(ctx context.Context, r *http.Request, serverName string) []cache.ToolEntry {
	// Prefer DB cached_tools from the request-scoped cascade.
	if entries := getCachedToolsFromServices(r, serverName); len(entries) > 0 {
		return entries
	}
	// File cache (personal mode / discovery fallback)
	return cache.GetToolsForServer(serverName)
}

func getCachedToolsFromServices(r *http.Request, serverName string) []cache.ToolEntry {
	if r == nil {
		return nil
	}
	svc := store.FromRequest(r)
	if svc == nil {
		return nil
	}
	ctx := r.Context()

	// team → org → platform (same override order as getPlatformCachedTools)
	try := func(ms store.MCPServerStore) []cache.ToolEntry {
		if ms == nil {
			return nil
		}
		srv, err := ms.Get(ctx, serverName)
		if err != nil || srv == nil || len(srv.CachedTools) == 0 {
			return nil
		}
		return parseCachedToolsJSONBytes(srv.CachedTools)
	}

	if entries := try(svc.TeamMCPServers); len(entries) > 0 {
		return entries
	}
	if entries := try(svc.MCPServers); len(entries) > 0 {
		return entries
	}
	if entries := try(svc.PlatformMCPServers); len(entries) > 0 {
		return entries
	}
	return nil
}

func parseCachedToolsJSONBytes(data []byte) []cache.ToolEntry {
	type discoveredTool struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	}
	var tools []discoveredTool
	if err := json.Unmarshal(data, &tools); err != nil {
		slog.Warn("failed to parse request MCP cached tools", "error", err)
		return nil
	}
	entries := make([]cache.ToolEntry, 0, len(tools))
	for _, t := range tools {
		entries = append(entries, cache.ToolEntry{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return entries
}
