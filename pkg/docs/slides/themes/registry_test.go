package themes

import "testing"

func TestListTemplatesDeterministicOrder(t *testing.T) {
	got := ListTemplates()
	if len(got) != 3 {
		t.Fatalf("expected 3 built-in templates, got %d", len(got))
	}
	want := []string{"aurora", "light-corporate", "midnight"}
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
}
