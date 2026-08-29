package slides

import (
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

func fixtureTokens() map[string]string {
	return map[string]string{
		"surface":     "#FFFFFF",
		"ink":         "#172033",
		"accent":      "#1E40AF",
		"displayFont": "Georgia",
		"bodyFont":    "Calibri",
	}
}

func sampleFills(kind string) map[string]string {
	m, ok := recipeByKind(kind)
	if !ok {
		return nil
	}
	fills := map[string]string{}
	for _, s := range m.Slots {
		if strings.EqualFold(s.Role, "optional") {
			continue
		}
		switch s.Role {
		case "label":
			fills[s.ID] = "Chapter One"
		case "title":
			fills[s.ID] = "Given away, then chosen by craft"
		case "heading":
			fills[s.ID] = "Craft"
		default:
			fills[s.ID] = "Paul was a machinist who taught that the unseen back of a cabinet still had to be finished properly."
		}
	}
	return fills
}

func TestRenderRecipeParsesAllTypes(t *testing.T) {
	chrome := Chrome{DeckTitle: "A life in twelve chapters", Page: 2, Total: 12}
	skins := []struct {
		name string
		skin Skin
	}{
		{"corporate", CorporateSkin(fixtureTokens())},
		{"product", ProductSkin(nil)},
	}
	for _, sc := range skins {
		for _, m := range allRecipeMeta() {
			t.Run(sc.name+"/"+m.Kind, func(t *testing.T) {
				skel, err := RenderRecipe(m.Kind, sc.skin, nil, chrome, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, diags, err := ParseSlide(skel); err != nil {
					t.Fatalf("prototype parse: %v\n%s", err, skel)
				} else {
					for _, d := range diags {
						if d.Severity == "error" {
							t.Errorf("prototype diagnostic: %+v", d)
						}
					}
				}
				filledSkel, err := RenderRecipe(m.Kind, sc.skin, nil, chrome, sampleFills(m.Kind))
				if err != nil {
					t.Fatal(err)
				}
				filledMarkup, err := fillArchetypeMarkup(filledSkel, sampleFills(m.Kind))
				if err != nil {
					t.Fatal(err)
				}
				slide, diags, err := ParseSlide(filledMarkup)
				if err != nil {
					t.Fatalf("filled parse: %v\n%s", err, filledMarkup)
				}
				for _, d := range diags {
					if d.Severity == "error" {
						t.Errorf("filled diagnostic: %+v", d)
					}
				}
				assertNoTextOverlap(t, slide)
				assertFooterInBottom(t, slide)
				if !strings.Contains(filledMarkup, `id="eyebrow"`) && m.Kind != "" {
					t.Error("missing eyebrow slot")
				}
				if chrome.DeckTitle != "" && !strings.Contains(filledMarkup, chrome.DeckTitle) && !strings.Contains(filledMarkup, "02") {
					t.Error("expected running footer or page number")
				}
			})
		}
	}
}

func TestRenderRecipeRequiredSlotsPresent(t *testing.T) {
	markup, err := RenderRecipe(RecipeSplitNarrative, CorporateSkin(fixtureTokens()), nil, Chrome{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := recipeByKind(RecipeSplitNarrative)
	for _, id := range requiredFillSlots(m.Slots) {
		if !strings.Contains(markup, `id="`+id+`"`) {
			t.Errorf("missing required slot %s", id)
		}
	}
}

func TestMissingRecipeSlotRejected(t *testing.T) {
	arch, err := recipeArchetypeFor(themes.Template{Tokens: fixtureTokens()}, RecipeSplitNarrative, Chrome{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fills := sampleFills(RecipeSplitNarrative)
	delete(fills, "headline")
	if miss := missingTextSlotFills(arch, fills); len(miss) == 0 {
		t.Fatal("expected missing headline")
	}
}

func TestOptionalHeadline2OmittedWhenEmpty(t *testing.T) {
	fills := sampleFills(RecipeSplitNarrative)
	markup, err := RenderRecipe(RecipeSplitNarrative, CorporateSkin(fixtureTokens()), nil, Chrome{Page: 1}, fills)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markup, `id="headline_2"`) {
		t.Fatal("empty optional headline_2 should be omitted at fill time")
	}
	fills["headline_2"] = "then chosen"
	markup, err = RenderRecipe(RecipeSplitNarrative, CorporateSkin(fixtureTokens()), nil, Chrome{Page: 1}, fills)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markup, `id="headline_2"`) {
		t.Fatal("filled headline_2 should be present")
	}
}

func TestChromeOverlayLogoAndLegal(t *testing.T) {
	tmpl := themes.Template{
		Tokens: fixtureTokens(),
		Archetypes: []themes.Archetype{{
			Kind: "title",
			Markup: `<ast-slide id="t">` +
				`<ast-image id="logo" x="40" y="40" w="180" h="48" asset-ref="sha256-logo"></ast-image>` +
				`<ast-text id="legal" x="40" y="1040" w="800" h="24" size="12"><ast-run>INTERNAL — Partners Only</ast-run></ast-text>` +
				`</ast-slide>`,
		}},
	}
	ch := ExtractChrome(tmpl)
	if ch.LogoRef != "sha256-logo" {
		t.Fatalf("logo ref: %#v", ch)
	}
	if ch.Legal == "" && ch.Confidential == "" {
		t.Fatalf("expected legal or confidential, got %#v", ch)
	}
	if ch.Confidential != "" {
		t.Fatalf("duplicate confidentiality should be footer-only, confidential=%q legal=%q", ch.Confidential, ch.Legal)
	}
	markup, err := RenderRecipe(RecipeCover, CorporateSkin(tmpl.Tokens), nil, ch, sampleFills(RecipeCover))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markup, `asset-ref="sha256-logo"`) {
		t.Fatalf("logo not painted:\n%s", markup)
	}
	if !strings.Contains(markup, "INTERNAL") && !strings.Contains(markup, "Partners") {
		t.Fatalf("legal/confidential not painted:\n%s", markup)
	}
	if n := strings.Count(markup, "INTERNAL"); n != 1 {
		t.Fatalf("confidential stamp painted %d times, want 1:\n%s", n, markup)
	}
	slide, _, err := ParseSlide(markup)
	if err != nil {
		t.Fatal(err)
	}
	logo := nodeByID(slide.Nodes, "chrome-logo")
	eyebrow := nodeByID(slide.Nodes, "eyebrow")
	if logo.ID == "" || eyebrow.ID == "" {
		t.Fatalf("missing logo or eyebrow")
	}
	if eyebrow.Geometry.Y < logo.Geometry.Y+logo.Geometry.H+16 {
		t.Fatalf("eyebrow y=%d tucked under logo y=%d h=%d", eyebrow.Geometry.Y, logo.Geometry.Y, logo.Geometry.H)
	}
}

func TestCatalogFromTemplateLeadsWithRecipes(t *testing.T) {
	tmpl := themes.Template{
		Tokens:     fixtureTokens(),
		Archetypes: []themes.Archetype{{Kind: "title", Title: "Title", Markup: `<ast-slide id="t"></ast-slide>`}},
	}
	cat := catalogFromTemplate(tmpl)
	if len(cat) < 11 {
		t.Fatalf("expected recipes + title, got %d", len(cat))
	}
	if cat[0].Kind != RecipeCover {
		t.Fatalf("first catalog entry should be recipe-cover, got %s", cat[0].Kind)
	}
	if !strings.Contains(cat[0].Summary, "eyebrow") {
		t.Fatalf("summary should name the job: %q", cat[0].Summary)
	}
	kinds := map[string]bool{}
	for _, e := range cat {
		kinds[e.Kind] = true
		if strings.Contains(e.Kind, "recipe") && strings.Contains(strings.ToLower(e.Summary+e.Label), "<ast-slide") {
			t.Fatal("catalog leaked markup")
		}
	}
	for _, m := range allRecipeMeta() {
		if !kinds[m.Kind] {
			t.Errorf("missing %s", m.Kind)
		}
	}
	if !kinds["title"] {
		t.Fatal("expected scoped/unbuilt title bookend in catalog")
	}
}

func TestCatalogFromTemplateDropsPatternsAndSection(t *testing.T) {
	tmpl := themes.Template{
		Name:   "gco",
		Tokens: fixtureTokens(),
		Archetypes: []themes.Archetype{
			{Kind: "title", Title: "Cover A", Tier: "fixed", Markup: `<ast-slide id="t"></ast-slide>`},
			{Kind: "title-2", Title: "Cover B", Tier: "fixed", Markup: `<ast-slide id="t2"></ast-slide>`},
			{Kind: "closing", Title: "End", Tier: "fixed", Markup: `<ast-slide id="c"></ast-slide>`},
			{Kind: "section", Title: "Divider", Tier: "fixed", Markup: `<ast-slide id="s"></ast-slide>`},
			{Kind: "agenda", Title: "Agenda", Tier: "fixed", Markup: `<ast-slide id="a"></ast-slide>`},
			{Kind: "pattern", Title: "Cards", Tier: "flexible", Markup: `<ast-slide id="p"></ast-slide>`},
			{Kind: "content", Title: "Title and Text", Markup: `<ast-slide id="x"></ast-slide>`},
		},
	}
	cat := catalogFromTemplate(tmpl)
	kinds := map[string]bool{}
	for _, e := range cat {
		kinds[e.Kind] = true
	}
	for _, want := range []string{RecipeCover, "title", "title-2", "closing"} {
		if !kinds[want] {
			t.Errorf("catalog missing %s", want)
		}
	}
	for _, drop := range []string{"pattern", "section", "agenda", "content"} {
		if kinds[drop] {
			t.Errorf("catalog should omit %s", drop)
		}
	}
}

func TestRecipeSourceHasNoBrandLiterals(t *testing.T) {
	if strings.Contains(string(mustRead(t, "recipe.go")), "GCO") || strings.Contains(string(mustRead(t, "recipe_layouts.go")), "GCO") {
		t.Error("recipe renderer contains a template-brand literal")
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestPaletteUsesTemplateTokens(t *testing.T) {
	tokens := map[string]string{"surface": "#0B1220", "ink": "#E2E8F0", "accent": "#F59E0B"}
	markup, err := RenderRecipe(RecipeThreeUp, CorporateSkin(tokens), nil, Chrome{Page: 1}, sampleFills(RecipeThreeUp))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markup, `fill="#0B1220"`) {
		t.Fatalf("surface not applied:\n%s", markup)
	}
	if !strings.Contains(markup, "#F59E0B") {
		t.Fatalf("accent not applied:\n%s", markup)
	}
}

func TestPaleMutedIsNotUsedForBodyText(t *testing.T) {
	tokens := map[string]string{
		"surface": "#FFFFFF",
		"ink":     "#172033",
		"accent":  "#1E40AF",
		"muted":   "#89D1FF",
	}
	p := paletteFrom(CorporateSkin(tokens), nil)
	if !textContrastOK(p.secondary, p.surface) {
		t.Fatalf("secondary %s fails contrast on %s", p.secondary, p.surface)
	}
	if strings.EqualFold(p.secondary, "#89D1FF") {
		t.Fatal("pale decorative muted must not be used as body text")
	}
	markup, err := RenderRecipe(RecipeThreeUp, CorporateSkin(tokens), nil, Chrome{Page: 1}, sampleFills(RecipeThreeUp))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markup, `color="#89D1FF"`) {
		t.Fatalf("pale muted used as text color:\n%s", markup)
	}
}

func TestBodyCardsFillToFooter(t *testing.T) {
	markup, err := RenderRecipe(RecipeThreeUp, CorporateSkin(fixtureTokens()), nil, Chrome{Page: 1}, sampleFills(RecipeThreeUp))
	if err != nil {
		t.Fatal(err)
	}
	slide, _, err := ParseSlide(markup)
	if err != nil {
		t.Fatal(err)
	}
	card := nodeByID(slide.Nodes, "card-1")
	if card.ID == "" {
		t.Fatal("missing card-1")
	}
	if card.Geometry.H < 320 || card.Geometry.H > 480 {
		t.Fatalf("three-up card should be a panel, not a full-height empty slab: h=%d", card.Geometry.H)
	}
	quote, err := RenderRecipe(RecipeQuoteSplit, CorporateSkin(fixtureTokens()), nil, Chrome{Page: 1}, sampleFills(RecipeQuoteSplit))
	if err != nil {
		t.Fatal(err)
	}
	qslide, _, err := ParseSlide(quote)
	if err != nil {
		t.Fatal(err)
	}
	qc := nodeByID(qslide.Nodes, "quote-card")
	if qc.Geometry.H < 240 || qc.Geometry.H > 420 {
		t.Fatalf("quote card should hug quote+attribution, h=%d", qc.Geometry.H)
	}
}

func TestProductBackgroundIsGradient(t *testing.T) {
	markup, err := RenderRecipe(RecipeCover, ProductSkin(nil), nil, Chrome{Page: 1, Total: 8}, sampleFills(RecipeCover))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markup, `id="bg-gradient"`) || !strings.Contains(markup, `"kind":"radial"`) {
		t.Fatalf("product cover bg should be a radial gradient:\n%s", markup)
	}
	if !strings.Contains(markup, `"cx":80`) || !strings.Contains(markup, `"cy":8`) {
		t.Fatalf("product cover glare should be top-right:\n%s", markup)
	}
	slide, diags, err := ParseSlide(markup)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Severity == "error" {
			t.Fatalf("gradient bg invalid: %+v", d)
		}
	}
	bg := nodeByID(slide.Nodes, "bg")
	if bg.Gradient == nil || bg.Gradient.Kind != "radial" || len(bg.Gradient.Stops) < 2 {
		t.Fatalf("parsed bg gradient: %#v", bg.Gradient)
	}
	if strings.EqualFold(bg.Gradient.Stops[0].Color, ProductSkin(nil).Surface) {
		t.Fatal("first gradient stop must be a visible accent wash, not the surface")
	}
}

func TestProductBodyHasNoGradientWash(t *testing.T) {
	markup, err := RenderRecipe(RecipeThreeUp, ProductSkin(nil), nil, Chrome{Page: 2, Total: 8}, sampleFills(RecipeThreeUp))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markup, `id="bg-gradient"`) || strings.Contains(markup, `"kind":"radial"`) {
		t.Fatalf("product body slides are solid surface, not a wash:\n%s", markup)
	}
	slide, _, err := ParseSlide(markup)
	if err != nil {
		t.Fatal(err)
	}
	bg := nodeByID(slide.Nodes, "bg")
	if bg.ID == "" {
		t.Fatal("body still needs a solid bg")
	}
	if bg.Gradient != nil {
		t.Fatalf("body bg must not carry a gradient: %#v", bg.Gradient)
	}
}

func TestProductCloserIsCoverLike(t *testing.T) {
	markup, err := RenderRecipe(RecipeCloser, ProductSkin(nil), nil, Chrome{Page: 8, Total: 8, DeckTitle: "deck"}, sampleFills(RecipeCloser))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markup, `id="close-card-1"`) {
		t.Fatalf("product closer must not paint gray chips:\n%s", markup)
	}
	if !strings.Contains(markup, `"cx":18`) || !strings.Contains(markup, `"cy":88`) {
		t.Fatalf("product closer glare should be bottom-left:\n%s", markup)
	}
	if !strings.Contains(markup, `id="thesis"`) || !strings.Contains(markup, `id="headline"`) {
		t.Fatal("product closer needs headline + thesis like the cover")
	}
	filled := sampleFills(RecipeCloser)
	filled["item_1_title"] = "Apple"
	filled["item_1_body"] = "A company."
	withItems, err := RenderRecipe(RecipeCloser, ProductSkin(nil), nil, Chrome{Page: 8, Total: 8}, filled)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withItems, `id="close-card-1"`) {
		t.Fatal("optional takeaways must stay a type row, not a card")
	}
	if !strings.Contains(withItems, `id="item_1_title"`) || !strings.Contains(withItems, `id="close-idx-1"`) {
		t.Fatalf("optional takeaway type row missing:\n%s", withItems)
	}
}

func TestProductThreeUpHasNoGraySlab(t *testing.T) {
	markup, err := RenderRecipe(RecipeThreeUp, ProductSkin(nil), nil, Chrome{Page: 1, Total: 8}, sampleFills(RecipeThreeUp))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markup, `geom="roundRect"`) && strings.Contains(markup, `id="card-1"`) {
		if strings.Contains(markup, `id="card-1"`) && strings.Contains(markup, `h="440"`) {
			t.Fatalf("product three-up should not be a filled slab:\n%s", markup)
		}
	}
	if !strings.Contains(markup, `id="card-1"`) {
		t.Fatal("product three-up should keep a hairline named card-1")
	}
	slide, _, err := ParseSlide(markup)
	if err != nil {
		t.Fatal(err)
	}
	card := nodeByID(slide.Nodes, "card-1")
	if card.Geometry.H != 1 {
		t.Fatalf("product three-up card-1 should be a hairline, h=%d", card.Geometry.H)
	}
}

func TestProductCoverHasNoInventedLogo(t *testing.T) {
	for _, kind := range []string{RecipeCover, RecipeCloser} {
		markup, err := RenderRecipe(kind, ProductSkin(nil), nil, Chrome{Page: 1, Total: 8, DeckTitle: "deck"}, sampleFills(kind))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(markup, "cover-glow") || strings.Contains(markup, `id="chrome-logo"`) || strings.Contains(markup, `id="ph-pic-1"`) {
			t.Fatalf("%s invented a logo/glow:\n%s", kind, markup)
		}
	}
}

func TestProductCoverPaintsProvidedLogo(t *testing.T) {
	fills := sampleFills(RecipeCover)
	fills["ph-pic-1"] = "sha256-aabbccdd"
	markup, err := RenderRecipe(RecipeCover, ProductSkin(nil), nil, Chrome{Page: 1, Total: 8}, fills)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markup, `id="ph-pic-1"`) || !strings.Contains(markup, `asset-ref="sha256-aabbccdd"`) || !strings.Contains(markup, `fit="contain"`) {
		t.Fatalf("expected top-right logo:\n%s", markup)
	}
	if !strings.Contains(markup, `w="630"`) || !strings.Contains(markup, `h="180"`) {
		t.Fatalf("logo well should be 630×180 on the Modern cover:\n%s", markup)
	}
	if strings.Contains(markup, `id="rail-page-right"`) {
		t.Fatal("page counter should not overlap the logo")
	}
	plain, err := RenderRecipe(RecipeCover, ProductSkin(nil), nil, Chrome{Page: 1, Total: 8}, sampleFills(RecipeCover))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, `id="rail-page-right"`) {
		t.Fatal("cover without a logo should keep the page counter")
	}
}

func TestAccentSpanDoesNotDoubleEscape(t *testing.T) {
	fills := sampleFills(RecipeCover)
	fills["dek"] = "The world's most valuable company — twice."
	fills["dek_accent"] = "twice"
	markup, err := RenderRecipe(RecipeCover, ProductSkin(nil), nil, Chrome{Page: 1}, fills)
	if err != nil {
		t.Fatal(err)
	}
	markup, err = fillArchetypeMarkup(markup, fills)
	if err != nil {
		t.Fatal(err)
	}
	markup = applyAccentSpans(markup, fills, "#8B5CF6")
	if strings.Contains(markup, "&amp;") {
		t.Fatalf("double-escaped markup:\n%s", markup)
	}
	if !strings.Contains(markup, "twice") || !strings.Contains(markup, "world") {
		t.Fatalf("dek fill missing:\n%s", markup)
	}
}

func TestCoverMetaFollowsDek(t *testing.T) {
	fills := sampleFills(RecipeCover)
	markup, err := RenderRecipe(RecipeCover, CorporateSkin(fixtureTokens()), nil, Chrome{Page: 1}, fills)
	if err != nil {
		t.Fatal(err)
	}
	slide, _, err := ParseSlide(markup)
	if err != nil {
		t.Fatal(err)
	}
	dek := nodeByID(slide.Nodes, "dek")
	meta := nodeByID(slide.Nodes, "meta_1_label")
	if dek.ID == "" || meta.ID == "" {
		t.Fatal("missing dek or meta")
	}
	if meta.Geometry.Y < dek.Geometry.Y+dek.Geometry.H {
		t.Fatalf("meta overlaps dek: meta.y=%d dek=%d+%d", meta.Geometry.Y, dek.Geometry.Y, dek.Geometry.H)
	}
	if meta.Geometry.Y > dek.Geometry.Y+dek.Geometry.H+80 {
		t.Fatalf("meta parked at bottom instead of under dek: meta.y=%d dek bottom=%d", meta.Geometry.Y, dek.Geometry.Y+dek.Geometry.H)
	}
}

func TestProductSkinUsesDotRailNotLogoRule(t *testing.T) {
	tokens := map[string]string{"surface": "#0B0D0F", "ink": "#ECEDEE", "accent": "#8B5CF6"}
	corp, err := RenderRecipe(RecipeThreeUp, CorporateSkin(tokens), nil, Chrome{Page: 1, Total: 8}, sampleFills(RecipeThreeUp))
	if err != nil {
		t.Fatal(err)
	}
	prod, err := RenderRecipe(RecipeThreeUp, ProductSkin(tokens), nil, Chrome{Page: 1, Total: 8, DeckTitle: "astonish"}, sampleFills(RecipeThreeUp))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prod, `id="rule"`) && strings.Contains(prod, `y="`) && !strings.Contains(prod, `id="rail-dot"`) {
		t.Fatal("product skin should use the dot rail, not the corporate accent rule")
	}
	if !strings.Contains(prod, `id="rail-dot"`) {
		t.Fatalf("product missing rail-dot:\n%s", prod)
	}
	if strings.Contains(corp, `id="rail-dot"`) {
		t.Fatal("corporate skin should not use the product rail")
	}
	if _, _, err := ParseSlide(prod); err != nil {
		t.Fatal(err)
	}
}

func TestSkinForProductTemplate(t *testing.T) {
	tmpl, ok := themes.LookupTemplate("modern")
	if !ok {
		t.Fatal("missing product template")
	}
	if SkinFor(tmpl).ID != SkinProduct {
		t.Fatalf("product template skin = %s", SkinFor(tmpl).ID)
	}
	if SkinFor(themes.Template{Name: "gco"}).ID != SkinCorporate {
		t.Fatal("imported-like template should be corporate")
	}
}

func TestAccentSpanColorsPhrase(t *testing.T) {
	fills := sampleFills(RecipeSplitNarrative)
	fills["headline"] = "An agent that acts, not just answers."
	fills["headline_accent"] = "acts"
	markup, err := RenderRecipe(RecipeSplitNarrative, ProductSkin(nil), nil, Chrome{Page: 2, Total: 10}, fills)
	if err != nil {
		t.Fatal(err)
	}
	markup, err = fillArchetypeMarkup(markup, fills)
	if err != nil {
		t.Fatal(err)
	}
	markup = applyAccentSpans(markup, fills, "#8B5CF6")
	if !strings.Contains(markup, `color="#8B5CF6"`) || !strings.Contains(markup, "acts") {
		t.Fatalf("accent span missing:\n%s", markup)
	}
	if _, _, err := ParseSlide(markup); err != nil {
		t.Fatal(err)
	}
}

func TestTwoUpEmphasizesColumn(t *testing.T) {
	fills := sampleFills(RecipeTwoUp)
	fills["emphasis"] = "2"
	markup, err := RenderRecipe(RecipeTwoUp, ProductSkin(nil), nil, Chrome{Page: 1, Total: 4}, fills)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markup, `id="col-card-2"`) {
		t.Fatal("missing col-card-2")
	}
	// Emphasized card uses accent fill.
	if !strings.Contains(markup, ProductSkin(nil).AccentFill) {
		t.Fatalf("expected emphasized fill %s:\n%s", ProductSkin(nil).AccentFill, markup)
	}
}

func nodeByID(nodes []Node, id string) Node {
	var found Node
	walkNodes(nodes, func(n Node) {
		if n.ID == id {
			found = n
		}
	})
	return found
}

func assertNoTextOverlap(t *testing.T, slide Slide) {
	t.Helper()
	var boxes []Geometry
	walkNodes(slide.Nodes, func(n Node) {
		if n.Type != "text" || strings.TrimSpace(nodeText(n)) == "" {
			return
		}
		if n.Geometry.W < 8 || n.Geometry.H < 8 {
			return
		}
		boxes = append(boxes, n.Geometry)
	})
	for i := 0; i < len(boxes); i++ {
		for j := i + 1; j < len(boxes); j++ {
			a, b := boxes[i], boxes[j]
			ix := minInt(a.X+a.W, b.X+b.W) - maxInt(a.X, b.X)
			iy := minInt(a.Y+a.H, b.Y+b.H) - maxInt(a.Y, b.Y)
			if ix > 8 && iy > 8 {
				t.Errorf("text boxes overlap by %dx%d: %+v vs %+v", ix, iy, a, b)
			}
		}
	}
}

func assertFooterInBottom(t *testing.T, slide Slide) {
	t.Helper()
	found := false
	walkNodes(slide.Nodes, func(n Node) {
		if n.ID != "chrome-footer" && n.ID != "chrome-legal" && n.ID != "chrome-page" {
			return
		}
		found = true
		if n.Geometry.Y < 1000 {
			t.Errorf("footer %s y=%d, want >= 1000", n.ID, n.Geometry.Y)
		}
	})
	if !found && slide.ID != RecipeCover {
		t.Error("expected a footer node")
	}
}
