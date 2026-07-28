package tools

import (
	"fmt"

	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// MemorySearchArgs defines the arguments for the memory_search tool.
type MemorySearchArgs struct {
	Query      string `json:"query" jsonschema:"The search query to find relevant knowledge in memory"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"Maximum number of results to return (default 6)"`
}

// PlatformMemorySearchResult is returned from memory search in platform mode.
// Uses store.MemorySearchResult which includes scope information.
type PlatformMemorySearchResult struct {
	Results []store.MemorySearchResult `json:"results"`
	Count   int                        `json:"count"`
	Message string                     `json:"message,omitempty"`
}

// MemorySearch creates the memory_search handler. In platform mode it routes to
// PG-backed stores from the request context. The memStore parameter is kept for
// API compatibility but is unused (always nil in platform mode).
func MemorySearch() func(ctx tool.Context, args MemorySearchArgs) (any, error) {
	return func(ctx tool.Context, args MemorySearchArgs) (any, error) {
		if args.Query == "" {
			return nil, fmt.Errorf("query is required")
		}

		// Platform mode: prefer the PG-backed three-tier searcher from context.
		if searcher := store.ThreeTierSearcherFromContext(ctx); searcher != nil {
			return platformMemorySearch(ctx, args, searcher, nil)
		}
		// Fall back to PG single-tier if available.
		if ms := store.MemoryStoreFromContext(ctx); ms != nil {
			return platformMemorySearch(ctx, args, nil, ms)
		}

		return PlatformMemorySearchResult{
			Results: []store.MemorySearchResult{},
			Count:   0,
			Message: "Memory search is not available.",
		}, nil
	}
}

// platformMemorySearch performs cross-tier or single-tier PG search.
func platformMemorySearch(ctx tool.Context, args MemorySearchArgs, searcher store.ThreeTierSearcher, fallback store.MemoryStore) (PlatformMemorySearchResult, error) {
	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = 6
	}
	// Fetch extra candidates before scenario-card de-duplication and query
	// filtering; otherwise a valid search can shrink below maxResults after
	// duplicate/unrelated cards are removed. The small fixed multiplier keeps the
	// direct tool aligned with automatic knowledge retrieval without making every
	// query a broad memory scan.
	searchLimit := maxResults * 3
	if searchLimit < 20 {
		searchLimit = 20
	}

	var results []store.MemorySearchResult
	var err error

	if searcher != nil {
		results, err = searcher.SearchAllTiers(ctx, args.Query, searchLimit, 0)
	} else if fallback != nil {
		results, err = fallback.Search(ctx, args.Query, searchLimit, 0)
	} else {
		return PlatformMemorySearchResult{
			Results: []store.MemorySearchResult{},
			Count:   0,
			Message: "Memory search is not available.",
		}, nil
	}

	if err != nil {
		return PlatformMemorySearchResult{}, fmt.Errorf("memory search failed: %w", err)
	}

	results = memory.FilterPreferredScenarioResultsForQuery(args.Query, results)

	if len(results) == 0 {
		return PlatformMemorySearchResult{
			Results: []store.MemorySearchResult{},
			Count:   0,
			Message: "No matching results found in memory.",
		}, nil
	}
	if len(results) > maxResults {
		results = results[:maxResults]
	}

	return PlatformMemorySearchResult{
		Results: results,
		Count:   len(results),
	}, nil
}

// PlatformMemorySearch performs cross-tier memory search in platform mode.
// When a ThreeTierSearcher is provided, it searches personal + team + org tiers.
// Falls back to single-tier search via the store.MemoryStore if no three-tier
// searcher is available.
func PlatformMemorySearch(searcher store.ThreeTierSearcher, fallback store.MemoryStore) func(ctx tool.Context, args MemorySearchArgs) (PlatformMemorySearchResult, error) {
	return func(ctx tool.Context, args MemorySearchArgs) (PlatformMemorySearchResult, error) {
		if args.Query == "" {
			return PlatformMemorySearchResult{}, fmt.Errorf("query is required")
		}
		return platformMemorySearch(ctx, args, searcher, fallback)
	}
}

// NewMemorySearchTool creates the memory_search tool.
// In platform mode, the tool checks the context for PG-backed stores
// (injected by ChatRunner.InjectMemoryStores).
func NewMemorySearchTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "memory_search",
		Description: "Search memory for relevant knowledge. Use before answering questions " +
			"about prior decisions, preferences, facts, project details, or past conversations. " +
			"Returns scored snippets with file path and line references. " +
			"Use memory_get to read more context around a search result.",
	}, MemorySearch())
}

// NewPlatformMemorySearchTool creates the memory_search tool for platform mode.
// Performs cross-tier search (personal + team + org) when a ThreeTierSearcher
// is provided. Results include a "scope" field indicating the knowledge tier.
func NewPlatformMemorySearchTool(searcher store.ThreeTierSearcher, fallback store.MemoryStore) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "memory_search",
		Description: "Search memory for relevant knowledge across all knowledge tiers " +
			"(personal, team, organization). Use before answering questions about prior decisions, " +
			"preferences, facts, project details, or past conversations. " +
			"Results include a 'scope' field (personal/team/org) indicating the knowledge tier.",
	}, PlatformMemorySearch(searcher, fallback))
}
