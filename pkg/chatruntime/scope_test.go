package chatruntime

import (
	"context"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

// Embedding interfaces supplies unused methods while keeping fixture identity distinct.
type privateDocs struct{ store.DocsStore }
type privateSessions struct{ store.SessionStore }

func TestInputsTeamScopeDropsPrivateCapabilities(t *testing.T) {
	docs := &privateDocs{}
	sessions := &privateSessions{}
	svc := &store.Services{PersonalDocs: docs, PersonalSessions: sessions}
	in, err := Build(context.Background(), BuildOptions{Principal: testPrincipal(), Services: svc, ResourceScope: ResourceScopeTeam})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := in.Bind(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.FromContext(ctx).PersonalDocs != nil || store.FromContext(ctx).PersonalSessions != nil {
		t.Fatal("private resources leaked into team scope")
	}
	if svc.PersonalDocs != docs || svc.PersonalSessions != sessions {
		t.Fatal("mutated caller services")
	}
	_, err = Build(context.Background(), BuildOptions{Principal: testPrincipal(), Services: svc, ResourceScope: ResourceScopeTeam, MemoryScope: store.MemoryScopePersonal})
	if err == nil {
		t.Fatal("team runtime accepted personal memory")
	}
}

func TestInputsStrictStdioRequiresScopedPool(t *testing.T) {
	_, err := Build(context.Background(), BuildOptions{Principal: testPrincipal(), Services: &store.Services{}, ResourceScope: ResourceScopePersonal, MCPConfig: &config.MCPConfig{MCPServers: map[string]config.MCPServerConfig{"host-command": {Command: "must-not-run"}}}})
	if err == nil {
		t.Fatal("strict runtime accepted host stdio fallback")
	}
}

func TestStrictMCPCatalogDoesNotReadHostCache(t *testing.T) {
	cfg := LoadMCPConfig(context.Background(), &store.Services{}, nil, FallbackForbidden)
	if len(cfg.MCPServers) != 0 {
		t.Fatal("strict empty services inherited host MCP config")
	}
	if got := CachedToolsForMCPServer(context.Background(), nil, "context7", FallbackForbidden); got != nil {
		t.Fatal("strict catalog inherited host cache")
	}
}
