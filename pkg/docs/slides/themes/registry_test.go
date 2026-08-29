package themes

import (
	"strings"
	"testing"
)

func TestListTemplatesDeterministicOrder(t *testing.T) {
	got := ListTemplates()
	if len(got) != 2 {
		t.Fatalf("expected 2 built-in templates, got %d", len(got))
	}
	want := []string{"classic", "modern"}
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
	tmpl, ok := LookupTemplate("classic")
	if !ok {
		t.Fatal("expected classic template to exist")
	}
	if tmpl.Tokens["surface"] != "#FFFFFF" || tmpl.Tokens["accent"] != "#1E40AF" {
		t.Fatalf("unexpected classic tokens: %#v", tmpl.Tokens)
	}
	if tmpl.ThemeTokens()["ink"] != "#172033" {
		t.Fatalf("ThemeTokens mismatch: %#v", tmpl.ThemeTokens())
	}
	if tmpl.Skin != "corporate" {
		t.Fatalf("classic skin = %q, want corporate", tmpl.Skin)
	}
	if _, ok := LookupTemplate("does-not-exist"); ok {
		t.Fatal("expected missing template to report ok=false")
	}
	prod, ok := LookupTemplate("modern")
	if !ok {
		t.Fatal("expected modern template to exist")
	}
	if prod.Name != "modern" || prod.Skin != "product" || prod.Tokens["accent"] != "#8B5CF6" {
		t.Fatalf("unexpected modern template: %#v", prod)
	}
	if prod.Label != "Modern" {
		t.Fatalf("modern label = %q, want Modern", prod.Label)
	}
	if len(prod.Palettes) < 8 {
		t.Fatalf("modern palettes = %d, want >= 8", len(prod.Palettes))
	}
	if _, ok := prod.PaletteByID("orange"); !ok {
		t.Fatal("modern missing orange palette")
	}
	if _, ok := prod.PaletteByID("editorial"); !ok {
		t.Fatal("modern missing editorial palette")
	}
	if !strings.Contains(prod.Tokens["embedded-fonts"], `"family":"Manrope"`) {
		t.Fatal("modern must declare the faces it needs")
	}
	if !strings.Contains(prod.Tokens["embedded-fonts"], `"family":"JetBrains Mono"`) {
		t.Fatal("modern must declare JetBrains Mono")
	}
	if tmpl.Tokens["embedded-fonts"] != "" {
		t.Fatal("classic must not declare fonts it does not need")
	}
	if len(tmpl.Palettes) != 3 {
		t.Fatalf("classic palettes = %d, want 3", len(tmpl.Palettes))
	}
	for _, id := range []string{"light", "midnight", "aurora"} {
		if _, ok := tmpl.PaletteByID(id); !ok {
			t.Fatalf("classic missing palette %q", id)
		}
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

func TestLookupTemplateAliases(t *testing.T) {
	for _, name := range []string{"aurora", "midnight", "light-corporate"} {
		got, ok := LookupTemplate(name)
		if !ok {
			t.Fatalf("%s should alias to classic", name)
		}
		if got.Name != "classic" {
			t.Fatalf("%s resolved to %q, want classic", name, got.Name)
		}
	}
	if CanonicalTemplateName("midnight") != "classic" {
		t.Fatal("CanonicalTemplateName(midnight)")
	}
	if AliasPaletteID("midnight") != "midnight" || AliasPaletteID("light-corporate") != "light" {
		t.Fatalf("AliasPaletteID: midnight=%q light-corporate=%q", AliasPaletteID("midnight"), AliasPaletteID("light-corporate"))
	}
	pal, ok := LookupTemplate("classic")
	if !ok {
		t.Fatal("classic missing")
	}
	mid, ok := pal.PaletteByID("midnight")
	if !ok || mid.Tokens["surface"] != "#0B1220" {
		t.Fatalf("midnight palette: %#v", mid)
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
