package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
	"testing"
)

func TestChatCatalogPreservesExecutableIdentity(t *testing.T) {
	base, err := tools.GetInternalTools()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ChatFactoryConfig{AppConfig: &config.AppConfig{}, CatalogResources: &ChatCatalogResources{Prepared: true, CoreTools: base}}
	catalog, err := BuildChatCatalog(context.Background(), nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]tool.Tool{}
	for _, x := range base {
		original[x.Name()] = x
	}
	for _, g := range catalog.Groups {
		for _, x := range g.Tools {
			if want := original[x.Name()]; want != nil && want != x {
				t.Fatalf("%s executable replaced", x.Name())
			}
		}
	}
	for _, x := range catalog.MainThreadTools {
		if original[x.Name()] != x {
			t.Fatalf("%s direct executable replaced", x.Name())
		}
	}
	ctx := store.WithDisabledTools(context.Background(), []string{"read_file"})
	filtered, err := BuildChatCatalog(ctx, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range filtered.AllTools {
		if x.Name() == "read_file" {
			t.Fatal("disabled tool advertised")
		}
	}
}

type catalogTestPool struct{ sandbox.ToolNodePool }

func TestChatCatalogScopedGroups(t *testing.T) {
	f := newBotFactoryFixture(t)
	inputs, err := chatruntime.Build(context.Background(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopeTeam, AppConfig: &config.AppConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := BuildChatCatalog(context.Background(), inputs, &ChatFactoryConfig{AppConfig: &config.AppConfig{}, CatalogResources: &ChatCatalogResources{Pool: &catalogTestPool{}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"core", "web", "process", "credentials", "scheduler", "slides", "distill", "fleet", "drill", "flows"} {
		if catalog.Groups[name] == nil {
			t.Errorf("missing group %s", name)
		}
	}
	for _, target := range catalog.MainThreadTools {
		for _, g := range catalog.Groups {
			for _, other := range g.Tools {
				if target.Name() == other.Name() && target != other {
					t.Fatalf("different wrappers for %s", target.Name())
				}
			}
		}
	}
	if catalog.Groups["email"] != nil || catalog.Groups["browser"] != nil {
		t.Fatal("inherited host transports")
	}
}
func TestChatCatalogRequiresExplicitScopedResources(t *testing.T) {
	if _, err := BuildChatCatalog(context.Background(), nil, &ChatFactoryConfig{AppConfig: &config.AppConfig{}}); err == nil {
		t.Fatal("accepted absent scoped inputs")
	}
}
func TestScopedEmailAccountNoHostFallback(t *testing.T) {
	for _, personal := range []bool{true, false} {
		if _, err := scopedEmailAccount(context.Background(), &store.Services{}, personal); err == nil {
			t.Fatal("missing scoped account accepted")
		}
	}
	if _, err := ScopedChatEmailTools(ScopedChatEmailOptions{}); err == nil {
		t.Fatal("missing scoped transport accepted")
	}
}
