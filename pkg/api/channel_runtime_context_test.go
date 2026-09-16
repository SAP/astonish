package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/store"
)

type channelRuntimePlatformSettingsStore struct {
	settings *store.PlatformSettings
}

type channelRuntimeMCPStore struct {
	servers []store.MCPServer
}

func (s *channelRuntimeMCPStore) List(context.Context) ([]store.MCPServer, error) {
	return s.servers, nil
}

func (s *channelRuntimeMCPStore) Get(_ context.Context, name string) (*store.MCPServer, error) {
	for i := range s.servers {
		if s.servers[i].Name == name {
			return &s.servers[i], nil
		}
	}
	return nil, nil
}

func (s *channelRuntimeMCPStore) Save(context.Context, *store.MCPServer) error { return nil }
func (s *channelRuntimeMCPStore) Delete(context.Context, string) error         { return nil }
func (s *channelRuntimeMCPStore) UpdateCachedTools(context.Context, string, json.RawMessage) error {
	return nil
}

func (s channelRuntimePlatformSettingsStore) Get(context.Context) (*store.PlatformSettings, error) {
	return s.settings, nil
}

func (s channelRuntimePlatformSettingsStore) Save(context.Context, *store.PlatformSettings) error {
	return nil
}

func TestWithChannelRuntimeContextInjectsTenantTools(t *testing.T) {
	cached, err := json.Marshal([]map[string]any{{
		"name":        "tenant_search",
		"description": "Search the tenant source",
		"inputSchema": map[string]any{"type": "object"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := store.WithServices(context.Background(), &store.Services{
		Mode: store.ModePlatform,
		PlatformSettings: channelRuntimePlatformSettingsStore{settings: &store.PlatformSettings{
			WebSearchTool: "perplexity:perplexity_web_search",
			PerplexityWebSearch: &store.PerplexityWebSearchSettings{
				Provider: "perplexity",
				Model:    "sonar",
			},
		}},
		PlatformMCPServers: &channelRuntimeMCPStore{servers: []store.MCPServer{{
			Name:        "tenant-web",
			Command:     "tenant-web",
			Enabled:     boolPtr(true),
			CachedTools: cached,
		}}},
	})

	ctx = WithChannelRuntimeContext(ctx, nil)

	groups := agent.RequestMCPGroupsFromContext(ctx)
	if groups["mcp:tenant-web"] == nil {
		t.Fatalf("request MCP groups = %#v, want mcp:tenant-web", groups)
	}
	overrides := agent.PromptOverridesFromContext(ctx)
	if overrides == nil || overrides.WebSearchAvailable == nil || !*overrides.WebSearchAvailable {
		t.Fatalf("prompt overrides = %#v, want web search available", overrides)
	}
	if overrides.WebSearchToolName != "perplexity_web_search" {
		t.Fatalf("web search tool = %q, want perplexity_web_search", overrides.WebSearchToolName)
	}
	if len(overrides.AdditionalTools) != 1 || overrides.AdditionalTools[0].Name() != "perplexity_web_search" {
		t.Fatalf("additional tools = %#v, want perplexity_web_search", overrides.AdditionalTools)
	}
}
