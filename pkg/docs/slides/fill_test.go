package slides

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

func TestFillArchetypeMarkupReplacesTextAndPreservesChrome(t *testing.T) {
	markup := `<ast-slide id="p"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-shape id="c-1" kind="rect" x="80" y="200" w="500" h="300" geom="roundRect" fill="#DBEAFE"></ast-shape>` +
		`<ast-text id="ph-1" x="160" y="80" w="1600" h="100" size="54" color="#111"><ast-run>{{TITLE}}</ast-run></ast-text>` +
		`<ast-text id="ph-2" x="100" y="220" w="460" h="80" size="28" color="#111"><ast-run>{{BODY}}</ast-run></ast-text></ast-slide>`
	out, err := fillArchetypeMarkup(markup, map[string]string{
		"ph-1": "Revenue grew 23%",
		"ph-2": "Enterprise renewals",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `geom="roundRect"`) || !strings.Contains(out, `fill="#DBEAFE"`) {
		t.Fatalf("chrome roundRect was not preserved:\n%s", out)
	}
	if strings.Contains(out, "{{TITLE}}") || strings.Contains(out, "{{BODY}}") {
		t.Fatalf("placeholders remain:\n%s", out)
	}
	if !strings.Contains(out, "Revenue grew 23%") || !strings.Contains(out, "Enterprise renewals") {
		t.Fatalf("fills not applied:\n%s", out)
	}
	if !strings.Contains(out, `id="c-1"`) {
		t.Fatalf("chrome id lost:\n%s", out)
	}
}

func TestFillArchetypeMarkupRejectsLeftoverPlaceholders(t *testing.T) {
	markup := `<ast-slide id="p"><ast-text id="ph-1" x="1" y="1" w="10" h="10"><ast-run>{{TITLE}}</ast-run></ast-text>` +
		`<ast-text id="ph-2" x="1" y="20" w="10" h="10"><ast-run>{{BODY}}</ast-run></ast-text></ast-slide>`
	if _, err := fillArchetypeMarkup(markup, map[string]string{"ph-1": "Only title"}); err == nil {
		t.Fatal("expected error for leftover {{BODY}}")
	}
}

func TestFillArchetypeMarkupReplacesImageSlot(t *testing.T) {
	markup := `<ast-slide id="p"><ast-shape id="ph-pic-2" kind="rect" x="10" y="20" w="30" h="40" geom="rect" fill="#EEE" alt="Image"></ast-shape>` +
		`<ast-text id="ph-1" x="1" y="1" w="10" h="10"><ast-run>{{TITLE}}</ast-run></ast-text></ast-slide>`
	out, err := fillArchetypeMarkup(markup, map[string]string{
		"ph-1":     "Title",
		"ph-pic-2": "sha256-deadbeef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `<ast-shape id="ph-pic-2"`) {
		t.Fatalf("image drop-slot shape should have been replaced:\n%s", out)
	}
	if !strings.Contains(out, `<ast-image id="ph-pic-2"`) || !strings.Contains(out, `asset-ref="sha256-deadbeef"`) {
		t.Fatalf("expected ast-image with asset-ref:\n%s", out)
	}
	if !strings.Contains(out, `x="10"`) || !strings.Contains(out, `y="20"`) {
		t.Fatalf("image geometry not preserved:\n%s", out)
	}
}

func TestFillArchetypeMarkupRejectsPlaceholderDummy(t *testing.T) {
	markup := `<ast-slide id="p"><ast-text id="ph-1" x="10" y="10" w="400" h="80" size="24"><ast-run>{{TITLE}}</ast-run></ast-text></ast-slide>`
	if _, err := fillArchetypeMarkup(markup, map[string]string{"ph-1": "<initials> <date>yyyy-MM-dd</date>:"}); err == nil {
		t.Fatal("expected error for leftover date-token fill")
	}
}

func TestCollectAssetRefsAndLightweightSeed(t *testing.T) {
	markup := `<ast-slide id="p"><ast-image id="a" asset-ref="sha256-aaa" x="1" y="1" w="2" h="2"></ast-image>` +
		`<ast-image id="b" asset-ref="sha256-aaa" x="1" y="1" w="2" h="2"></ast-image>` +
		`<ast-image id="c" asset-ref="sha256-bbb" x="1" y="1" w="2" h="2"></ast-image></ast-slide>`
	got := collectAssetRefs(markup)
	if len(got) != 2 || got[0] != "sha256-aaa" || got[1] != "sha256-bbb" {
		t.Fatalf("collectAssetRefs = %#v", got)
	}
	src := map[string]string{
		"font:Brand:regular": "data:font/ttf;base64,AA",
		"sha256-photo":       "data:image/png;base64,BB",
	}
	seed := seedLightweightAssets(src)
	if len(seed) != 1 || seed["font:Brand:regular"] != "data:font/ttf;base64,AA" {
		t.Fatalf("seedLightweightAssets = %#v", seed)
	}
}

func TestLooksLikePlaceholderFillGenericPrompts(t *testing.T) {
	for _, s := range []string{
		"Contact information:",
		"Title goes here",
		"and here and here",
		"Name, Acme",
		"Month 00, 2026",
		"Add company logo",
		"Speaker name",
	} {
		if !looksLikePlaceholderFill(s) {
			t.Errorf("%q should be treated as leftover template prompt", s)
		}
	}
	for _, s := range []string{
		"The Life of Steve Jobs",
		"Thank You",
		"NeXT Computer",
		"INTERNAL – partners only",
	} {
		if looksLikePlaceholderFill(s) {
			t.Errorf("%q should be allowed as real content", s)
		}
	}
}

func TestFillArchetypeMarkupShrinksOverflowingText(t *testing.T) {
	markup := `<ast-slide id="p"><ast-text id="ph-1" x="10" y="10" w="200" h="40" size="40"><ast-run>{{TITLE}}</ast-run></ast-text></ast-slide>`
	out, err := fillArchetypeMarkup(markup, map[string]string{
		"ph-1": "A much longer headline than this tiny bar can hold at 40px",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `size="40"`) {
		t.Fatalf("expected font size to shrink to fit the box:\n%s", out)
	}
	if !strings.Contains(out, "A much longer headline") {
		t.Fatalf("fill text missing:\n%s", out)
	}
}

func TestFindTemplateArchetype(t *testing.T) {
	tmpl := themes.Template{Archetypes: []themes.Archetype{
		{Kind: "title", Title: "Blue cover"},
		{Kind: "pattern", Title: "3 rounded cards"},
		{Kind: "pattern-2", Title: "Icon row with images"},
	}}
	a, err := findTemplateArchetype(tmpl, "pattern-2", "")
	if err != nil || a.Title != "Icon row with images" {
		t.Fatalf("kind lookup: %#v %v", a, err)
	}
	a, err = findTemplateArchetype(tmpl, "", "3 rounded cards")
	if err != nil || a.Kind != "pattern" {
		t.Fatalf("label lookup: %#v %v", a, err)
	}
	if _, err := findTemplateArchetype(tmpl, "missing", ""); err == nil {
		t.Fatal("expected missing kind error")
	}
}

func TestCatalogFromOmitsMarkup(t *testing.T) {
	cat := catalogFrom([]themes.Archetype{{
		Kind: "pattern", Title: "3 rounded cards", Tier: "flexible",
		Markup:    `<ast-slide id="p"><ast-shape geom="roundRect"></ast-shape><ast-shape geom="roundRect"></ast-shape></ast-slide>`,
		FillSlots: []string{"ph-1", "ph-2"},
	}})
	if len(cat) != 1 {
		t.Fatalf("got %d", len(cat))
	}
	if cat[0].Kind != "pattern" || cat[0].Label != "3 rounded cards" {
		t.Fatalf("catalog entry: %#v", cat[0])
	}
	if !strings.Contains(cat[0].Summary, "rounded cards") {
		t.Fatalf("summary should mention cards: %q", cat[0].Summary)
	}
}
