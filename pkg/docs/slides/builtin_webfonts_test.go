package slides

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

func TestHydrateTemplateFontsFillsDeclaredFaces(t *testing.T) {
	tmpl, ok := themes.LookupTemplate("modern")
	if !ok {
		t.Fatal("modern template missing")
	}
	if len(tmpl.Assets) != 0 {
		t.Fatalf("built-in assets should be empty before hydrate, got %d", len(tmpl.Assets))
	}
	got := HydrateTemplateFonts(tmpl)
	for _, key := range []string{
		"font:Manrope:400", "font:Manrope:800",
		"font:JetBrains Mono:400", "font:JetBrains Mono:600",
	} {
		uri := got.Assets[key]
		if !strings.HasPrefix(uri, "data:font/woff2;base64,") {
			t.Errorf("%s missing or not woff2 data URI", key)
		}
	}
	if got.Assets["font:Manrope:300"] != "" {
		t.Error("undeclared Manrope 300 must not be filled")
	}
	if len(tmpl.Assets) != 0 {
		t.Fatal("hydrate must not mutate the built-in template")
	}
}

func TestHydrateTemplateFontsSkipsUndeclaredTemplates(t *testing.T) {
	tmpl, ok := themes.LookupTemplate("aurora")
	if !ok {
		t.Fatal("aurora template missing")
	}
	got := HydrateTemplateFonts(tmpl)
	if len(got.Assets) != 0 {
		t.Fatalf("aurora declares no fonts, hydrate filled %#v", got.Assets)
	}
}

func TestFillDeclaredFontAssetsOnlyHonorsManifest(t *testing.T) {
	theme := map[string]string{
		"displayFont":         "Manrope",
		embeddedFontsThemeKey: `[{"family":"JetBrains Mono","variant":"400","assetKey":"font:JetBrains Mono:400"}]`,
	}
	assets := map[string]string{}
	fillDeclaredFontAssets(theme, assets)
	if assets["font:JetBrains Mono:400"] == "" {
		t.Fatal("declared JetBrains Mono 400 should fill from the library")
	}
	if assets["font:Manrope:400"] != "" {
		t.Fatal("Manrope must not load unless the deck declared it")
	}
}

func TestInheritDeclaredFontsFromTemplate(t *testing.T) {
	theme := map[string]string{
		themeKeyTemplateName: "modern",
		"displayFont":        "Manrope",
	}
	inheritDeclaredFontsFromTemplate(theme)
	refs := parseEmbeddedFonts(theme)
	if len(refs) == 0 {
		t.Fatal("modern decks without a stored manifest should inherit the template declaration")
	}
	seen := map[string]bool{}
	for _, r := range refs {
		seen[r.Family] = true
	}
	if !seen["Manrope"] || !seen["JetBrains Mono"] {
		t.Fatalf("inherited families = %#v", seen)
	}

	keep := `[{"family":"JetBrains Mono","variant":"400","assetKey":"font:JetBrains Mono:400"}]`
	partial := map[string]string{
		themeKeyTemplateName:  "modern",
		embeddedFontsThemeKey: keep,
	}
	inheritDeclaredFontsFromTemplate(partial)
	if partial[embeddedFontsThemeKey] != keep {
		t.Fatal("an existing declaration must not be replaced by the template")
	}

	aurora := map[string]string{themeKeyTemplateName: "aurora", "displayFont": "Manrope"}
	inheritDeclaredFontsFromTemplate(aurora)
	if aurora[embeddedFontsThemeKey] != "" {
		t.Fatal("templates that declare no fonts must not gain a manifest")
	}
}
