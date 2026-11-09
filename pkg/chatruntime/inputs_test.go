package chatruntime

import (
	"context"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func testPrincipal() execution.Principal {
	return execution.Principal{Kind: execution.PrincipalKindLocal, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, Subject: "owner", Authenticated: true}
}
func TestInputsPreserveContextAndSnapshot(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "budget"))
	defer cancel()
	cfg := &config.AppConfig{}
	cfg.General.WebSearchTool = "search:tenant-search"
	svc := &store.Services{Mode: store.ModePlatform}
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
	if store.FromContext(bound).Mode != store.ModePlatform {
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
	if store.FromContext(again).Mode != store.ModePlatform {
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
