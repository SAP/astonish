package agent

import (
	"context"
	"sort"
	"strings"

	"google.golang.org/adk/tool"
)

// requestMCPGroupsKey holds per-request MCP tool groups built from the
// request-scoped MCP server stores. The Studio chat agent is a multi-tenant
// singleton that is often pre-warmed for a default team; without this, team
// MCP servers (and their cached tools) never appear in search_tools or
// dynamic injection for the actual requesting team.
type requestMCPGroupsKey struct{}

// WithRequestMCPGroups attaches per-request MCP tool groups to ctx.
func WithRequestMCPGroups(ctx context.Context, groups map[string]*ToolGroup) context.Context {
	if ctx == nil || len(groups) == 0 {
		return ctx
	}
	return context.WithValue(ctx, requestMCPGroupsKey{}, groups)
}

// RequestMCPGroupsFromContext returns per-request MCP tool groups, or nil.
func RequestMCPGroupsFromContext(ctx context.Context) map[string]*ToolGroup {
	if ctx == nil {
		return nil
	}
	g, _ := ctx.Value(requestMCPGroupsKey{}).(map[string]*ToolGroup)
	return g
}

// LookupRequestMCPTool finds a tool by bare name in request MCP groups.
// Also resolves mcp:server/tool aliases.
func LookupRequestMCPTool(ctx context.Context, name string) (tool.Tool, string /*groupName*/, bool) {
	groups := RequestMCPGroupsFromContext(ctx)
	if len(groups) == 0 || name == "" {
		return nil, "", false
	}
	// Alias: mcp:email/send_email → send_email within that group
	if group, toolName, isRef := parseMCPToolRef(name); isRef {
		if toolName == "" {
			return nil, "", false
		}
		if g := groups[group]; g != nil {
			if t := findToolInGroup(g, toolName); t != nil {
				return t, group, true
			}
		}
		name = toolName
	}
	for gName, g := range groups {
		if t := findToolInGroup(g, name); t != nil {
			return t, gName, true
		}
	}
	return nil, "", false
}

func findToolInGroup(g *ToolGroup, name string) tool.Tool {
	if g == nil {
		return nil
	}
	for _, t := range g.Tools {
		if t != nil && t.Name() == name {
			return t
		}
	}
	readCtx := &minimalReadonlyContext{Context: context.Background()}
	for _, ts := range g.Toolsets {
		if ts == nil {
			continue
		}
		tools, err := ts.Tools(readCtx)
		if err != nil {
			continue
		}
		for _, t := range tools {
			if t != nil && t.Name() == name {
				return t
			}
		}
	}
	return nil
}

// ToolMatchesFromRequestMCP returns ToolMatch entries for every tool in the
// request-scoped MCP groups (for inventory / search merge).
func ToolMatchesFromRequestMCP(ctx context.Context) []ToolMatch {
	groups := RequestMCPGroupsFromContext(ctx)
	if len(groups) == 0 {
		return nil
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)

	readCtx := &minimalReadonlyContext{Context: context.Background()}
	var out []ToolMatch
	for _, gName := range names {
		g := groups[gName]
		if g == nil {
			continue
		}
		for _, t := range g.Tools {
			if t == nil {
				continue
			}
			out = append(out, ToolMatch{
				ToolName:    t.Name(),
				GroupName:   gName,
				Description: t.Description(),
				IsMainTool:  false,
				Score:       1.0,
			})
		}
		for _, ts := range g.Toolsets {
			if ts == nil {
				continue
			}
			tools, err := ts.Tools(readCtx)
			if err != nil {
				continue
			}
			for _, t := range tools {
				if t == nil {
					continue
				}
				out = append(out, ToolMatch{
					ToolName:    t.Name(),
					GroupName:   gName,
					Description: t.Description(),
					IsMainTool:  false,
					Score:       1.0,
				})
			}
		}
	}
	return out
}

// MatchRequestMCPGroupsFromQuery returns tools from request MCP groups that
// match free-text references (mcp server email, mcp:email, etc.).
func MatchRequestMCPGroupsFromQuery(ctx context.Context, query string) []ToolMatch {
	all := ToolMatchesFromRequestMCP(ctx)
	if len(all) == 0 || strings.TrimSpace(query) == "" {
		return nil
	}
	// Which groups are mentioned?
	mentioned := make(map[string]bool)
	for _, m := range all {
		if mcpGroupMentionedInQuery(query, m.GroupName) {
			mentioned[m.GroupName] = true
		}
	}
	if len(mentioned) == 0 {
		return nil
	}
	var out []ToolMatch
	for _, m := range all {
		if mentioned[m.GroupName] {
			out = append(out, m)
		}
	}
	return out
}

func mcpGroupMentionedInQuery(query, groupName string) bool {
	if !strings.HasPrefix(groupName, "mcp:") {
		return false
	}
	server := strings.TrimPrefix(groupName, "mcp:")
	if server == "" {
		return false
	}
	q := strings.ToLower(query)
	serverLower := strings.ToLower(server)
	groupLower := strings.ToLower(groupName)
	if strings.Contains(q, "mcp:"+serverLower) || strings.Contains(q, groupLower) {
		return true
	}
	if strings.Contains(q, "mcp") && (strings.Contains(q, " "+serverLower) ||
		strings.Contains(q, serverLower+" ") ||
		strings.HasSuffix(q, serverLower) ||
		strings.HasPrefix(q, serverLower+" ") ||
		strings.Contains(q, "server "+serverLower) ||
		strings.Contains(q, serverLower+" mcp") ||
		strings.Contains(q, "mcp "+serverLower)) {
		return true
	}
	return false
}
