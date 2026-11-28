package launcher

import (
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/adk/tool"
)

// The research hook owns only admission/episode quota. Its continuation is the
// shared executable: no extra provider or model reservation belongs to this hook.
func TestBotSharedResearchContinuationCeilingAndDenial(t *testing.T) {
	for _, denial := range []string{"quota", "revoked", "route-changed", "wrong-session", "invalid-results", "unknown-field", "trailing-json"} {
		t.Run(denial, func(t *testing.T) {
			f, runtime, episode := researchFixture(t)
			guarded, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), nil, nil, episode)
			if err != nil {
				t.Fatal(err)
			}
			guard := guarded.(*botConfiguredResearchTool)
			guard.sharedResearchAdapter = true
			calls := 0
			guard.shared = func(c tool.Context, args any) (map[string]any, error) {
				calls++
				values := args.(map[string]any)
				if values["max_results"] != float64(runtime.m.PublicResearch.MaxResults) {
					t.Fatal("result ceiling lost")
				}
				return map[string]any{"query": values["query"], "answer": "Shared executable evidence", "citations": []string{"https://example.com/decisive"}}, nil
			}
			c := optionalKnowledgeContext{knowledgeCtx(f)}
			for i := 0; i < 2; i++ {
				result, err := guard.search(c, botPublicResearchArgs{Facet: runtime.m.PublicResearch.Facets[0]})
				if err != nil {
					t.Fatal(err)
				}
				if result.Metadata["episode_search_calls"] != int64(i+1) {
					t.Fatalf("ceiling: %+v", result.Metadata)
				}
			}
			if calls != 2 || runtime.calls != 2 {
				t.Fatal("double or missing reservation")
			}
			switch denial {
			case "quota":
				runtime.denyReservation = true
			case "revoked":
				runtime.revoked = true
			case "route-changed":
				f.services.Settings = botKnowledgeTeamSettings{}
			case "wrong-session":
				if _, err := episode.Begin(context.Background(), f.req.Run.ID, "foreign-session"); err == nil {
					t.Fatal("foreign session admitted")
				}
				return
			case "unknown-field", "trailing-json":
				raw := json.RawMessage(`{"query":"public subject","external_url":"https://example.com"}`)
				if denial == "trailing-json" {
					raw = json.RawMessage(`{"query":"public subject"} {}`)
				}
				if _, _, err := guard.prepare(c, raw); err == nil {
					t.Fatal("malformed shared arguments admitted")
				}
				return
			case "invalid-results":
				raw, _ := json.Marshal(map[string]any{"query": "public subject", "max_results": -1})
				if _, _, err := guard.prepare(c, raw); err == nil {
					t.Fatal("expanded result ceiling admitted")
				}
				return
			}
			if _, err := guard.search(c, botPublicResearchArgs{Facet: runtime.m.PublicResearch.Facets[0]}); err == nil {
				t.Fatal("denial bypass")
			}
			if calls != 2 {
				t.Fatal("denial reached shared executable")
			}
			if denial != "quota" && episode.AllowDetail(c, f.req.Run.ID, c.SessionID(), "https://example.com/decisive") == nil {
				t.Fatal("revocation/drift retained detail authority")
			}
		})
	}
}
