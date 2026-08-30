package agent

import (
	"context"
	"fmt"
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
	merged := make(map[string]*ToolGroup, len(groups)+len(RequestMCPGroupsFromContext(ctx)))
	for name, group := range RequestMCPGroupsFromContext(ctx) {
		merged[name] = group
	}
	for name, group := range groups {
		merged[name] = group
	}
	return context.WithValue(ctx, requestMCPGroupsKey{}, merged)
}

// RequestMCPGroupsFromContext returns per-request MCP tool groups, or nil.
func RequestMCPGroupsFromContext(ctx context.Context) map[string]*ToolGroup {
	if ctx == nil {
		return nil
	}
	g, _ := ctx.Value(requestMCPGroupsKey{}).(map[string]*ToolGroup)
	return g
}

// LookupRequestMCPTool resolves a request-scoped tool. Qualified group/tool
// references are exact; ambiguous bare names are rejected.
func LookupRequestMCPTool(ctx context.Context, name string) (tool.Tool, string, error) {
	groups := RequestMCPGroupsFromContext(ctx)
	name = strings.TrimSpace(name)
	if len(groups) == 0 || name == "" {
		return nil, "", nil
	}
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		if slash == 0 || slash == len(name)-1 {
			return nil, "", nil
		}
		groupName, toolName := name[:slash], name[slash+1:]
		if g := groups[groupName]; g != nil {
			return findToolInGroup(g, toolName), groupName, nil
		}
		return nil, "", nil
	}

	var matched tool.Tool
	var matchedGroup string
	for groupName, group := range groups {
		if candidate := findToolInGroup(group, name); candidate != nil {
			if matched != nil {
				return nil, "", fmt.Errorf("tool %q is ambiguous across request groups; use a qualified group/tool reference", name)
			}
			matched = candidate
			matchedGroup = groupName
		}
	}
	return matched, matchedGroup, nil
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
				ToolName:    gName + "/" + t.Name(),
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
