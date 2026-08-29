package slides

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/store"
)

func TestCatalogResolvePrefersPersonalOverOrg(t *testing.T) {
	ctx := context.Background()
	org := store.NewMemorySlideTemplateStore()
	personal := store.NewMemorySlideTemplateStore()
	if err := org.Save(ctx, RecordFromTemplate(themes.Template{Name: "acme", Label: "Org Acme"})); err != nil {
		t.Fatal(err)
	}
	if err := personal.Save(ctx, RecordFromTemplate(themes.Template{Name: "acme", Label: "My Acme"})); err != nil {
		t.Fatal(err)
	}
	cat := TemplateCatalog{Org: org, Personal: personal}
	got, ok, err := cat.Resolve(ctx, "acme")
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if got.Label != "My Acme" || got.Scope != ScopePersonal {
		t.Fatalf("got %+v", got)
	}
}

func TestCatalogListAllKeepsSameNameAtTwoScopes(t *testing.T) {
	ctx := context.Background()
	org := store.NewMemorySlideTemplateStore()
	personal := store.NewMemorySlideTemplateStore()
	_ = org.Save(ctx, RecordFromTemplate(themes.Template{Name: "acme", Label: "Org Acme"}))
	_ = personal.Save(ctx, RecordFromTemplate(themes.Template{Name: "acme", Label: "My Acme"}))
	all, err := (TemplateCatalog{Org: org, Personal: personal}).ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var orgHit, personalHit bool
	for _, tplt := range all {
		if tplt.Name == "acme" && tplt.Scope == ScopeOrg {
			orgHit = true
		}
		if tplt.Name == "acme" && tplt.Scope == ScopePersonal {
			personalHit = true
		}
	}
	if !orgHit || !personalHit {
		t.Fatalf("expected both org and personal acme, got %#v", all)
	}
}

func TestCatalogBuiltinNameAlwaysWins(t *testing.T) {
	ctx := context.Background()
	personal := store.NewMemorySlideTemplateStore()
	_ = personal.Save(ctx, RecordFromTemplate(themes.Template{Name: "classic", Label: "Fake Classic"}))
	got, ok, err := (TemplateCatalog{Personal: personal}).Resolve(ctx, "classic")
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if got.Scope != ScopeBuiltin || got.Label == "Fake Classic" {
		t.Fatalf("builtin must win, got scope=%s label=%s", got.Scope, got.Label)
	}
}
