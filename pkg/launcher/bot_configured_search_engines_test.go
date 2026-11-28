package launcher

import (
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotConfiguredSearchDefaultEngines(t *testing.T) {
	for _, tc := range []struct{ selection, engine, name string }{
		{"tavily:tavily_search", "tavily", "tavily"},
		{"tavily:tavily-search", "tavily", "tavily"},
		{"brave-search:brave_web_search", "brave", "brave-search"},
		{"firecrawl:firecrawl_search", "firecrawl", "firecrawl"},
	} {
		t.Run(tc.engine+tc.selection, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.services.TeamMCPServers = newMemMCPStore(store.MCPServer{Name: tc.name, Command: "npx", Env: map[string]string{"API_KEY": "{{CREDENTIAL:owner:token}}"}})
			route, err := botConfiguredSearchRoute(f.ctx, f.services, &config.AppConfig{General: config.GeneralConfig{WebSearchTool: tc.selection}})
			if err != nil || string(route.Engine) != tc.engine || route.Tool != "mcp:"+tc.name+"/"+tc.selection[len(tc.name)+1:] {
				t.Fatalf("exact selected route: %+v %v", route, err)
			}
		})
	}
}

func TestBotConfiguredSearchRejectsUnknownAndUnscoped(t *testing.T) {
	for _, selected := range []string{"", "brave-search:brave_local_search", "firecrawl:firecrawl_scrape", "tavily:unknown", "unknown:web_search"} {
		f := newBotFactoryFixture(t)
		knowledgeSearchSettings(f)
		if _, err := botConfiguredSearchRoute(f.ctx, f.services, &config.AppConfig{General: config.GeneralConfig{WebSearchTool: selected}}); err == nil {
			t.Fatalf("selection %q fell back", selected)
		}
	}
	for _, env := range []map[string]string{nil, {"API_KEY": "host-secret"}, {"API_KEY": "prefix{{CREDENTIAL:owner:token}}suffix"}} {
		f := newBotFactoryFixture(t)
		f.services.TeamMCPServers = newMemMCPStore(store.MCPServer{Name: "brave-search", Command: "npx", Env: env})
		if _, err := botConfiguredSearchRoute(f.ctx, f.services, &config.AppConfig{General: config.GeneralConfig{WebSearchTool: "brave-search:brave_web_search"}}); err == nil {
			t.Fatal("non-symbolic credentials admitted")
		}
	}
	// The standard descriptor is not authority to load host web_servers credentials.
	f := newBotFactoryFixture(t)
	if _, err := botConfiguredSearchRoute(f.ctx, f.services, &config.AppConfig{General: config.GeneralConfig{WebSearchTool: "brave-search:brave_web_search"}, WebServers: map[string]config.WebServerConfig{"brave-search": {APIKey: "host-secret"}}}); err == nil {
		t.Fatal("host credential fallback")
	}
}
