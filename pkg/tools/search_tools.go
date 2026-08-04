package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// SearchToolsArgs defines the arguments for the search_tools tool.
type SearchToolsArgs struct {
	Query      string `json:"query" jsonschema:"Describe what you want to do (e.g., 'take a screenshot', 'send an email', 'check API health'). Use '*' or 'list all' to list every available tool."`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"Maximum number of results to return (default 10, ignored when listing all)"`
}

// SearchToolsMatchResult is a single tool match in the search results.
type SearchToolsMatchResult struct {
	ToolName    string  `json:"tool_name"`
	GroupName   string  `json:"group_name"`
	Description string  `json:"description"`
	IsMainTool  bool    `json:"is_main_tool"`
	Score       float64 `json:"score"`
	Access      string  `json:"access"`
}

// SearchToolsResult is returned from tool search.
type SearchToolsResult struct {
	Matches []SearchToolsMatchResult `json:"matches"`
	Count   int                      `json:"count"`
	Message string                   `json:"message,omitempty"`
}

// isListAllQuery returns true if the query is requesting a full tool inventory.
func isListAllQuery(query string) bool {
	switch query {
	case "*", "list all", "list all tools", "all", "all tools":
		return true
	}
	return false
}

// SearchTools performs semantic search across the tool index.
// When onResults is non-nil, it is called with the matched tool names so the
// dynamic injection system can make them available for direct use.
func SearchTools(toolIndex *agent.ToolIndex, onResults func([]string)) func(ctx tool.Context, args SearchToolsArgs) (SearchToolsResult, error) {
	return func(ctx tool.Context, args SearchToolsArgs) (SearchToolsResult, error) {
		if args.Query == "" {
			return SearchToolsResult{}, fmt.Errorf("query is required — describe what you want to do, or use '*' to list all tools")
		}

		// Handle "list all" mode
		if isListAllQuery(args.Query) {
			result := listAllTools(ctx, toolIndex)
			// Notify injection system about all tools
			if onResults != nil && len(result.Matches) > 0 {
				names := make([]string, len(result.Matches))
				for i, m := range result.Matches {
					names[i] = m.ToolName
				}
				onResults(names)
			}
			return result, nil
		}

		maxResults := args.MaxResults
		if maxResults <= 0 {
			maxResults = 10
		}

		// Use background context if tool.Context is nil (e.g., in tests)
		var searchCtx context.Context
		if ctx != nil {
			searchCtx = ctx
		} else {
			searchCtx = context.Background()
		}

		var matches []agent.ToolMatch
		if toolIndex != nil {
			var err error
			matches, err = toolIndex.SearchHybrid(searchCtx, args.Query, maxResults, 0.005)
			if err != nil {
				return SearchToolsResult{}, fmt.Errorf("tool search failed: %w", err)
			}
		}

		// Filter out MCP tools the user's team doesn't have access to
		matches = agent.FilterAccessibleToolMatches(searchCtx, matches)

		// Merge request-scoped MCP tools (not in pre-warmed index) that match the query.
		if mcpHits := agent.MatchRequestMCPGroupsFromQuery(searchCtx, args.Query); len(mcpHits) > 0 {
			mcpHits = agent.FilterAccessibleToolMatches(searchCtx, mcpHits)
			matches = agent.MergeToolMatches(matches, mcpHits)
		}
		// Also keyword-match individual request MCP tools by name/description.
		for _, m := range agent.ToolMatchesFromRequestMCP(searchCtx) {
			ql := strings.ToLower(args.Query)
			if strings.Contains(strings.ToLower(m.ToolName), ql) ||
				strings.Contains(strings.ToLower(m.Description), ql) ||
				strings.Contains(ql, strings.ToLower(m.ToolName)) {
				matches = agent.MergeToolMatches(matches, []agent.ToolMatch{m})
			}
		}
		matches = agent.FilterAccessibleToolMatches(searchCtx, matches)

		if len(matches) == 0 {
			return SearchToolsResult{
				Matches: []SearchToolsMatchResult{},
				Count:   0,
				Message: "No matching tools found. Try a different query, use '*' to list all tools, or check the system prompt for available tool groups.",
			}, nil
		}

		results := make([]SearchToolsMatchResult, len(matches))
		for i, m := range matches {
			access := toolAccessHint(m)
			results[i] = SearchToolsMatchResult{
				ToolName:    m.ToolName,
				GroupName:   m.GroupName,
				Description: m.Description,
				IsMainTool:  m.IsMainTool,
				Score:       m.Score,
				Access:      access,
			}
		}

		// Notify the dynamic injection system so these tools become callable
		// on the very next LLM call within this turn.
		if onResults != nil {
			names := make([]string, len(matches))
			for i, m := range matches {
				names[i] = m.ToolName
			}
			onResults(names)
		}

		return SearchToolsResult{
			Matches: results,
			Count:   len(results),
		}, nil
	}
}

// listAllTools returns every tool in the index, grouped by group name.
// MCP tools from servers the user doesn't have access to are excluded.
// Also merges per-request MCP groups (team servers not present in the
// pre-warmed singleton ToolIndex).
func listAllTools(ctx context.Context, toolIndex *agent.ToolIndex) SearchToolsResult {
	var searchCtx context.Context
	if ctx != nil {
		searchCtx = ctx
	} else {
		searchCtx = context.Background()
	}

	groups := map[string][]agent.ToolMatch{}
	if toolIndex != nil {
		groups = toolIndex.ListAll()
	}
	// Merge request-scoped MCP tools (team catalog) into inventory.
	for _, m := range agent.ToolMatchesFromRequestMCP(searchCtx) {
		groups[m.GroupName] = append(groups[m.GroupName], m)
	}

	var results []SearchToolsMatchResult
	// Sort group names for deterministic output
	groupNames := make([]string, 0, len(groups))
	for g := range groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	// Dedup tools within a group by name (index + request may overlap).
	for _, gName := range groupNames {
		// Filter out MCP groups the user's team doesn't have access to
		if agent.IsMCPGroupInaccessible(searchCtx, gName) {
			continue
		}

		seen := make(map[string]bool)
		for _, m := range groups[gName] {
			if seen[m.ToolName] {
				continue
			}
			seen[m.ToolName] = true
			results = append(results, SearchToolsMatchResult{
				ToolName:    m.ToolName,
				GroupName:   m.GroupName,
				Description: m.Description,
				IsMainTool:  m.IsMainTool,
				Score:       1.0,
				Access:      toolAccessHint(m),
			})
		}
	}

	// Count distinct groups in results
	groupCount := 0
	seenG := map[string]bool{}
	for _, m := range results {
		if !seenG[m.GroupName] {
			seenG[m.GroupName] = true
			groupCount++
		}
	}

	return SearchToolsResult{
		Matches: results,
		Count:   len(results),
		Message: fmt.Sprintf("Complete tool inventory: %d tools across %d groups", len(results), groupCount),
	}
}

// toolAccessHint tells the LLM how to invoke a matched tool. MCP tools must be
// called by bare tool_name (send_email), never as mcp:server/tool (app format).
func toolAccessHint(m agent.ToolMatch) string {
	if m.IsMainTool {
		return "always available (main thread tool) — call as `" + m.ToolName + "`"
	}
	if strings.HasPrefix(m.GroupName, "mcp:") {
		return "call directly as `" + m.ToolName + "` on the main thread (NOT `" + m.GroupName + "/" + m.ToolName +
			"`; do not use delegate_tasks for a single MCP call)"
	}
	return "available — call directly as `" + m.ToolName + "` (group " + m.GroupName + "; do not delegate a single tool call)"
}

// NewSearchToolsTool creates the search_tools tool using the given tool index.
// When onResults is non-nil, tools found via search become available for direct
// invocation through the dynamic tool injection system.
func NewSearchToolsTool(toolIndex *agent.ToolIndex, onResults func([]string)) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "search_tools",
		Description: "Search for available tools by describing what you want to do. " +
			"Found tools become available for you to call directly by tool_name " +
			"(e.g. send_email — never mcp:server/tool). " +
			"Use this when you need a capability that isn't currently available. " +
			"Use query='*' to list ALL available tools.",
	}, SearchTools(toolIndex, onResults))
}
