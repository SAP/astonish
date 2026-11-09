package chatruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"log/slog"
)

func BuildMCPToolGroups(ctx context.Context, svc *store.Services, mcpCfg *config.MCPConfig, resolver credentials.CredentialResolver, pool sandbox.ToolNodePool, debug bool, fallback FallbackPolicy) map[string]*agent.ToolGroup {
	groups, _ := BuildMCPToolGroupsChecked(ctx, svc, mcpCfg, resolver, pool, debug, fallback)
	return groups
}

func BuildMCPToolGroupsChecked(ctx context.Context, svc *store.Services, mcpCfg *config.MCPConfig, resolver credentials.CredentialResolver, pool sandbox.ToolNodePool, debug bool, fallback FallbackPolicy) (map[string]*agent.ToolGroup, error) {
	if mcpCfg == nil || len(mcpCfg.MCPServers) == 0 {
		return nil, nil
	}

	groups := make(map[string]*agent.ToolGroup)
	var skippedNoCache []string

	for name, serverCfg := range mcpCfg.MCPServers {
		if !serverCfg.IsEnabled() {
			continue
		}
		cachedTools, err := CachedToolsForMCPServerChecked(ctx, svc, name, fallback)
		if err != nil {
			return nil, err
		}
		if len(cachedTools) == 0 {
			skippedNoCache = append(skippedNoCache, name)
			continue
		}
		var excluded map[string]bool
		if fallback == FallbackAllowed {
			excluded = config.GetExcludedTools(name)
		}
		if excluded != nil {
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
	return groups, nil
}
func groupNames(groups map[string]*agent.ToolGroup) []string {
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	return names
}
func CachedToolsFromServices(ctx context.Context, svc *store.Services, serverName string) []cache.ToolEntry {
	entries, _ := cachedToolsFromServices(ctx, svc, serverName, FallbackAllowed)
	return entries
}
func cachedToolsFromServices(ctx context.Context, svc *store.Services, name string, fallback FallbackPolicy) ([]cache.ToolEntry, error) {
	if svc == nil {
		return nil, nil
	}
	for _, tier := range []store.MCPServerStore{svc.TeamMCPServers, svc.MCPServers, svc.PlatformMCPServers} {
		if tier == nil {
			continue
		}
		srv, err := tier.Get(ctx, name)
		if err != nil {
			if fallback == FallbackForbidden {
				return nil, fmt.Errorf("MCP cache %q: %w", name, err)
			}
			continue
		}
		if srv == nil {
			continue
		}
		if len(srv.CachedTools) == 0 {
			if fallback == FallbackForbidden {
				return nil, nil
			}
			continue
		}
		entries, err := ParseCachedToolsJSONBytes(srv.CachedTools)
		if err != nil && fallback == FallbackForbidden {
			return nil, fmt.Errorf("MCP cache %q: %w", name, err)
		}
		if len(entries) > 0 || fallback == FallbackForbidden {
			return entries, nil
		}
	}
	return nil, nil
}

// ParseCachedToolsJSONBytes parses the shared persisted MCP discovery format.
func ParseCachedToolsJSONBytes(data []byte) ([]cache.ToolEntry, error) {
	type discoveredTool struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	}
	var tools []discoveredTool
	if err := json.Unmarshal(data, &tools); err != nil {
		return nil, err
	}
	entries := make([]cache.ToolEntry, 0, len(tools))
	for _, t := range tools {
		entries = append(entries, cache.ToolEntry{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return entries, nil
}
func CachedToolsForMCPServer(ctx context.Context, svc *store.Services, name string, fallback FallbackPolicy) []cache.ToolEntry {
	entries, _ := CachedToolsForMCPServerChecked(ctx, svc, name, fallback)
	return entries
}
func CachedToolsForMCPServerChecked(ctx context.Context, svc *store.Services, name string, fallback FallbackPolicy) ([]cache.ToolEntry, error) {
	entries, err := cachedToolsFromServices(ctx, svc, name, fallback)
	if err != nil || len(entries) > 0 {
		return entries, err
	}
	if fallback == FallbackAllowed {
		return cache.GetToolsForServer(name), nil
	}
	return nil, nil
}
