package execution

import "testing"

func TestBotPublicResearchScope(t *testing.T) {
	s := BotPublicResearchScope{Version: 1, Subject: "Stock Market News", SourceEventID: "owner-event", Route: BotConfiguredSearchRoute{Engine: "perplexity", Provider: "sap_ai_core_sci", Model: "sonar", Tool: "perplexity_web_search", Fingerprint: "fixture-config", CredentialHandles: []string{"provider:sap_ai_core_sci:key"}}, Facets: []BotResearchFacet{BotResearchOverview, BotResearchRecent, BotResearchPrimary, BotResearchContradictions}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 4}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, f := range s.Facets {
		q, err := s.Query(f)
		if err != nil || len(q) <= len(s.Subject) {
			t.Fatalf("facet %s: %s %v", f, q, err)
		}
	}
	if _, err := s.Query(BotResearchFacet("private assignment")); err == nil {
		t.Fatal("arbitrary disclosure admitted")
	}
	for name, change := range map[string]func(*BotPublicResearchScope){
		"provider":       func(s *BotPublicResearchScope) { s.Route.Provider = "other" },
		"credential":     func(s *BotPublicResearchScope) { s.Route.CredentialHandles[0] = "provider:other:key" },
		"subject":        func(s *BotPublicResearchScope) { s.Subject = "private project" },
		"source":         func(s *BotPublicResearchScope) { s.SourceEventID = "spoofed" },
		"negative calls": func(s *BotPublicResearchScope) { s.MaxCalls = -1 },
		"facet":          func(s *BotPublicResearchScope) { s.Facets = s.Facets[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			current := s.Clone()
			change(&current)
			if s.ValidateWithin(&current) == nil {
				t.Fatal("changed current policy admitted")
			}
		})
	}
	if s.ValidateWithin(nil) == nil {
		t.Fatal("revoked grant admitted")
	}
	for _, calls := range []int64{0, 1, 100} {
		current := s.Clone()
		current.MaxCalls = calls
		if err := s.ValidateWithin(&current); err != nil {
			t.Fatal(err)
		}
	}
	clone := BotMandate{PublicResearch: &s}.Clone()
	clone.PublicResearch.Facets[0] = BotResearchRecent
	clone.PublicResearch.Route.CredentialHandles[0] = "changed"
	if s.Facets[0] != BotResearchOverview || s.Route.CredentialHandles[0] == "changed" {
		t.Fatal("mutable mandate clone")
	}
}

func TestBotPublicResearchSearchEngineBindings(t *testing.T) {
	for _, tc := range []struct {
		engine BotSearchEngine
		tool   string
	}{
		{BotSearchTavily, "tavily_search"}, {BotSearchTavily, "tavily-search"}, {BotSearchBrave, "brave_web_search"}, {BotSearchFirecrawl, "firecrawl_search"},
	} {
		engine, err := BotSearchEngineForTool(tc.tool)
		if err != nil || engine != tc.engine || !engine.IsMCP() {
			t.Fatalf("%s: %s %v", tc.tool, engine, err)
		}
		s := BotPublicResearchScope{Version: 1, Subject: "public subject", SourceEventID: "event", Route: BotConfiguredSearchRoute{Engine: tc.engine, Tool: "mcp:exact/" + tc.tool, Fingerprint: "fixture", CredentialHandles: []string{"{{CREDENTIAL:owner:token}}"}}, Facets: []BotResearchFacet{BotResearchOverview}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 2}
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"mcp:exact/unknown", "mcp:exact/brave_local_search", "mcp:exact/firecrawl_scrape", "mcp:/" + tc.tool, "mcp:exact/" + tc.tool + "/other"} {
			changed := s.Clone()
			changed.Route.Tool = bad
			if changed.Validate() == nil {
				t.Fatalf("invalid tool admitted: %s", bad)
			}
		}
		changed := s.Clone()
		changed.Route.Engine = "unknown"
		if changed.Validate() == nil {
			t.Fatal("unknown engine admitted")
		}
	}
	for _, tool := range []string{"", "web_search", "firecrawl_scrape", "brave_local_search"} {
		if _, err := BotSearchEngineForTool(tool); err == nil {
			t.Fatalf("unsupported tool %s", tool)
		}
	}
}
