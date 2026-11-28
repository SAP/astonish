package launcher

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
)

type configuredSearchReceiptClient struct {
	sandbox.ToolNodeClient
	calls   int
	args    map[string]any
	payload string
}

func (c *configuredSearchReceiptClient) Call(_ string, _ string, args map[string]any) (json.RawMessage, error) {
	c.calls++
	c.args = args
	return json.Marshal(map[string]any{"Content": []any{map[string]any{"type": "text", "text": c.payload}}})
}

func TestBotConfiguredSearchMCPExactDispatch(t *testing.T) {
	for _, tc := range []struct{ name, executable, payload, limitKey string }{
		{"tavily", "tavily_search", `{"results":[{"url":"https://example.com/source"}]}`, "max_results"},
		{"brave-search", "brave_web_search", `{"web":{"results":[{"url":"https://example.com/source"}]}}`, "count"},
		{"firecrawl", "firecrawl_search", `{"data":[{"url":"https://example.com/source"}]}`, "limit"},
	} {
		for _, mode := range []string{"success", "empty", "error"} {
			t.Run(tc.name+mode, func(t *testing.T) {
				f, runtime, episode := researchFixture(t)
				metadata, _ := json.Marshal([]any{map[string]any{"name": tc.executable, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}, tc.limitKey: map[string]any{"type": "integer"}}, "required": []string{"query", tc.limitKey}, "additionalProperties": false}}})
				server := store.MCPServer{Name: tc.name, Command: "fixture-not-started", Env: map[string]string{"API_KEY": "{{CREDENTIAL:owner:token}}"}, CachedTools: metadata}
				f.services.TeamMCPServers = newMemMCPStore(server)
				f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{WebSearchTool: tc.name + ":" + tc.executable}}
				runtime.m.PublicResearch.Route, _ = BotEffectiveConfiguredSearchRoute(f.ctx, f.services)
				auth, err := execution.VerifyBotMandateAuthorization(f.ctx, runtime, runtime.m.Ref, runtime.m.Purpose)
				if err != nil {
					t.Fatal(err)
				}
				f.req.Mandate.Authorization = auth
				c := store.WithCredentialStore(f.ctx, sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: productionTestSecret}}))
				client := &configuredSearchReceiptClient{payload: tc.payload}
				if mode == "empty" {
					client.payload = `{}`
				}
				if mode == "error" {
					client.payload = `{"error":"429"}`
				}
				mcp, cleanup, err := BotMCPTools(c, productionCapabilityPool{backend: &productionCapabilityBackend{}, client: client}, f.req.Run.SessionID.String(), f.services.TeamMCPServers)
				defer cleanup()
				if err != nil {
					t.Fatal(err)
				}
				search, err := BotConfiguredResearchTool(c, f.req, knowledgeScope(f), nil, mcp, episode)
				if err != nil {
					t.Fatal(err)
				}
				if client.calls != 0 {
					t.Fatal("eager dispatch")
				}
				ctx := knowledgeCtx(f)
				ctx.base = c
				out, err := search.(botRunnableTool).Run(ctx, map[string]any{"facet": "overview"})
				if client.calls != 1 || runtime.calls != 1 {
					t.Fatalf("dispatch/accounting %d %d", client.calls, runtime.calls)
				}
				raw, _ := json.Marshal(client.args)
				var req struct {
					Tool      string         `json:"tool"`
					Arguments map[string]any `json:"arguments"`
				}
				_ = json.Unmarshal(raw, &req)
				if req.Tool != tc.executable || req.Arguments[tc.limitKey] != float64(5) || req.Arguments["query"] != "Stock Market News overview" || len(req.Arguments) != 2 {
					t.Fatalf("wrong exact invocation %s", raw)
				}
				if mode == "success" {
					data, _ := json.Marshal(out)
					if err != nil || episode.State() != BotResearchSearched || !strings.Contains(string(data), "https://example.com/source") {
						t.Fatalf("receipt %s %v", data, err)
					}
				} else if err == nil || episode.AllowDetail(c, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/source") == nil {
					t.Fatal("error/empty unlocked details")
				}
				if _, err := mcp[0].(botRunnableTool).Run(ctx, map[string]any{"query": "private"}); err == nil || client.calls != 1 {
					t.Fatal("raw MCP bypass")
				}
			})
		}
	}
}

func TestBotConfiguredSearchMCPArguments(t *testing.T) {
	for _, tc := range []struct {
		engine execution.BotSearchEngine
		want   map[string]any
	}{
		{execution.BotSearchTavily, map[string]any{"query": "public overview", "max_results": 3}},
		{execution.BotSearchBrave, map[string]any{"query": "public overview", "count": 3}},
		{execution.BotSearchFirecrawl, map[string]any{"query": "public overview", "limit": 3}},
	} {
		got, err := botConfiguredSearchMCPArguments(tc.engine, "public overview", 3)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s arguments: %v %v", tc.engine, got, err)
		}
	}
	for _, engine := range []execution.BotSearchEngine{"", "other", execution.BotSearchPerplexity} {
		if _, err := botConfiguredSearchMCPArguments(engine, "public", 3); err == nil {
			t.Fatalf("unsupported %s", engine)
		}
	}
}

func TestBotConfiguredSearchMCPNormalize(t *testing.T) {
	for _, tc := range []struct {
		engine  execution.BotSearchEngine
		payload string
	}{
		{execution.BotSearchTavily, `{"results":[{"title":"Source","url":"https://example.com/source"}]}`},
		{execution.BotSearchBrave, `{"web":{"results":[{"title":"Source","url":"https://example.com/source"}]}}`},
		{execution.BotSearchFirecrawl, `{"success":true,"data":[{"title":"Source","url":"https://example.com/source"}]}`},
		{execution.BotSearchFirecrawl, `{"success":true,"data":{"web":[{"title":"Source","url":"https://example.com/source"}]}}`},
	} {
		for _, key := range []string{"content", "Content", "direct"} {
			var values map[string]any
			if key == "direct" {
				_ = json.Unmarshal([]byte(tc.payload), &values)
			} else {
				values = map[string]any{key: []any{map[string]any{"type": "text", "text": tc.payload}}}
			}
			result, err := botNormalizeConfiguredSearchMCP(tc.engine, "public overview", 1, values)
			if err != nil || len(result.SearchResults) != 1 || result.SearchResults[0].URL != "https://example.com/source" || result.Provider != string(tc.engine) {
				t.Fatalf("%s %s: %+v %v", tc.engine, key, result, err)
			}
		}
	}
}

func TestBotConfiguredSearchMCPNormalizeRejectsErrorsAndUnsafe(t *testing.T) {
	for _, payload := range []string{
		`{}`, `{"results":[]}`, `{"results":"broken"}`, `{"error":"failed","results":[{"url":"https://example.com"}]}`,
		`{"success":false,"results":[{"url":"https://example.com"}]}`, `{"isError":true,"content":[{"text":"{\"results\":[{\"url\":\"https://example.com\"}]}"}]}`,
		`{"content":[{"type":"text","text":"not JSON"}]}`, `{"results":[{"url":"http://127.0.0.1/private"},{"url":"http://10.0.0.1/private"},{"url":"http://localhost/private"},{"url":"https://user:pass@example.com"},{"url":"file:///etc/passwd"}]}`,
	} {
		var values map[string]any
		_ = json.Unmarshal([]byte(payload), &values)
		if result, err := botNormalizeConfiguredSearchMCP(execution.BotSearchTavily, "public", 3, values); err == nil || len(result.SearchResults) != 0 {
			t.Fatalf("payload admitted: %s %+v %v", payload, result, err)
		}
	}
	var values map[string]any
	_ = json.Unmarshal([]byte(`{"results":[{"url":"https://example.com/one"},{"url":"https://example.com/one"},{"url":"https://example.com/two"},{"url":"https://example.com/three"}]}`), &values)
	result, err := botNormalizeConfiguredSearchMCP(execution.BotSearchTavily, "public", 2, values)
	if err != nil || len(result.SearchResults) != 2 || result.SearchResults[1].URL != "https://example.com/two" {
		t.Fatalf("bound/deduplicate: %+v %v", result, err)
	}
	if _, err := botNormalizeConfiguredSearchMCP("unknown", "public", 2, values); err == nil {
		t.Fatal("unknown engine accepted")
	}
}
