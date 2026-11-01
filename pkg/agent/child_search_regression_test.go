package agent

import (
	"context"
	"fmt"
	"testing"
)

func TestChildSearchPerplexityRankingAndLimits(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})
	mgr.ToolGroups = map[string]*ToolGroup{
		"core":    {Name: "core", Tools: mockTools("read_file", "announce_plan")},
		"browser": {Name: "browser", Tools: mockTools("browser_wait_for")},
		"web":     {Name: "web", Tools: mockTools("perplexity_web_search", "web_fetch")},
	}
	for i := 0; i < 120; i++ {
		mgr.ToolGroups["browser"].Tools = append(mgr.ToolGroups["browser"].Tools, mockTool{name: fmt.Sprintf("browser_search_%03d", i)})
	}
	scope, _, err := mgr.childToolScope(context.Background(), SubAgentTask{ToolFilter: []string{"core"}, ToolSelectionMode: ChildToolSelectionPreferred})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		limit int
		first string
	}{
		{"perplexity_web_search", 5, "perplexity_web_search"},
		{"Perplexity web search factual lookup", 3, "perplexity_web_search"},
		{"web/perplexity_web_search", 1, "perplexity_web_search"},
		{"*", 1, ""},
		{"list all tools", 3, ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			hits, err := scope.Search(context.Background(), tc.query, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) == 0 || len(hits) > tc.limit {
				t.Fatalf("got %d results, limit %d", len(hits), tc.limit)
			}
			if tc.first != "" && hits[0].ToolName != tc.first {
				t.Fatalf("first result = %s, want %s", hits[0].ToolName, tc.first)
			}
		})
	}
	if got := len(scope.Matches(context.Background())); got != 125 {
		t.Fatalf("inventory lost entries: %d", got)
	}
	if len(scope.initial) != 2 {
		t.Fatal("search changed initial declarations")
	}
}
