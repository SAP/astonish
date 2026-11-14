package launcher

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotConfiguredSearchStandardNoRowDispatch(t *testing.T) {
	for _, tc := range []struct{ name, executable, payload string }{
		{"tavily", "tavily_search", `{"results":[{"url":"https://example.com/source"}]}`},
		{"brave-search", "brave_web_search", `{"web":{"results":[{"url":"https://example.com/source"}]}}`},
		{"firecrawl", "firecrawl_search", `{"data":[{"url":"https://example.com/source"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, runtime, episode := researchFixture(t)
			settings := &store.TeamSettings{WebSearchTool: tc.name + ":" + tc.executable, WebServers: map[string]map[string]string{tc.name: {"api_key": productionTestSecret}}}
			f.services.Settings = botKnowledgeTeamSettings{row: settings}
			c := store.WithServices(store.WithCredentialStore(f.ctx, sessioncreds.NewStore(nil)), f.services)
			route, err := BotEffectiveConfiguredSearchRoute(c, f.services)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(route)
			if strings.Contains(string(raw), productionTestSecret) {
				t.Fatal("route secret leak")
			}
			runtime.m.PublicResearch.Route = route
			auth, err := execution.VerifyBotMandateAuthorization(c, runtime, runtime.m.Ref, runtime.m.Purpose)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Mandate.Authorization = auth
			client := &configuredSearchReceiptClient{payload: strings.Replace(tc.payload, `"url":`, `"title":"`+productionTestSecret+`","url":`, 1)}
			backend := &productionCapabilityBackend{}
			mcp, cleanup, err := BotMCPTools(c, productionCapabilityPool{backend: backend, client: client}, f.req.Run.SessionID.String())
			defer cleanup()
			if err != nil || len(mcp) != 1 {
				t.Fatalf("selected adapter %d %v", len(mcp), err)
			}
			search, err := BotConfiguredResearchTool(c, f.req, knowledgeScope(f), nil, mcp, episode)
			if err != nil {
				t.Fatal(err)
			}
			ctx := knowledgeCtx(f)
			ctx.base = c
			out, err := search.(botRunnableTool).Run(ctx, map[string]any{"facet": "overview"})
			if err != nil || client.calls != 1 || episode.State() != BotResearchSearched {
				t.Fatalf("dispatch %v calls %d", err, client.calls)
			}
			raw, _ = json.Marshal(client.args)
			if strings.Contains(string(raw), productionTestSecret) || !strings.Contains(string(raw), tc.executable) || !strings.Contains(string(raw), "npx") {
				t.Fatalf("unsafe/wrong descriptor %s", raw)
			}
			raw, _ = json.Marshal(out)
			if strings.Contains(string(raw), productionTestSecret) {
				t.Fatal("result secret leak")
			}
			if !strings.Contains(string(raw), "https://example.com/source") {
				t.Fatal("missing source")
			}
			adapter := mcp[0].(*botMCPTool)
			vault, parseErr := sessioncreds.Parse(backend.vault)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			handle := strings.TrimSuffix(strings.TrimPrefix(route.CredentialHandles[0], "{{CREDENTIAL:"), ":value}}")
			if credential := vault.Get(c, handle); credential == nil || credential.Value != productionTestSecret {
				t.Fatal("symbolic credential not materialized in protected vault")
			}
			if len(vault.List(c)) != 1 {
				t.Fatal("unexpected credential scope")
			}
			if adapter.standardCredentials.Get(c, "not-owned") != nil {
				t.Fatal("foreign credential")
			}
			// Settings/key rotation cannot reinterpret the immutable run's executable.
			settings.WebServers[tc.name]["api_key"] = "rotated-scoped-key"
			if _, err := adapter.currentStore(c); err == nil {
				t.Fatal("rotation accepted old pin")
			}
			if _, err := mcp[0].(botRunnableTool).Run(ctx, map[string]any{"query": "private"}); err == nil || client.calls != 1 {
				t.Fatal("raw sibling authorization")
			}
		})
	}
}

func TestBotConfiguredSearchStandardNoHostFallback(t *testing.T) {
	old := config.GetInstalledSecretGetterForTest()
	config.SetInstalledSecretGetter(func(string) string { return productionTestSecret })
	defer config.SetInstalledSecretGetter(old)
	f, _, _ := researchFixture(t)
	f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{WebSearchTool: "tavily:tavily_search"}}
	c := store.WithServices(store.WithCredentialStore(f.ctx, sessioncreds.NewStore(nil)), f.services)
	if _, err := BotEffectiveConfiguredSearchRoute(c, f.services); err == nil {
		t.Fatal("host fallback")
	}
	if _, _, err := BotMCPTools(c, productionCapabilityPool{backend: &productionCapabilityBackend{}, client: &configuredSearchReceiptClient{}}, f.req.Run.SessionID.String()); err == nil {
		t.Fatal("missing configuration accepted")
	}
}
