package chatruntime

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"testing"
)

func TestScopedPerplexityUsesExplicitProviderFactory(t *testing.T) {
	cfg := &config.AppConfig{}
	cfg.General.WebSearchTool = "perplexity:perplexity_web_search"
	cfg.PerplexityWebSearch.Provider, cfg.PerplexityWebSearch.Model = "tenant-provider", "sonar"
	called := 0
	sentinel := errors.New("fixture provider reached")
	in, err := BuildScopedChatRuntimeInputs(t.Context(), BuildOptions{Principal: testPrincipal(), ResourceScope: ResourceScopePersonal, Services: &store.Services{}, AppConfig: cfg, ProviderFactory: func(ctx context.Context, p, m string, c *config.AppConfig) (model.LLM, error) {
		called++
		if p != "tenant-provider" || m != "sonar" {
			t.Fatalf("wrong provider %s/%s", p, m)
		}
		return nil, sentinel
	}})
	if err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatal("construction called model")
	}
	first := in.RequestToolGroups()["configured_search"].Tools[0]
	for i := 0; i < 2; i++ {
		bound, err := in.Bind(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(agent.PromptOverridesFromContext(bound).AdditionalTools) != 0 {
			t.Fatal("Bind created a second executable outside catalog policy")
		}
		if in.RequestToolGroups()["configured_search"].Tools[0] != first {
			t.Fatal("search executable changed")
		}
		_, err = first.(interface {
			Run(tool.Context, any) (map[string]any, error)
		}).Run(searchFixtureContext{bound: bound}, map[string]any{"query": "source evidence"})
		if !errors.Is(err, sentinel) {
			t.Fatalf("explicit factory not used: %v", err)
		}
	}
	if called != 2 {
		t.Fatalf("factory calls %d", called)
	}
}

type searchFixtureContext struct {
	tool.Context
	bound context.Context
}

func (c searchFixtureContext) Value(k any) any                                      { return c.bound.Value(k) }
func (c searchFixtureContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

func testPrincipal() execution.Principal {
	return execution.Principal{Kind: execution.PrincipalKindLocal, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, Subject: "owner", Authenticated: true}
}
func TestInputsPreserveContextAndSnapshot(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "budget"))
	defer cancel()
	cfg := &config.AppConfig{}
	cfg.General.WebSearchTool = "search:tenant-search"
	// Local identity exercises snapshot isolation without claiming platform
	// authority. Platform membership has its own directory-backed tests.
	svc := &store.Services{Mode: store.DeploymentMode("local-fixture")}
	chain := []string{"base", "team"}
	in, err := Build(ctx, BuildOptions{Principal: testPrincipal(), Services: svc, ResourceScope: ResourceScopePersonal, AppConfig: cfg, Templates: SandboxSelectors{BaseLayerChain: func(context.Context) []string { return chain }}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.General.WebSearchTool = "wrong:wrong"
	svc.Mode = store.DeploymentMode("other")
	chain[1] = "wrong"
	bound, err := in.Bind(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Value(key{}) != "budget" {
		t.Fatal("lost caller policy context")
	}
	if store.FromContext(bound).Mode != store.DeploymentMode("local-fixture") {
		t.Fatal("services not snapshotted")
	}
	if got := agent.PromptOverridesFromContext(bound).WebSearchToolName; got != "tenant_search" {
		t.Fatalf("search=%s", got)
	}
	if got := store.SandboxLayerChainFromContext(bound)[1]; got != "team" {
		t.Fatal(got)
	}
	store.FromContext(bound).Mode = store.DeploymentMode("other")
	again, err := in.Bind(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if store.FromContext(again).Mode != store.DeploymentMode("local-fixture") {
		t.Fatal("bindings share mutable services")
	}
	cancel()
	if bound.Err() != context.Canceled {
		t.Fatal("lost cancellation")
	}
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestInputsRejectScopeMismatchAndMissingStores(t *testing.T) {
	p := testPrincipal()
	o := BuildOptions{Principal: p, ResourceScope: ResourceScopePersonal}
	if _, err := Build(context.Background(), o); err == nil {
		t.Fatal("strict runtime accepted missing stores")
	}
	o.Services = &store.Services{}
	in, err := Build(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	other := p
	other.Subject = "other"
	ctx, err := execution.WithPrincipal(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Bind(ctx); err == nil {
		t.Fatal("cross-principal bind accepted")
	}
	o.MemoryScope = store.MemoryScopePersonal
	if _, err := Build(context.Background(), o); err == nil {
		t.Fatal("personal memory silently fell back")
	}
}
func TestRequestToolGroupsCopiesContainers(t *testing.T) {
	in := &Inputs{groups: map[string]*agent.ToolGroup{"tenant": {Name: "tenant"}}}
	first := in.RequestToolGroups()
	first["tenant"].Name = "other"
	delete(first, "tenant")
	if in.RequestToolGroups()["tenant"].Name != "tenant" {
		t.Fatal("mutable group leaked")
	}
}
