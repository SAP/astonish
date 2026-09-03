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

func TestAttrIntFromPreservesNegativeValues(t *testing.T) {
	if got := attrIntFrom(`<ast-image x="-50" y="20">`, "x"); got != -50 {
		t.Fatalf("attrIntFrom(x) = %d, want -50", got)
	}
}

func TestSetIntAttrDoesNotMatchLongerAttributeName(t *testing.T) {
	got := setIntAttr(`cx="100" rx="20"`, "x", -50)
	if got != `cx="100" rx="20" x="-50"` {
		t.Fatalf("setIntAttr() = %q", got)
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

func TestMissingTextSlotFills(t *testing.T) {
	arch := themes.Archetype{
		FillSlots: []string{"ph-1", "ph-2", "ph-pic-3"},
		SlotHints: []themes.SlotHint{
			{ID: "ph-1", Role: "title"},
			{ID: "ph-2", Role: "body"},
			{ID: "ph-pic-3", Role: "image"},
		},
	}
	miss := missingTextSlotFills(arch, map[string]string{"ph-1": "Title"})
	if len(miss) != 1 || miss[0] != "ph-2" {
		t.Fatalf("missing = %#v, want [ph-2]", miss)
	}
	if miss := missingTextSlotFills(arch, map[string]string{"ph-1": "Title", "ph-2": "Body"}); len(miss) != 0 {
		t.Fatalf("fully filled text slots still missing %#v", miss)
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

func TestFindTemplateArchetypeResolvesRecipes(t *testing.T) {
	tmpl := themes.Template{Tokens: map[string]string{"surface": "#FFFFFF", "ink": "#111", "accent": "#00F"}}
	a, err := findTemplateArchetype(tmpl, RecipeCover, "")
	if err != nil || a.Kind != RecipeCover {
		t.Fatalf("recipe kind: %#v %v", a, err)
	}
	if len(a.FillSlots) == 0 || a.FillSlots[0] != "eyebrow" {
		t.Fatalf("named slots: %#v", a.FillSlots)
	}
	a, err = findTemplateArchetype(tmpl, "", "Three-up cards")
	if err != nil || a.Kind != RecipeThreeUp {
		t.Fatalf("recipe label: %#v %v", a, err)
	}
}

func TestFillArchetypeMarkupIgnoresUnknownSlots(t *testing.T) {
	markup := `<ast-slide id="p"><ast-text id="headline" x="10" y="10" w="400" h="80" size="24"></ast-text></ast-slide>`
	out, err := fillArchetypeMarkup(markup, map[string]string{
		"headline":     "Steve Jobs",
		"meta_4_label": "Legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Steve Jobs") {
		t.Fatalf("headline fill missing:\n%s", out)
	}
}

func TestFindElementDoesNotMatchPrefixIDs(t *testing.T) {
	markup := `<ast-slide id="p"><ast-text id="headline_2" x="1" y="1" w="10" h="10"></ast-text><ast-text id="headline" x="1" y="20" w="10" h="10"></ast-text></ast-slide>`
	start, _, _, _, _, _, ok := findElement(markup, "headline")
	if !ok {
		t.Fatal("headline not found")
	}
	snip := markup[start:]
	if !strings.HasPrefix(snip, `<ast-text id="headline"`) {
		t.Fatalf("matched wrong element: %s", snip[:min(60, len(snip))])
	}
}

func TestProductCatalogOmitsSkinMissingSlots(t *testing.T) {
	tmpl, ok := themes.LookupTemplate("modern")
	if !ok {
		t.Fatal("missing product template")
	}
	cat := catalogFromTemplate(tmpl)
	var cover, closer ArchetypeCatalogEntry
	for _, e := range cat {
		switch e.Kind {
		case RecipeCover:
			cover = e
		case RecipeCloser:
			closer = e
		}
	}
	if cover.Kind == "" || closer.Kind == "" {
		t.Fatalf("missing cover/closer in catalog: %#v", cat)
	}
	for _, id := range cover.FillSlots {
		if strings.HasPrefix(id, "meta_4") {
			t.Fatalf("product cover fillSlots includes %s: %#v", id, cover.FillSlots)
		}
	}
	for _, h := range cover.SlotHints {
		if strings.HasPrefix(h.ID, "meta_4") {
			t.Fatalf("product cover slotHints includes %s", h.ID)
		}
	}
	foundThesis, foundItem := false, false
	for _, id := range closer.FillSlots {
		if id == "thesis" {
			foundThesis = true
		}
		if id == "item_1_title" {
			foundItem = true
		}
	}
	if !foundThesis {
		t.Fatalf("product closer must require thesis, fillSlots=%v", closer.FillSlots)
	}
	if foundItem {
		t.Fatalf("product closer takeaways must be optional, not required chips: %#v", closer.FillSlots)
	}
	wantCover := []string{"eyebrow", "headline", "dek", "meta_1_label", "meta_1_value", "meta_2_label", "meta_2_value"}
	for _, id := range wantCover {
		found := false
		for _, got := range cover.FillSlots {
			if got == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("product cover missing required %s: %#v", id, cover.FillSlots)
		}
	}
	for _, id := range cover.FillSlots {
		if id == "ph-pic-1" {
			t.Fatalf("optional logo must not be a required fillSlot: %#v", cover.FillSlots)
		}
	}
	foundLogo := false
	for _, h := range cover.SlotHints {
		if h.ID == "ph-pic-1" {
			foundLogo = true
			if !strings.EqualFold(h.Role, "optional") && !strings.EqualFold(h.Role, "image") {
				t.Fatalf("logo slot role = %q", h.Role)
			}
		}
	}
	if !foundLogo {
		t.Fatal("product cover should advertise optional ph-pic-1 logo in slotHints")
	}
}

func TestProductCatalogOmitsGenericTitle(t *testing.T) {
	tmpl, ok := themes.LookupTemplate("modern")
	if !ok {
		t.Fatal("missing product")
	}
	cat := catalogFromTemplate(tmpl)
	for _, e := range cat {
		base := stripVariantSuffix(e.Kind)
		if base == "title" || base == "closing" || base == "pattern" || base == "section" {
			t.Fatalf("product catalog should omit generic bookends and samples, got %s", e.Kind)
		}
	}
}

func TestRestoreOfficialPictureWellFromModel(t *testing.T) {
	tmpl := themes.Template{
		Name:   "gco",
		Tokens: map[string]string{"muted": "#89D1FF", "surface": "#FFFFFF"},
		Model: &themes.TemplateModel{
			Layouts: []themes.IRLayout{{
				Name: "White cover with blue pattern",
				Placeholders: []themes.IRPlaceholder{
					{Name: "title-1", Type: "title", X: 45, Y: 426, W: 857, H: 157},
					{Name: "image-2", Type: "image", X: 957, Y: 0, W: 963, H: 1080, OOXMLType: "pic"},
				},
			}},
		},
		Archetypes: []themes.Archetype{{
			Kind: "title-2", Title: "White cover with blue pattern", Tier: "fixed",
			Markup:    `<ast-slide id="white"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#FFFFFF" decorative="true"></ast-shape><ast-text id="ph-1" x="45" y="426" w="857" h="157">{{TITLE}}</ast-text></ast-slide>`,
			FillSlots: []string{"ph-1"},
		}},
	}
	arch, err := findTemplateArchetype(tmpl, "title-2", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(arch.Markup, `id="ph-pic-1"`) {
		t.Fatalf("expected restored picture well:\n%s", arch.Markup)
	}
	if !strings.Contains(arch.Markup, `fill="#89D1FF"`) {
		t.Fatalf("picture well should use template muted:\n%s", arch.Markup)
	}
	if !strings.Contains(arch.Markup, `x="957"`) || !strings.Contains(arch.Markup, `w="963"`) {
		t.Fatalf("picture well geometry: %s", arch.Markup)
	}
	got := catalogFromTemplate(tmpl)
	var e ArchetypeCatalogEntry
	for _, c := range got {
		if c.Kind == "title-2" {
			e = c
		}
	}
	found := false
	for _, id := range e.FillSlots {
		if id == "ph-pic-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("catalog should list restored ph-pic-1: %#v", e.FillSlots)
	}
}

func TestStripUnselectedHeroPhotosKeepsLogos(t *testing.T) {
	markup := `<ast-slide id="t">` +
		`<ast-image id="hero" x="0" y="0" w="1920" h="1080" asset-ref="sha256-bike"></ast-image>` +
		`<ast-image id="ph-pic-2" x="960" y="0" w="960" h="1080" asset-ref="sha256-bike"></ast-image>` +
		`<ast-image id="logo" x="20" y="20" w="100" h="40" asset-ref="sha256-logo"></ast-image>` +
		`</ast-slide>`
	got := stripUnselectedHeroPhotos(markup, nil, "#89D1FF")
	if strings.Contains(got, `asset-ref="sha256-bike"`) {
		t.Fatalf("hero photos should be gone:\n%s", got)
	}
	if !strings.Contains(got, `id="ph-pic-2"`) || !strings.Contains(got, `fill="#89D1FF"`) {
		t.Fatalf("pic well should be muted shape:\n%s", got)
	}
	if !strings.Contains(got, `asset-ref="sha256-logo"`) {
		t.Fatalf("logo should remain:\n%s", got)
	}
}

func TestAliasOfficialBookendFillsMapsHeadlineDek(t *testing.T) {
	arch := themes.Archetype{
		Kind:      "title-2",
		Tier:      "fixed",
		Markup:    `<ast-slide id="t"><ast-text id="ph-1" x="0" y="0" w="100" h="40">{{TITLE}}</ast-text><ast-text id="ph-2" x="0" y="50" w="100" h="40">{{BODY}}</ast-text></ast-slide>`,
		FillSlots: []string{"ph-1", "ph-2"},
	}
	got := aliasOfficialBookendFills(arch, map[string]string{"headline": "Cover", "dek": "Subtitle"})
	if got["ph-1"] != "Cover" || got["ph-2"] != "Subtitle" {
		t.Fatalf("aliases: %#v", got)
	}
	out, err := fillArchetypeMarkup(arch.Markup, got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Cover") || !strings.Contains(out, "Subtitle") {
		t.Fatalf("filled:\n%s", out)
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

func TestSetElementTextEscapesAndRejectsNonText(t *testing.T) {
	markup := `<ast-slide id="s0"><ast-text id="headline" x="1" y="1" w="10" h="10">Title</ast-text><ast-shape id="card" x="1" y="1" w="10" h="10"></ast-shape></ast-slide>`
	got, err := setElementText(markup, "headline", `Hello & <world>`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Hello &amp; &lt;world&gt;") {
		t.Fatalf("escaped text missing: %s", got)
	}
	if _, err := setElementText(markup, "card", "nope"); err == nil {
		t.Fatal("expected non-text reject")
	}
	if _, err := setElementText(markup, "missing", "nope"); err == nil {
		t.Fatal("expected missing id")
	}
}

func TestRemoveElementDropsMarkup(t *testing.T) {
	markup := `<ast-slide id="s0"><ast-text id="headline" x="1" y="1" w="10" h="10">Title</ast-text><ast-text id="dek" x="1" y="20" w="10" h="10">Sub</ast-text></ast-slide>`
	got, err := removeElement(markup, "dek")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, `id="dek"`) {
		t.Fatalf("dek still present: %s", got)
	}
	if !strings.Contains(got, `id="headline"`) {
		t.Fatalf("headline removed: %s", got)
	}
}

func TestSetStringAttr(t *testing.T) {
	// Replace existing
	got := setStringAttr(`id="foo" fill="blue"`, "fill", "red")
	if !strings.Contains(got, `fill="red"`) {
		t.Fatalf("expected fill=red, got %s", got)
	}
	// Add new attr
	got = setStringAttr(`id="foo"`, "fill", "green")
	if !strings.Contains(got, `fill="green"`) {
		t.Fatalf("expected fill=green, got %s", got)
	}
	// Empty attrs
	got = setStringAttr("", "fill", "#ff0000")
	if got != `fill="#ff0000"` {
		t.Fatalf("expected fill=#ff0000, got %s", got)
	}
	// HTML escaping
	got = setStringAttr("", "fill", `a"b<c`)
	if !strings.Contains(got, "a&#34;b&lt;c") {
		t.Fatalf("expected escaped value, got %s", got)
	}
}

func TestRewriteElementAttrs(t *testing.T) {
	markup := `<ast-slide><ast-shape id="s1" kind="rect" x="10" y="20" w="100" h="50" fill="blue"></ast-shape></ast-slide>`
	got, err := rewriteElementAttrs(markup, "s1", map[string]string{"fill": "red", "opacity": "0.5"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `fill="red"`) {
		t.Fatalf("expected fill=red in %s", got)
	}
	if !strings.Contains(got, `opacity="0.5"`) {
		t.Fatalf("expected opacity=0.5 in %s", got)
	}
}

func TestRewriteElementAttrsRejectsDisallowed(t *testing.T) {
	markup := `<ast-slide><ast-shape id="s1" kind="rect" x="10" y="20" w="100" h="50"></ast-shape></ast-slide>`
	_, err := rewriteElementAttrs(markup, "s1", map[string]string{"onclick": "alert(1)"})
	if err == nil {
		t.Fatal("expected error for disallowed attribute")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected 'not allowed' in error, got: %s", err)
	}
	_, err = rewriteElementAttrs(markup, "s1", map[string]string{"style": "color:red"})
	if err == nil {
		t.Fatal("expected error for style attribute")
	}
}

func TestInsertElement(t *testing.T) {
	markup := `<ast-slide id="s"><ast-text id="t1" x="0" y="0" w="100" h="50">Hello</ast-text></ast-slide>`
	got, err := insertElement(markup, "ast-shape", map[string]string{
		"id": "user-rect-1", "kind": "rect", "x": "100", "y": "100", "w": "200", "h": "150", "fill": "#4F46E5", "geom": "rect",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `id="user-rect-1"`) {
		t.Fatalf("expected user-rect-1 in %s", got)
	}
	if !strings.Contains(got, "</ast-slide>") {
		t.Fatalf("expected closing tag preserved in %s", got)
	}
	// Verify element is before </ast-slide>
	shapeIdx := strings.Index(got, `id="user-rect-1"`)
	closeIdx := strings.Index(got, "</ast-slide>")
	if shapeIdx > closeIdx {
		t.Fatal("inserted element should be before </ast-slide>")
	}

	// ast-text with content
	got, err = insertElement(markup, "ast-text", map[string]string{
		"id": "user-text-1", "x": "50", "y": "50", "w": "400", "h": "60", "size": "32",
	}, "Text")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, ">Text</ast-text>") {
		t.Fatalf("expected text content in %s", got)
	}
}

func TestInsertElementValidatesTag(t *testing.T) {
	markup := `<ast-slide id="s"></ast-slide>`
	_, err := insertElement(markup, "div", map[string]string{"id": "x"}, "")
	if err == nil {
		t.Fatal("expected error for div tag")
	}
	_, err = insertElement(markup, "script", map[string]string{"id": "x"}, "")
	if err == nil {
		t.Fatal("expected error for script tag")
	}
}
