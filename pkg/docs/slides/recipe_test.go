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
	tokens := fixtureTokens()
	chrome := Chrome{DeckTitle: "A life in twelve chapters", Page: 2}
	for _, m := range allRecipeMeta() {
		t.Run(m.Kind, func(t *testing.T) {
			skel, err := RenderRecipe(m.Kind, nil, tokens, chrome, nil)
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
			filledSkel, err := RenderRecipe(m.Kind, nil, tokens, chrome, sampleFills(m.Kind))
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

func TestRenderRecipeRequiredSlotsPresent(t *testing.T) {
	markup, err := RenderRecipe(RecipeSplitNarrative, nil, fixtureTokens(), Chrome{}, nil)
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
	markup, err := RenderRecipe(RecipeSplitNarrative, nil, fixtureTokens(), Chrome{Page: 1}, fills)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markup, `id="headline_2"`) {
		t.Fatal("empty optional headline_2 should be omitted at fill time")
	}
	fills["headline_2"] = "then chosen"
	markup, err = RenderRecipe(RecipeSplitNarrative, nil, fixtureTokens(), Chrome{Page: 1}, fills)
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
	markup, err := RenderRecipe(RecipeCover, nil, tmpl.Tokens, ch, sampleFills(RecipeCover))
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
	markup, err := RenderRecipe(RecipeThreeUp, nil, tokens, Chrome{Page: 1}, sampleFills(RecipeThreeUp))
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
	p := paletteFrom(nil, tokens)
	if !textContrastOK(p.secondary, p.surface) {
		t.Fatalf("secondary %s fails contrast on %s", p.secondary, p.surface)
	}
	if strings.EqualFold(p.secondary, "#89D1FF") {
		t.Fatal("pale decorative muted must not be used as body text")
	}
	markup, err := RenderRecipe(RecipeThreeUp, nil, tokens, Chrome{Page: 1}, sampleFills(RecipeThreeUp))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markup, `color="#89D1FF"`) {
		t.Fatalf("pale muted used as text color:\n%s", markup)
	}
}

func TestCardsHugContent(t *testing.T) {
	markup, err := RenderRecipe(RecipeThreeUp, nil, fixtureTokens(), Chrome{Page: 1}, sampleFills(RecipeThreeUp))
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
	if card.Geometry.H > 400 {
		t.Fatalf("three-up card stretched to leftover canvas: h=%d", card.Geometry.H)
	}
	quote, err := RenderRecipe(RecipeQuoteSplit, nil, fixtureTokens(), Chrome{Page: 1}, sampleFills(RecipeQuoteSplit))
	if err != nil {
		t.Fatal(err)
	}
	qslide, _, err := ParseSlide(quote)
	if err != nil {
		t.Fatal(err)
	}
	qc := nodeByID(qslide.Nodes, "quote-card")
	if qc.Geometry.H > 360 {
		t.Fatalf("quote card too tall: h=%d", qc.Geometry.H)
	}
}

func TestCoverMetaFollowsDek(t *testing.T) {
	fills := sampleFills(RecipeCover)
	markup, err := RenderRecipe(RecipeCover, nil, fixtureTokens(), Chrome{Page: 1}, fills)
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
	if !found {
		t.Error("expected a footer node")
	}
}
