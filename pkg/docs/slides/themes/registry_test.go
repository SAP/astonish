package themes

import (
	"strings"
	"testing"
)

func TestListTemplatesDeterministicOrder(t *testing.T) {
	got := ListTemplates()
	if len(got) != 4 {
		t.Fatalf("expected 4 built-in templates, got %d", len(got))
	}
	want := []string{"aurora", "light-corporate", "midnight", "product"}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("template %d = %q, want %q", i, got[i].Name, name)
		}
		if got[i].Schema != 2 {
			t.Fatalf("template %q schema = %d, want 2", got[i].Name, got[i].Schema)
		}
		if len(got[i].Archetypes) != 3 {
			t.Fatalf("template %q has %d archetypes, want 3", got[i].Name, len(got[i].Archetypes))
		}
		kinds := map[string]bool{}
		for _, a := range got[i].Archetypes {
			kinds[a.Kind] = true
		}
		for _, k := range []string{"title", "section", "content"} {
			if !kinds[k] {
				t.Fatalf("template %q missing archetype kind %q", got[i].Name, k)
			}
		}
	}
}

func TestLookupTemplate(t *testing.T) {
	tmpl, ok := LookupTemplate("midnight")
	if !ok {
		t.Fatal("expected midnight template to exist")
	}
	if tmpl.Tokens["surface"] != "#0B1220" || tmpl.Tokens["accent"] != "#F59E0B" {
		t.Fatalf("unexpected midnight tokens: %#v", tmpl.Tokens)
	}
	if tmpl.ThemeTokens()["ink"] != "#E2E8F0" {
		t.Fatalf("ThemeTokens mismatch: %#v", tmpl.ThemeTokens())
	}
	if _, ok := LookupTemplate("does-not-exist"); ok {
		t.Fatal("expected missing template to report ok=false")
	}
	prod, ok := LookupTemplate("product")
	if !ok {
		t.Fatal("expected product template to exist")
	}
	if prod.Skin != "product" || prod.Tokens["accent"] != "#8B5CF6" {
		t.Fatalf("unexpected product template: %#v", prod)
	}
	if prod.Label != "Product Deck" {
		t.Fatalf("product label = %q, want Product Deck", prod.Label)
	}
	if len(prod.Palettes) < 8 {
		t.Fatalf("product palettes = %d, want >= 8", len(prod.Palettes))
	}
	if _, ok := prod.PaletteByID("orange"); !ok {
		t.Fatal("product missing orange palette")
	}
	if _, ok := prod.PaletteByID("editorial"); !ok {
		t.Fatal("product missing editorial palette")
	}
	seen := map[string]bool{}
	for _, p := range prod.Palettes {
		if p.ID == "" || p.Label == "" || p.Tokens["accent"] == "" {
			t.Fatalf("invalid palette: %#v", p)
		}
		if seen[p.ID] {
			t.Fatalf("duplicate palette id %q", p.ID)
		}
		seen[p.ID] = true
	}
}

func TestArchetypesForMatchesInternal(t *testing.T) {
	got := ArchetypesFor("#101820", "#F2F2F2", "#FFB81C")
	if len(got) != 3 {
		t.Fatalf("expected 3 archetypes, got %d", len(got))
	}
	kinds := map[string]string{}
	for _, a := range got {
		kinds[a.Kind] = a.Markup
	}
	for _, k := range []string{"title", "section", "content"} {
		markup, ok := kinds[k]
		if !ok {
			t.Fatalf("missing archetype kind %q", k)
		}
		// The palette colors must be embedded in the regenerated markup so
		// previews reflect the new colors.
		if !strings.Contains(markup, "#101820") {
			t.Fatalf("archetype %q missing surface color: %s", k, markup)
		}
	}
	// The exported wrapper must equal the internal builder for the default title.
	want := archetypesFor("#101820", "#F2F2F2", "#FFB81C", "")
	if len(want) != len(got) {
		t.Fatalf("ArchetypesFor length %d != archetypesFor length %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].Markup != want[i].Markup {
			t.Fatalf("archetype %d mismatch between ArchetypesFor and archetypesFor", i)
		}
	}
}
