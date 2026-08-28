package slides

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/store"
)

func toolContext(t *testing.T, docs store.DocsStore) context.Context {
	t.Helper()
	return store.WithServices(context.Background(), &store.Services{PersonalDocs: docs})
}

func sessionToolContext(t *testing.T, docs store.DocsStore, sessionID string) context.Context {
	t.Helper()
	return store.WithSessionID(toolContext(t, docs), sessionID)
}

func seedFillTemplate(t *testing.T, ctx context.Context, docs store.DocsStore) {
	t.Helper()
	svc := Service{Store: docs}
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "brand",
		Tokens: map[string]string{"surface": "#FFFFFF"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="p"><ast-text id="ph-1" x="10" y="10" w="400" h="80" size="24"><ast-run>{{TITLE}}</ast-run></ast-text></ast-slide>`, FillSlots: []string{"ph-1"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSlideToolsUseOnlyPersonalDocs(t *testing.T) {
	personal := &memoryDocsStore{}
	team := &memoryDocsStore{}
	ctx := store.WithServices(context.Background(), &store.Services{
		PersonalDocs: personal,
		Docs:         team,
	})

	created, err := createDeck(ctx, CreateDeckArgs{Slug: "private", Title: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Deck == nil || created.Deck.Slug != "private" {
		t.Fatalf("unexpected create result: %#v", created)
	}
	if personal.deck == nil || personal.deck.Slug != "private" {
		t.Fatalf("deck was not persisted to personal docs: %#v", personal.deck)
	}
	if team.deck != nil {
		t.Fatalf("team docs store must remain untouched: %#v", team.deck)
	}
}

func TestSlideToolsCreateWriteReadAndValidate(t *testing.T) {
	backend := &memoryDocsStore{}
	ctx := toolContext(t, backend)

	created, err := createDeck(ctx, CreateDeckArgs{Slug: "quarterly", Title: "Quarterly review"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Deck == nil || created.Deck.Slug != "quarterly" {
		t.Fatalf("unexpected create result: %#v", created)
	}

	written, err := writeSlide(ctx, WriteSlideArgs{
		DeckSlug: "quarterly",
		Position: 0,
		Markup:   `<ast-slide id="overview" title="Overview"><ast-text id="title" x="96" y="72" w="1728" h="100">Quarterly review</ast-text></ast-slide>`,
		Notes:    "Open with the headline.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if written.Slide == nil || written.SlideCount != 1 || written.Slide.Notes != "Open with the headline." {
		t.Fatalf("unexpected write result: %#v", written)
	}

	got, err := getDeck(ctx, GetDeckArgs{Slug: "quarterly"})
	if err != nil {
		t.Fatal(err)
	}
	if got.SlideCount != 1 || len(got.SlideIndex) != 1 || got.SlideIndex[0].Title != "Overview" {
		t.Fatalf("unexpected deck result: %#v", got)
	}
	if len(got.Slides) != 0 {
		t.Fatalf("get_deck must not return slide markup, got %d slides", len(got.Slides))
	}
	one, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "quarterly", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	if one.Slide == nil || !strings.Contains(one.Slide.Content, "Quarterly review") {
		t.Fatalf("read_slide missing markup: %#v", one)
	}

	validation, err := validateDeck(ctx, ValidateDeckArgs{Slug: "quarterly"})
	if err != nil {
		t.Fatal(err)
	}
	if !validation.Valid || len(validation.Diagnostics) != 0 {
		t.Fatalf("unexpected validation result: %#v", validation)
	}
}

func TestWriteSlideRejectsInvalidInputWithoutPersisting(t *testing.T) {
	backend := &memoryDocsStore{}
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck"}); err != nil {
		t.Fatal(err)
	}

	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: -1, Markup: `<ast-slide id="bad"></ast-slide>`}); err == nil {
		t.Fatal("expected negative position error")
	}
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: `<ast-slide><ast-text x="0" y="0" w="100" h="100">Missing ID</ast-text></ast-slide>`}); err == nil {
		t.Fatal("expected validation error")
	}
	if len(backend.slides) != 0 {
		t.Fatalf("invalid slide was persisted: %#v", backend.slides)
	}
}

func TestGetTools(t *testing.T) {
	got, err := GetTools()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"create_deck", "fill_slides", "fill_slide", "get_archetype", "write_slide", "get_deck", "read_slide", "list_decks", "list_slide_templates", "get_template_variant_previews", "validate_deck", "review_deck", "list_deck_assets", "add_deck_image", "ask_user"}
	if len(got) != len(want) {
		t.Fatalf("got %d tools, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name() != name {
			t.Fatalf("tool %d = %q, want %q", i, got[i].Name(), name)
		}
	}
}

func TestCreateDeckWithTemplateSeedsThemeAssetsAndArchetypes(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)

	created, err := createDeck(ctx, CreateDeckArgs{Slug: "kickoff", Title: "Kickoff", Template: "midnight"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Deck == nil {
		t.Fatalf("expected deck, got %#v", created)
	}
	if created.Deck.Theme["surface"] == "" || created.Deck.Theme["ink"] == "" {
		t.Fatalf("template theme tokens not seeded: %#v", created.Deck.Theme)
	}
	if created.Deck.Theme["surface"] != "#0B1220" {
		t.Fatalf("expected midnight surface token, got %q", created.Deck.Theme["surface"])
	}
	if len(created.Catalog) == 0 {
		t.Fatalf("expected non-empty catalog from template, got %#v", created.Catalog)
	}
	if len(created.Archetypes) != 0 {
		t.Fatalf("create_deck must not dump archetype markup, got %d archetypes", len(created.Archetypes))
	}

	// Explicit theme overrides win over template tokens.
	created2, err := createDeck(ctx, CreateDeckArgs{Slug: "kickoff2", Title: "Kickoff2", Template: "midnight", Theme: map[string]string{"surface": "#000000"}})
	if err != nil {
		t.Fatal(err)
	}
	if created2.Deck.Theme["surface"] != "#000000" {
		t.Fatalf("explicit theme override should win, got %q", created2.Deck.Theme["surface"])
	}
}

func TestCreateDeckCatalogPayloadIsBounded(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	created, err := createDeck(ctx, CreateDeckArgs{Slug: "kickoff", Title: "Kickoff", Template: "midnight"})
	if err != nil {
		t.Fatal(err)
	}
	raw := jsonDump(t, created)
	if strings.Contains(raw, "<ast-slide") || strings.Contains(raw, "data:image") || strings.Contains(raw, "data:font") {
		t.Fatalf("create_deck leaked markup or data URIs (%d bytes)", len(raw))
	}
	if len(raw) > 160_000 {
		t.Fatalf("create_deck builtin catalog too large: %d bytes", len(raw))
	}
	if !catalogHasKind(created.Catalog, "recipe-cover") {
		t.Fatalf("create_deck catalog missing recipe-cover: %#v", created.Catalog)
	}
	if created.Deck.Theme[themeKeyTemplateName] != "midnight" {
		t.Fatalf("template-name not stamped: %#v", created.Deck.Theme)
	}
}

func TestFillSlideSubstitutesSlotsAndOmitsMarkup(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	markup := `<ast-slide id="p"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-shape id="card" kind="rect" x="80" y="200" w="500" h="300" geom="roundRect" fill="#DBEAFE"></ast-shape>` +
		`<ast-text id="ph-1" x="160" y="80" w="1600" h="100" size="54" color="#111111"><ast-run>{{TITLE}}</ast-run></ast-text>` +
		`<ast-text id="ph-2" x="100" y="220" w="460" h="80" size="28" color="#111111"><ast-run>{{BODY}}</ast-run></ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "cards",
		Label:  "Cards",
		Tokens: map[string]string{"surface": "#FFFFFF", "ink": "#111111"},
		Archetypes: []themes.Archetype{
			{Kind: "pattern", Title: "3 rounded cards", Tier: "flexible", Markup: markup, FillSlots: []string{"ph-1", "ph-2"}},
		},
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	created, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "cards"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(jsonDump(t, created), "<ast-slide") {
		t.Fatal("create_deck must not include ast-slide markup")
	}
	if created.Deck.Theme[themeKeyTemplateName] != "cards" {
		t.Fatalf("template-name not stamped: %#v", created.Deck.Theme)
	}

	res, err := fillSlide(ctx, FillSlideArgs{
		DeckSlug: "deck", Position: 0, Kind: "pattern",
		Fills: map[string]string{"ph-1": "Revenue grew 23%", "ph-2": "Enterprise renewals"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SlideCount != 1 || res.Position != 0 {
		t.Fatalf("unexpected fill result: %#v", res)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "<ast-slide") || strings.Contains(string(raw), "roundRect") {
		t.Fatalf("fill_slide result must not echo markup: %s", raw)
	}

	got, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "deck", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	if got.Slide == nil {
		t.Fatal("expected a slide")
	}
	body := got.Slide.Content
	if !strings.Contains(body, `geom="roundRect"`) {
		t.Fatalf("persisted slide lost card chrome:\n%s", body)
	}
	if !strings.Contains(body, "Revenue grew 23%") {
		t.Fatalf("persisted slide missing fill:\n%s", body)
	}
	if strings.Contains(body, "{{TITLE}}") {
		t.Fatalf("placeholder left behind:\n%s", body)
	}
}

func TestFillSlidesWritesManyAndCopiesOnlyReferencedAssets(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	used := "sha256-used"
	unused := "sha256-unused"
	logo := "data:image/png;base64,AAAUSED"
	noise := "data:image/png;base64,AAANOISE"
	font := "data:font/ttf;base64,AAAFONT"
	markup := `<ast-slide id="p" title="Cover"><ast-image id="logo" x="40" y="40" w="200" h="80" asset-ref="` + used + `"></ast-image>` +
		`<ast-text id="ph-1" x="160" y="80" w="1600" h="100" size="54"><ast-run>{{TITLE}}</ast-run></ast-text></ast-slide>`
	bodyMarkup := `<ast-slide id="b" title="Body"><ast-text id="ph-1" x="160" y="80" w="1600" h="100" size="40"><ast-run>{{TITLE}}</ast-run></ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "brand",
		Label:  "Brand",
		Tokens: map[string]string{"surface": "#FFFFFF", "ink": "#111111"},
		Assets: map[string]string{
			used:                 logo,
			unused:               noise,
			"font:Brand:regular": font,
		},
		Archetypes: []themes.Archetype{
			{Kind: "title", Title: "Title", Tier: "fixed", Markup: markup, FillSlots: []string{"ph-1"}},
			{Kind: "pattern", Title: "Cards", Tier: "flexible", Markup: bodyMarkup, FillSlots: []string{"ph-1"}},
		},
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	created, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "brand"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := backend.GetDeck(ctx, "deck")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Assets[unused] != "" || stored.Assets[used] != "" {
		t.Fatalf("create_deck must not copy sample photos, got %#v", stored.Assets)
	}
	if stored.Assets["font:Brand:regular"] != font {
		t.Fatalf("create_deck should seed embedded fonts, got %#v", stored.Assets)
	}
	if created.Deck == nil {
		t.Fatal("expected deck view")
	}
	foundUsed, foundUnused := false, false
	for _, a := range created.Deck.Assets {
		if a.Ref == used {
			foundUsed = true
		}
		if a.Ref == unused {
			foundUnused = true
		}
	}
	if !foundUsed || !foundUnused {
		t.Fatalf("create_deck catalog should list template image refs, got %#v", created.Deck.Assets)
	}

	batch, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "deck",
		Slides: []FillSlideSpec{
			{Position: 0, Kind: "title", Fills: map[string]string{"ph-1": "Steve Jobs"}},
			{Position: 1, Kind: "pattern", Fills: map[string]string{"ph-1": "Early life"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch.SlideCount != 2 || len(batch.Positions) != 2 {
		t.Fatalf("unexpected batch: %#v", batch)
	}
	raw, _ := json.Marshal(batch)
	if strings.Contains(string(raw), "<ast-slide") || strings.Contains(string(raw), "data:image") {
		t.Fatalf("fill_slides must not echo markup or data URIs: %s", raw)
	}

	stored, err = backend.GetDeck(ctx, "deck")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Assets[used] != logo {
		t.Fatalf("referenced logo was not copied onto the deck: %#v", stored.Assets)
	}
	if stored.Assets[unused] != "" {
		t.Fatalf("unused sample photo was copied onto the deck: %#v", stored.Assets)
	}

	cover, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "deck", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	if cover.Slide == nil || !strings.Contains(cover.Slide.Content, "Steve Jobs") {
		t.Fatalf("cover missing fill: %#v", cover)
	}
}

func TestFillSlidesRecipeUsesNamedSlotsAndTheme(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Life story", Template: "midnight"}); err != nil {
		t.Fatal(err)
	}
	_, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "deck",
		Slides: []FillSlideSpec{{
			Position: 0,
			Kind:     RecipeCover,
			Fills: map[string]string{
				"eyebrow":      "A biographical presentation",
				"headline":     "Steve",
				"headline_2":   "Jobs",
				"dek":          "A life in twelve chapters, stated as a thesis sentence.",
				"meta_1_label": "Born",
				"meta_1_value": "1955",
				"meta_2_label": "Died",
				"meta_2_value": "2011",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "deck", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	body := got.Slide.Content
	if !strings.Contains(body, "A biographical presentation") || !strings.Contains(body, "Steve") {
		t.Fatalf("missing fills:\n%s", body)
	}
	if !strings.Contains(body, `id="eyebrow"`) || !strings.Contains(body, `id="headline"`) {
		t.Fatalf("missing named slots:\n%s", body)
	}
	if !strings.Contains(body, `fill="#0B1220"`) {
		t.Fatalf("midnight surface not applied:\n%s", body)
	}
	if !strings.Contains(body, "Life story") {
		t.Fatalf("running footer should use deck title:\n%s", body)
	}
}

func TestFillSlidesProductCoverAndCloser(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{
		Slug: "steve-jobs-2026", Title: "Steve Jobs: Think Different", Template: "product",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "steve-jobs-2026",
		Slides: []FillSlideSpec{
			{
				Position: 0,
				Kind:     RecipeCover,
				Fills: map[string]string{
					"eyebrow":      "1955 – 2011",
					"headline":     "Steve Jobs",
					"headline_2":   "Think Different",
					"dek":          "The adopted son of a machinist who built the most valuable company on Earth.",
					"dek_accent":   "most valuable company on Earth",
					"meta_1_label": "Born",
					"meta_1_value": "Feb 24, 1955",
					"meta_2_label": "Died",
					"meta_2_value": "Oct 5, 2011",
					"meta_4_label": "Legacy",
					"meta_4_value": "6 industries",
					"prompt":       "$ think different",
				},
			},
			{
				Position: 1,
				Kind:     RecipeYearHero,
				Fills: map[string]string{
					"eyebrow":      "ORIGIN",
					"headline":     "Adopted in infancy",
					"year":         "1955",
					"item_1_title": "Adoption",
					"item_1_body":  "Paul and Clara Jobs raised Steve in Mountain View.",
					"item_2_title": "Garage",
					"item_2_body":  "Paul taught young Steve electronics.",
					"item_3_title": "California",
					"item_3_body":  "The Bay Area shaped his worldview.",
				},
			},
			{
				Position: 2,
				Kind:     RecipeCloser,
				Fills: map[string]string{
					"eyebrow":      "LEGACY",
					"headline":     "Stay hungry.",
					"headline_2":   "Stay foolish.",
					"thesis":       "Stanford commencement, 2005 — the line that outlived the keynote.",
					"item_1_title": "Apple",
					"item_1_body":  "The most valuable company on Earth.",
					"item_2_title": "Pixar",
					"item_2_body":  "Toy Story invented a studio and a medium.",
					"item_3_title": "NeXT",
					"item_3_body":  "The OS that became Mac OS X and iOS.",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("product fill_slides: %v", err)
	}
	cover, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "steve-jobs-2026", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	if cover.Slide == nil || !strings.Contains(cover.Slide.Content, "Steve Jobs") {
		t.Fatalf("cover missing fill: %#v", cover)
	}
	if strings.Contains(cover.Slide.Content, `id="meta_4_label"`) {
		t.Fatal("product cover should not emit meta_4")
	}
	closer, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "steve-jobs-2026", Position: 2})
	if err != nil {
		t.Fatal(err)
	}
	if closer.Slide == nil || !strings.Contains(closer.Slide.Content, "Stay hungry") {
		t.Fatalf("closer missing fill: %#v", closer)
	}
	if !strings.Contains(closer.Slide.Content, `id="item_1_title"`) || !strings.Contains(closer.Slide.Content, "Apple") {
		t.Fatalf("product closer missing takeaway chips:\n%s", closer.Slide.Content)
	}
	if !strings.Contains(cover.Slide.Content, "bg-gradient") {
		t.Fatalf("product cover missing background gradient:\n%s", cover.Slide.Content)
	}
}

func TestCreateDeckResolvesSlugCollision(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	first, err := createDeck(ctx, CreateDeckArgs{Slug: "steve-jobs-life", Title: "One", Template: "product"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := createDeck(ctx, CreateDeckArgs{Slug: "steve-jobs-life", Title: "Two", Template: "product"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Deck == nil || second.Deck.Slug == first.Deck.Slug {
		t.Fatalf("expected unique slug, first=%q second=%#v", first.Deck.Slug, second.Deck)
	}
}

func TestCreateDeckSessionUsesUniqueSlug(t *testing.T) {
	backend := newMultiDeckStore()
	ctxA := sessionToolContext(t, backend, "session-a")
	ctxB := sessionToolContext(t, backend, "session-b")

	first, err := createDeck(ctxA, CreateDeckArgs{Slug: "steve-jobs-life", Title: "Jobs A", Description: "Session A biography", Template: "product"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := createDeck(ctxB, CreateDeckArgs{Slug: "steve-jobs-life", Title: "Jobs B", Description: "Session B biography", Template: "product"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Deck == nil || first.Deck.Slug != "s-session-a" {
		t.Fatalf("session A persist slug = %#v, want s-session-a", first.Deck)
	}
	if second.Deck == nil || second.Deck.Slug != "s-session-b" {
		t.Fatalf("session B persist slug = %#v, want s-session-b", second.Deck)
	}

	keptA, err := backend.GetDeck(ctxA, "s-session-a")
	if err != nil {
		t.Fatal(err)
	}
	keptB, err := backend.GetDeck(ctxB, "s-session-b")
	if err != nil {
		t.Fatal(err)
	}
	if keptA.Title != "Jobs A" || keptA.Description != "Session A biography" {
		t.Fatalf("session A deck overwritten: %#v", keptA)
	}
	if keptB.Title != "Jobs B" || keptB.Description != "Session B biography" {
		t.Fatalf("session B deck missing: %#v", keptB)
	}
	if _, err := backend.GetDeck(ctxA, "steve-jobs-life"); err == nil {
		t.Fatal("hint slug must not be the persist key in a chat session")
	}
}

func TestCreateDeckSameSessionReplacesDraft(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := sessionToolContext(t, backend, "session-retry")
	first, err := createDeck(ctx, CreateDeckArgs{Slug: "steve-jobs-life", Title: "One", Template: "product"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := createDeck(ctx, CreateDeckArgs{Slug: "steve-jobs-life", Title: "Two", Description: "Retry", Template: "product"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Deck.Slug != "s-session-retry" || second.Deck.Slug != "s-session-retry" {
		t.Fatalf("same-session retry should reuse persist slug, first=%q second=%q", first.Deck.Slug, second.Deck.Slug)
	}
	got, err := backend.GetDeck(ctx, "s-session-retry")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Two" {
		t.Fatalf("retry should replace leftover draft, got %#v", got)
	}
}

func TestFillSlidesHintMapsToSessionDraft(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := sessionToolContext(t, backend, "session-fill")
	seedFillTemplate(t, ctx, backend)
	created, err := createDeck(ctx, CreateDeckArgs{Slug: "steve-jobs-life", Title: "Jobs", Description: "A life in slides", Template: "brand"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Deck.Slug != "s-session-fill" {
		t.Fatalf("persist slug = %q", created.Deck.Slug)
	}
	filled, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "steve-jobs-life",
		Slides:   []FillSlideSpec{{Position: 0, Kind: "title", Fills: map[string]string{"ph-1": "Stay hungry"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filled.Deck == nil || filled.Deck.Slug != "s-session-fill" {
		t.Fatalf("fill should target session draft, got %#v", filled.Deck)
	}
	if _, err := backend.GetDeck(ctx, "steve-jobs-life"); err == nil {
		t.Fatal("fill must not create a second deck under the hint slug")
	}
	_, slides, err := Service{Store: backend}.Deck(ctx, "s-session-fill")
	if err != nil {
		t.Fatal(err)
	}
	if len(slides) != 1 || !strings.Contains(slides[0].Content, "Stay hungry") {
		t.Fatalf("session draft missing filled slide: %#v", slides)
	}
}

func TestFillSlidesDoesNotOverwriteOtherSession(t *testing.T) {
	backend := newMultiDeckStore()
	ctxA := sessionToolContext(t, backend, "session-a")
	ctxB := sessionToolContext(t, backend, "session-b")
	seedFillTemplate(t, ctxA, backend)

	// Legacy session-A deck stored under the human hint (the pre-fix persist key).
	legacy := &store.DeckManifest{
		ID:        "legacy-a",
		Slug:      "steve-jobs-life",
		Title:     "Jobs A",
		SessionID: "session-a",
		Theme:     map[string]string{themeKeyTemplateName: "brand"},
	}
	if err := backend.CreateDeck(ctxA, legacy); err != nil {
		t.Fatal(err)
	}
	if err := backend.UpsertSlide(ctxA, &store.SlideContent{
		ID: "s0", DeckID: "legacy-a", Position: 0, Title: "Original",
		Content: `<ast-slide id="p"><ast-text id="ph-1" x="10" y="10" w="400" h="80">Original A</ast-text></ast-slide>`,
	}); err != nil {
		t.Fatal(err)
	}

	created, err := createDeck(ctxB, CreateDeckArgs{Slug: "steve-jobs-life", Title: "Jobs B", Template: "brand"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Deck.Slug != "s-session-b" {
		t.Fatalf("session B persist slug = %q", created.Deck.Slug)
	}
	if _, err := fillSlides(ctxB, FillSlidesArgs{
		DeckSlug: "steve-jobs-life",
		Slides:   []FillSlideSpec{{Position: 0, Kind: "title", Fills: map[string]string{"ph-1": "Session B fill"}}},
	}); err != nil {
		t.Fatal(err)
	}

	kept, slides, err := Service{Store: backend}.Deck(ctxA, "steve-jobs-life")
	if err != nil {
		t.Fatal(err)
	}
	if kept.Title != "Jobs A" || kept.SessionID != "session-a" {
		t.Fatalf("session A deck overwritten: %#v", kept)
	}
	if len(slides) != 1 || !strings.Contains(slides[0].Content, "Original A") {
		t.Fatalf("session A slides overwritten: %#v", slides)
	}
}

func TestFillSlidesRejectsDuplicatePositions(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "brand",
		Tokens: map[string]string{"surface": "#FFFFFF"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="p"><ast-text id="ph-1" x="10" y="10" w="400" h="80" size="24"><ast-run>{{TITLE}}</ast-run></ast-text></ast-slide>`, FillSlots: []string{"ph-1"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "brand"}); err != nil {
		t.Fatal(err)
	}
	_, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "deck",
		Slides: []FillSlideSpec{
			{Position: 0, Kind: "title", Fills: map[string]string{"ph-1": "One"}},
			{Position: 0, Kind: "title", Fills: map[string]string{"ph-1": "Two"}},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate position error")
	}
}

func catalogHasKind(cat []ArchetypeCatalogEntry, kind string) bool {
	for _, e := range cat {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func optionsWithoutDefault(opts []AskUserOptionPayload) []AskUserOptionPayload {
	out := make([]AskUserOptionPayload, 0, len(opts))
	for _, o := range opts {
		if o.ID == "default" {
			continue
		}
		out = append(out, o)
	}
	return out
}

func hasDefaultAskOption(opts []AskUserOptionPayload) bool {
	for _, o := range opts {
		if o.ID == "default" {
			return true
		}
	}
	return false
}

func jsonDump(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCreateDeckWithScopedTemplateSeedsAssets(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "acme",
		Label:  "Acme",
		Tokens: map[string]string{"surface": "#101820", "ink": "#F2F2F2"},
		Assets: map[string]string{"logo": "acme.png"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#F2F2F2" size="72">{{TITLE}}</ast-text></ast-slide>`},
		},
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	created, err := createDeck(ctx, CreateDeckArgs{Slug: "brand", Title: "Brand", Template: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Deck.AssetCount != 1 {
		t.Fatalf("scoped template assets not seeded: assetCount=%d", created.Deck.AssetCount)
	}
	if created.Deck.Theme["surface"] != "#101820" {
		t.Fatalf("scoped template tokens not seeded: %#v", created.Deck.Theme)
	}
	if !catalogHasKind(created.Catalog, "title") {
		t.Fatalf("scoped template title missing from catalog: %#v", created.Catalog)
	}
	if !catalogHasKind(created.Catalog, "recipe-cover") {
		t.Fatalf("expected recipe-cover in catalog, got %#v", created.Catalog)
	}
}

func TestListTemplatesToolReturnsBuiltinsAndScoped(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "acme",
		Label:  "Acme",
		Tokens: map[string]string{"surface": "#101820"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#F2F2F2" size="72">{{TITLE}}</ast-text></ast-slide>`},
		},
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	res, err := listTemplates(ctx, ListTemplatesArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Templates) < 3 {
		t.Fatalf("expected >=3 built-in templates, got %d: %#v", len(res.Templates), res.Templates)
	}
	names := map[string]bool{}
	for _, tmpl := range res.Templates {
		names[tmpl.Name] = true
	}
	for _, want := range []string{"light-corporate", "midnight", "aurora", "product", "acme"} {
		if !names[want] {
			t.Fatalf("list_templates missing %q; got %#v", want, names)
		}
	}

	// Symptom B: the lightweight catalog must NOT carry archetype markup. Every
	// archetype ast-slide fragment contains "ast-slide", so its absence from the
	// serialized result proves no full template payload leaked into the response.
	blob, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "ast-slide") {
		t.Fatalf("list_templates result leaked archetype markup (found \"ast-slide\"): %s", blob)
	}
	// Scoped templates are tagged so the model can distinguish imports from built-ins.
	for _, tmpl := range res.Templates {
		if tmpl.Name == "acme" && tmpl.Scope != "scope" {
			t.Fatalf("scoped template acme has scope %q, want \"scope\"", tmpl.Scope)
		}
	}
}

// TestSaveTemplateThenListSurfacesScopedTemplate pins Symptom A: a template
// imported (persisted via SaveTemplate) into the same personal store the chat
// tool reads must appear in list_templates alongside the built-ins.
func TestSaveTemplateThenListSurfacesScopedTemplate(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:        "brand",
		Label:       "Brand",
		Description: "Imported corporate template",
		Tokens:      map[string]string{"surface": "#101820", "ink": "#F2F2F2"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#F2F2F2" size="72">{{TITLE}}</ast-text></ast-slide>`},
			{Kind: "content", Markup: `<ast-slide id="c"><ast-text id="b" x="160" y="320" w="1600" h="600" color="#F2F2F2" size="36">{{BODY}}</ast-text></ast-slide>`},
		},
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	res, err := listTemplates(ctx, ListTemplatesArgs{})
	if err != nil {
		t.Fatal(err)
	}
	var brand *TemplateSummary
	for i := range res.Templates {
		if res.Templates[i].Name == "brand" {
			brand = &res.Templates[i]
			break
		}
	}
	if brand == nil {
		t.Fatalf("imported template \"brand\" not surfaced by list_templates; got %#v", res.Templates)
	}
	if brand.Scope != "scope" {
		t.Fatalf("imported template scope = %q, want \"scope\"", brand.Scope)
	}
	if brand.Label != "Brand" || brand.Description != "Imported corporate template" {
		t.Fatalf("imported template summary lost identity: %#v", brand)
	}
	if len(brand.ArchetypeKinds) != 2 || brand.ArchetypeKinds[0] != "title" {
		t.Fatalf("imported template archetype kinds = %#v, want [title content]", brand.ArchetypeKinds)
	}
	// Built-ins are still present alongside the import.
	seen := map[string]bool{}
	for _, tmpl := range res.Templates {
		seen[tmpl.Name] = true
	}
	for _, want := range []string{"light-corporate", "midnight", "aurora", "product"} {
		if !seen[want] {
			t.Fatalf("built-in %q missing after import; got %#v", want, seen)
		}
	}
}

func TestListDeckAssets(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}

	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "gallery", Title: "Gallery"}); err != nil {
		t.Fatal(err)
	}
	// Inject assets (a photo + a tiny svg logo) into the stored manifest.
	if _, err := svc.AddDeckAsset(ctx, "gallery", "sha256-photo", "data:image/png;base64,"+strings.Repeat("A", 8000)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddDeckAsset(ctx, "gallery", "sha256-logo", "data:image/svg+xml;base64,AAAA"); err != nil {
		t.Fatal(err)
	}
	// Also inject an embedded FONT asset (shares the Assets map, keyed font:...).
	// It must be excluded from the image catalog and never leak its data: bytes.
	if _, err := svc.AddDeckAsset(ctx, "gallery", "font:72 Brand:regular", "data:font/ttf;base64,"+strings.Repeat("B", 4000)); err != nil {
		t.Fatal(err)
	}

	res, err := listDeckAssets(ctx, ListDeckAssetsArgs{DeckSlug: "gallery"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Assets) != 2 {
		t.Fatalf("expected 2 image assets (font excluded), got %d: %#v", len(res.Assets), res.Assets)
	}
	for _, a := range res.Assets {
		if strings.HasPrefix(a.Ref, "font:") {
			t.Fatalf("font asset must not appear in image catalog: %#v", a)
		}
	}
	// Deterministic order (sorted by ref): sha256-logo < sha256-photo.
	if res.Assets[0].Ref != "sha256-logo" || res.Assets[0].Kind != "logo" || res.Assets[0].MIME != "image/svg+xml" {
		t.Fatalf("logo asset misclassified: %#v", res.Assets[0])
	}
	if res.Assets[1].Ref != "sha256-photo" || res.Assets[1].Kind != "image" || res.Assets[1].MIME != "image/png" {
		t.Fatalf("photo asset misclassified: %#v", res.Assets[1])
	}
	// The catalog must never leak the heavy data: URI bytes (image OR font).
	blob, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "data:image") {
		t.Fatalf("list_deck_assets leaked a data: URI: %s", blob)
	}
	if strings.Contains(string(blob), "data:font") {
		t.Fatalf("list_deck_assets leaked a font data: URI: %s", blob)
	}
}

func TestAddDeckImage(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)

	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "cover", Title: "Cover"}); err != nil {
		t.Fatal(err)
	}

	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 32))
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader(string(png))),
			Request:    req,
		}, nil
	})
	orig := newAssetIngestor
	newAssetIngestor = func() AssetIngestor { return AssetIngestor{Transport: transport} }
	t.Cleanup(func() { newAssetIngestor = orig })

	res, err := addDeckImage(ctx, AddDeckImageArgs{DeckSlug: "cover", URL: "https://example.com/steve.png"})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(png)
	wantRef := "sha256-" + hex.EncodeToString(sum[:])
	if res.AssetRef != wantRef {
		t.Fatalf("assetRef = %q, want %q", res.AssetRef, wantRef)
	}
	if res.MIME != "image/png" || res.Bytes != len(png) {
		t.Fatalf("unexpected mime/bytes: %#v", res)
	}
	// The manifest in the store now carries the ref -> data:image/png URI.
	deck, err := backend.GetDeck(ctx, "cover")
	if err != nil {
		t.Fatal(err)
	}
	got := deck.Assets[wantRef]
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("stored asset not a png data URI: %q", got)
	}
	// The returned catalog surfaces the new asset without leaking data.
	if res.Deck == nil || len(res.Deck.Assets) != 1 || res.Deck.Assets[0].Ref != wantRef {
		t.Fatalf("result catalog missing new asset: %#v", res.Deck)
	}

	// Idempotent: adding the same URL again does not duplicate the asset.
	if _, err := addDeckImage(ctx, AddDeckImageArgs{DeckSlug: "cover", URL: "https://example.com/steve.png"}); err != nil {
		t.Fatal(err)
	}
	deck2, err := backend.GetDeck(ctx, "cover")
	if err != nil {
		t.Fatal(err)
	}
	if len(deck2.Assets) != 1 {
		t.Fatalf("re-adding same image duplicated assets: %d", len(deck2.Assets))
	}
}

func TestAddDeckImageRejectsUnsafeURL(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "cover", Title: "Cover"}); err != nil {
		t.Fatal(err)
	}
	// Default (production) ingestor: a private-network URL must be rejected by
	// AssetIngestor's SSRF validation before any asset is stored.
	if _, err := addDeckImage(ctx, AddDeckImageArgs{DeckSlug: "cover", URL: "http://127.0.0.1/x.png"}); err == nil {
		t.Fatal("expected private URL rejection")
	}
	deck, err := backend.GetDeck(ctx, "cover")
	if err != nil {
		t.Fatal(err)
	}
	if len(deck.Assets) != 0 {
		t.Fatalf("rejected fetch must not persist an asset: %#v", deck.Assets)
	}
}

func TestListDecksToolHidesTemplateDecks(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "quarterly", Title: "Quarterly"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "acme",
		Tokens: map[string]string{"surface": "#FFFFFF"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#172033" size="72">{{TITLE}}</ast-text></ast-slide>`},
		},
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	res, err := listDecks(ctx, ListDecksArgs{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range res.Decks {
		if strings.HasPrefix(d.Slug, "tmpl/") {
			t.Fatalf("list_decks leaked template deck: %#v", d)
		}
		if d.Slug == "quarterly" {
			found = true
		}
	}
	if !found {
		t.Fatalf("list_decks dropped regular deck; got %#v", res.Decks)
	}
}

func TestAskUserSlidesTemplateAttachesThumbnailsAndOptions(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}

	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "acme",
		Tokens: map[string]string{"surface": "#0b1220", "ink": "#e2e8f0"},
		Assets: map[string]string{},
		Archetypes: []themes.Archetype{
			{Kind: "title", Title: "Blue cover", Tier: "fixed", Markup: `<ast-slide id="a"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#e2e8f0" size="72">{{TITLE}}</ast-text></ast-slide>`},
			{Kind: "title", Title: "Pink cover", Tier: "fixed", Markup: `<ast-slide id="b"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#e2e8f0" size="72">{{TITLE}}</ast-text></ast-slide>`},
			{Kind: "section", Title: "Divider", Tier: "fixed", Markup: `<ast-slide id="c"><ast-text id="h" x="160" y="470" w="1600" h="140" color="#e2e8f0" size="64">{{TITLE}}</ast-text></ast-slide>`},
		},
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	// Options omitted → auto-generated one per title variant, each with a live
	// slides-archetype thumbnail carrying the variant markup + shared theme.
	res, err := askUser(ctx, AskUserArgs{
		Kind:           "select",
		Prompt:         "Which cover?",
		SlidesTemplate: "acme",
		SlidesKind:     "title",
	})
	if err != nil {
		t.Fatalf("askUser: %v", err)
	}
	visual := optionsWithoutDefault(res.Options)
	if len(visual) != 2 {
		t.Fatalf("expected 2 auto-generated title options (+ default), got %d: %#v", len(res.Options), res.Options)
	}
	if !hasDefaultAskOption(res.Options) {
		t.Fatalf("title picker should include Use the default: %#v", res.Options)
	}
	for _, o := range visual {
		if o.Thumbnail == nil {
			t.Fatalf("option %q missing thumbnail", o.Label)
		}
		if o.Thumbnail.Kind != "slides-archetype" {
			t.Fatalf("option %q thumbnail kind = %q, want slides-archetype", o.Label, o.Thumbnail.Kind)
		}
		if strings.TrimSpace(o.Thumbnail.Markup) == "" {
			t.Fatalf("option %q thumbnail markup empty", o.Label)
		}
		if o.Thumbnail.Theme["surface"] != "#0b1220" {
			t.Fatalf("option %q thumbnail missing shared theme: %#v", o.Label, o.Thumbnail.Theme)
		}
		// The thumbnail must carry the template NAME (so the frontend resolves
		// asset-refs at render time) and MUST NOT embed a resolved asset map:
		// embedding data URIs here bloated model history to hundreds of MB.
		if o.Thumbnail.Template != "acme" {
			t.Fatalf("option %q thumbnail missing template name: %#v", o.Label, o.Thumbnail)
		}
	}

	// slidesKind filters to the single section variant.
	sec, err := askUser(ctx, AskUserArgs{Kind: "select", Prompt: "Which divider?", SlidesTemplate: "acme", SlidesKind: "section"})
	if err != nil {
		t.Fatalf("askUser section: %v", err)
	}
	if len(sec.Options) != 1 || sec.Options[0].Thumbnail == nil {
		t.Fatalf("expected 1 section option with thumbnail, got %#v", sec.Options)
	}

	// A stray single explicit option must NOT hide the other variants: when
	// slidesTemplate is set and fewer options than variants are supplied, the
	// full variant set replaces them.
	partial, err := askUser(ctx, AskUserArgs{
		Kind:           "select",
		Prompt:         "Which cover?",
		SlidesTemplate: "acme",
		SlidesKind:     "title",
		Options:        []AskUserOption{{ID: "blue-cover", Label: "Blue cover"}},
	})
	if err != nil {
		t.Fatalf("askUser partial: %v", err)
	}
	if len(optionsWithoutDefault(partial.Options)) != 2 {
		t.Fatalf("partial explicit options should expand to all 2 title variants, got %d: %#v", len(partial.Options), partial.Options)
	}
	for _, o := range optionsWithoutDefault(partial.Options) {
		if o.Thumbnail == nil {
			t.Fatalf("expanded option %q missing thumbnail", o.Label)
		}
	}
}

// TestGetTemplateVariantPreviewsMatchesVariantSuffixedKinds guards the "only one
// option shown" regression for imported templates: variant multiplicity is
// preserved by suffixing the role (title, title-2, title-3, …), so a filter by
// slidesKind="title" must return the whole role family, not just the exact match.
func TestGetTemplateVariantPreviewsMatchesVariantSuffixedKinds(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}

	mk := func(kind, title string) themes.Archetype {
		return themes.Archetype{Kind: kind, Title: title, Tier: "fixed",
			Markup: `<ast-slide id="` + kind + `"><ast-text id="h" x="0" y="0" w="1920" h="200" size="72">{{TITLE}}</ast-text></ast-slide>`}
	}
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "gco",
		Tokens: map[string]string{"surface": "#0b1220"},
		Archetypes: []themes.Archetype{
			mk("title", "White cover with blue pattern"),
			mk("title-2", "Blue cover, anvil and image"),
			mk("title-3", "Dark cover"),
			mk("section", "Divider A"),
			mk("section-2", "Divider B"),
			mk("content", "Content"),
		},
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	res, err := getTemplateVariantPreviews(ctx, TemplateVariantPreviewsArgs{Template: "gco", Kind: "title"})
	if err != nil {
		t.Fatalf("getTemplateVariantPreviews: %v", err)
	}
	if len(res.Variants) != 3 {
		t.Fatalf("expected 3 title-family variants, got %d: %#v", len(res.Variants), res.Variants)
	}
	if raw := jsonDump(t, res); strings.Contains(raw, "<ast-slide") {
		t.Fatalf("previews must not include markup: %s", raw)
	}

	// ask_user auto-generation must therefore surface all three as options.
	ask, err := askUser(ctx, AskUserArgs{Kind: "select", Prompt: "Which cover?", SlidesTemplate: "gco", SlidesKind: "title"})
	if err != nil {
		t.Fatalf("askUser: %v", err)
	}
	if len(optionsWithoutDefault(ask.Options)) != 3 {
		t.Fatalf("expected 3 title options (+ default), got %d: %#v", len(ask.Options), ask.Options)
	}
}

func TestAskUserUsesBakedThumbnailWithoutMarkup(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "baked",
		Tokens: map[string]string{"surface": "#0b1220"},
		Assets: map[string]string{"thumb/title": "data:image/png;base64,AAAA"},
		Archetypes: []themes.Archetype{
			{Kind: "section", Title: "Section", Tier: "fixed", ThumbnailRef: "thumb/title",
				Markup: `<ast-slide id="a"><ast-text id="h" x="0" y="0" w="100" h="100">{{TITLE}}</ast-text></ast-slide>`},
		},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	res, err := askUser(ctx, AskUserArgs{Kind: "select", Prompt: "Cover?", SlidesTemplate: "baked", SlidesKind: "section"})
	if err != nil {
		t.Fatal(err)
	}
	visual := optionsWithoutDefault(res.Options)
	if len(visual) != 1 || visual[0].Thumbnail == nil {
		t.Fatalf("got %#v", res.Options)
	}
	th := visual[0].Thumbnail
	if th.Kind != "image" || th.AssetRef != "thumb/title" || th.Template != "baked" {
		t.Fatalf("expected baked image thumb, got %#v", th)
	}
	if strings.Contains(jsonDump(t, res), "<ast-slide") {
		t.Fatalf("ask_user must not dump markup when a baked thumb exists: %s", jsonDump(t, res))
	}
}

// TestAskUserSlidesTemplateUniqueOptionIDs guards the "only one option shown"
// regression: when title variants have EMPTY labels (common for imported
// templates), every option must still get a distinct, non-empty id and label.
// Duplicate ids would collapse into a single rendered tile on the frontend
// (which keys tiles by id).
func TestAskUserSlidesTemplateUniqueOptionIDs(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}

	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "acme",
		Tokens: map[string]string{"surface": "#0b1220"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="a"><ast-text id="h" x="0" y="0" w="1920" h="200" size="72">{{TITLE}}</ast-text></ast-slide>`},
			{Kind: "title", Markup: `<ast-slide id="b"><ast-text id="h" x="0" y="0" w="1920" h="200" size="72">{{TITLE}}</ast-text></ast-slide>`},
			{Kind: "title", Markup: `<ast-slide id="c"><ast-text id="h" x="0" y="0" w="1920" h="200" size="72">{{TITLE}}</ast-text></ast-slide>`},
		},
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	res, err := askUser(ctx, AskUserArgs{
		Kind:           "select",
		Prompt:         "Which cover?",
		SlidesTemplate: "acme",
		SlidesKind:     "title",
	})
	if err != nil {
		t.Fatalf("askUser: %v", err)
	}
	if len(optionsWithoutDefault(res.Options)) != 3 {
		t.Fatalf("expected 3 title options (+ default), got %d: %#v", len(res.Options), res.Options)
	}
	seen := make(map[string]bool, len(res.Options))
	for _, o := range res.Options {
		if strings.TrimSpace(o.ID) == "" {
			t.Fatalf("option has empty id: %#v", o)
		}
		if strings.TrimSpace(o.Label) == "" {
			t.Fatalf("option has empty label: %#v", o)
		}
		if seen[o.ID] {
			t.Fatalf("duplicate option id %q would collapse tiles: %#v", o.ID, res.Options)
		}
		seen[o.ID] = true
	}
}

// TestAskUserSlidesTemplatePicker verifies the FIRST question — "which template
// should I use?" — renders as a visual card: slidesTemplatePicker=true (with no
// options) auto-generates one option per available template (built-in + scoped),
// each carrying a live cover thumbnail (the template's title archetype markup)
// tagged with the template name so the frontend resolves asset-refs at render
// time. It must never embed data: bytes.
func TestAskUserSlidesTemplatePicker(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}

	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:        "acme",
		Label:       "Acme Brand",
		Description: "The corporate template",
		Tokens:      map[string]string{"surface": "#0b1220", "ink": "#e2e8f0"},
		Assets:      map[string]string{},
		Archetypes: []themes.Archetype{
			// A non-title archetype first, to prove the picker prefers the title
			// (cover) role rather than blindly taking the first archetype.
			{Kind: "content", Title: "Body", Tier: "flexible", Markup: `<ast-slide id="body"><ast-text id="b" x="160" y="320" w="1600" h="600" size="36">{{BODY}}</ast-text></ast-slide>`},
			{Kind: "title", Title: "Blue cover", Tier: "fixed", Markup: `<ast-slide id="cover"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#e2e8f0" size="72">{{TITLE}}</ast-text></ast-slide>`},
		},
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	res, err := askUser(ctx, AskUserArgs{
		Kind:                 "select",
		Prompt:               "Which template would you like?",
		SlidesTemplatePicker: true,
	})
	if err != nil {
		t.Fatalf("askUser: %v", err)
	}

	// The scoped "acme" template must appear as one option, id == template name.
	var acme *AskUserOptionPayload
	seen := make(map[string]bool, len(res.Options))
	for i := range res.Options {
		o := &res.Options[i]
		if seen[o.ID] {
			t.Fatalf("duplicate option id %q would collapse tiles: %#v", o.ID, res.Options)
		}
		seen[o.ID] = true
		if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.Label) == "" {
			t.Fatalf("template option missing id/label: %#v", o)
		}
		if o.ID == "acme" {
			acme = o
		}
	}
	// Built-in templates are also enumerated, so there must be more than one.
	if len(res.Options) < 2 {
		t.Fatalf("expected built-ins + acme, got %d options: %#v", len(res.Options), res.Options)
	}
	if acme == nil {
		t.Fatalf("scoped template 'acme' not offered: %#v", res.Options)
	}
	if acme.Label != "Acme Brand" {
		t.Fatalf("acme label = %q, want catalog label 'Acme Brand'", acme.Label)
	}
	if acme.Description != "The corporate template" {
		t.Fatalf("acme description = %q, want catalog description", acme.Description)
	}
	if acme.Thumbnail == nil {
		t.Fatalf("acme option missing cover thumbnail")
	}
	if acme.Thumbnail.Kind != "slides-archetype" {
		t.Fatalf("acme thumbnail kind = %q, want slides-archetype", acme.Thumbnail.Kind)
	}
	// The cover thumbnail must be the TITLE archetype, not the first (content).
	if !strings.Contains(acme.Thumbnail.Markup, `id="cover"`) {
		t.Fatalf("acme thumbnail should render the title cover, got: %s", acme.Thumbnail.Markup)
	}
	if acme.Thumbnail.Template != "acme" {
		t.Fatalf("acme thumbnail missing template name for asset resolution: %#v", acme.Thumbnail)
	}
	if acme.Thumbnail.Theme["surface"] != "#0b1220" {
		t.Fatalf("acme thumbnail missing shared theme tokens: %#v", acme.Thumbnail.Theme)
	}
	// Guard the no-data:-bytes invariant: the picker never embeds a resolved
	// asset map (that would bloat model history / persisted message by MBs).
	if acme.Thumbnail.AssetRef != "" {
		t.Fatalf("template picker must not embed asset bytes/refs; got %q", acme.Thumbnail.AssetRef)
	}
}

// reviewFindingsWithCode returns the findings from result matching code.
func reviewFindingsWithCode(findings []ReviewFinding, code string) []ReviewFinding {
	var out []ReviewFinding
	for _, f := range findings {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

func TestReviewDeckDetectsRunAdjacency(t *testing.T) {
	// A timeline label authored as two adjacent runs with no separating space
	// ("1972" + "Founded") is the "1972Founded" collision — review_deck must
	// flag it as a warning.
	collide := `<ast-slide id="s"><ast-text id="label" x="100" y="400" w="200" h="120">` +
		`<ast-run b>1972</ast-run><ast-run>Founded</ast-run></ast-text></ast-slide>`
	// The same content with a trailing space inside the date run, or a leading
	// space in the label run, is correctly separated — no finding.
	spacedTrailing := `<ast-slide id="s"><ast-text id="label" x="100" y="400" w="200" h="120">` +
		`<ast-run b>1972 </ast-run><ast-run>Founded</ast-run></ast-text></ast-slide>`
	spacedLeading := `<ast-slide id="s"><ast-text id="label" x="100" y="400" w="200" h="120">` +
		`<ast-run b>1972</ast-run><ast-run> Founded</ast-run></ast-text></ast-slide>`

	for _, tc := range []struct {
		name       string
		markup     string
		wantColide bool
	}{
		{"collides", collide, true},
		{"trailing space in first run", spacedTrailing, false},
		{"leading space in second run", spacedLeading, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &memoryDocsStore{}
			ctx := toolContext(t, backend)
			if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck"}); err != nil {
				t.Fatal(err)
			}
			if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: tc.markup}); err != nil {
				t.Fatal(err)
			}
			result, err := reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
			if err != nil {
				t.Fatal(err)
			}
			adj := reviewFindingsWithCode(result.Findings, "run_adjacency")
			if tc.wantColide {
				if len(adj) != 1 {
					t.Fatalf("expected 1 run_adjacency finding, got %d: %#v", len(adj), result.Findings)
				}
				if adj[0].Severity != "warning" {
					t.Fatalf("run_adjacency must be a warning, got %q", adj[0].Severity)
				}
				if adj[0].NodeID != "label" {
					t.Fatalf("finding should name the offending node, got %q", adj[0].NodeID)
				}
			} else if len(adj) != 0 {
				t.Fatalf("expected no run_adjacency finding for %q, got %#v", tc.name, adj)
			}
		})
	}
}

func TestReviewDeckChecklistAlwaysPresent(t *testing.T) {
	backend := &memoryDocsStore{}
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck"}); err != nil {
		t.Fatal(err)
	}
	// A clean slide with no heuristic findings still returns the checklist so
	// the model always has review guidance.
	clean := `<ast-slide id="s"><ast-text id="t" x="96" y="72" w="1728" h="100">Clean title</ast-text></ast-slide>`
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: clean}); err != nil {
		t.Fatal(err)
	}
	result, err := reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Checklist) == 0 {
		t.Fatal("review_deck must always return a non-empty checklist")
	}
	if len(reviewFindingsWithCode(result.Findings, "run_adjacency")) != 0 {
		t.Fatalf("clean slide should have no run_adjacency finding: %#v", result.Findings)
	}
	if result.Message == "" {
		t.Fatal("review_deck must return an instruction message")
	}
	if result.SlideCount != 1 {
		t.Fatalf("SlideCount = %d, want 1", result.SlideCount)
	}
}

func TestReviewDeckOmitsHeavyData(t *testing.T) {
	backend := &memoryDocsStore{}
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck"}); err != nil {
		t.Fatal(err)
	}
	// Add a real image asset so the deck carries data: bytes in its store.
	svc := Service{Store: backend}
	if _, err := svc.AddDeckAsset(ctx, "deck", "sha256-abc", "data:image/png;base64,AAAA"); err != nil {
		t.Fatal(err)
	}
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0,
		Markup: `<ast-slide id="s"><ast-text id="t" x="96" y="72" w="1728" h="100">Title</ast-text></ast-slide>`}); err != nil {
		t.Fatal(err)
	}
	result, err := reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "data:image") || strings.Contains(string(raw), "base64,AAAA") {
		t.Fatalf("review_deck result leaked heavy asset data: %s", raw)
	}
	// The slim DeckView reports assets exist via a count, never the bytes.
	if result.Deck == nil || result.Deck.AssetCount == 0 {
		t.Fatalf("expected slim deck view with asset count, got %#v", result.Deck)
	}
}

func TestReviewDeckChromeHeuristics(t *testing.T) {
	backend := &memoryDocsStore{}
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck"}); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: backend}
	if _, err := svc.AddDeckAsset(ctx, "deck", "sha256-abc", "data:image/png;base64,AAAA"); err != nil {
		t.Fatal(err)
	}

	chevron := `<ast-slide id="s">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-shape id="c-1" kind="rect" x="80" y="300" w="560" h="80" geom="chevron" fill="#003FC9"></ast-shape>` +
		`<ast-text id="ph-1" x="100" y="310" w="500" h="60">Apple I</ast-text>` +
		`</ast-slide>`
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: chevron}); err != nil {
		t.Fatal(err)
	}
	result, err := reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewFindingsWithCode(result.Findings, "missing_chrome"); len(got) != 0 {
		t.Fatalf("chevron card slide should not be missing_chrome, got %#v", got)
	}
	if got := reviewFindingsWithCode(result.Findings, "low_contrast"); len(got) != 0 {
		t.Fatalf("white full-canvas bg should not be low_contrast, got %#v", got)
	}

	titleBody := `<ast-slide id="s">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-text id="ph-1" x="80" y="80" w="1760" h="80">A title</ast-text>` +
		`<ast-text id="ph-2" x="80" y="200" w="1760" h="600">A wall of body copy that is just title and text.</ast-text>` +
		`</ast-slide>`
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: titleBody}); err != nil {
		t.Fatal(err)
	}
	result, err = reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewFindingsWithCode(result.Findings, "missing_chrome"); len(got) != 1 {
		t.Fatalf("title+body wall should be missing_chrome, got %#v", result.Findings)
	}

	divider := `<ast-slide id="s">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-text id="ph-1" x="80" y="480" w="1760" h="80">Founding Apple in 1976</ast-text>` +
		`</ast-slide>`
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: divider}); err != nil {
		t.Fatal(err)
	}
	result, err = reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewFindingsWithCode(result.Findings, "missing_chrome"); len(got) != 0 {
		t.Fatalf("single-title divider should not be missing_chrome, got %#v", got)
	}

	emptyPic := `<ast-slide id="s">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-shape id="ph-pic-2" kind="rect" x="960" y="0" w="960" h="1080" geom="rect" fill="#89D1FF" alt="Image"></ast-shape>` +
		`<ast-text id="ph-1" x="80" y="400" w="800" h="120">Cover</ast-text>` +
		`</ast-slide>`
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: emptyPic}); err != nil {
		t.Fatal(err)
	}
	result, err = reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewFindingsWithCode(result.Findings, "unfilled_image_slot"); len(got) != 1 {
		t.Fatalf("empty ph-pic panel should be unfilled_image_slot, got %#v", result.Findings)
	}
}

func TestReviewDeckSparseSection(t *testing.T) {
	backend := &memoryDocsStore{}
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Theme: map[string]string{themeKeyTemplateName: "brand"}}); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: backend}
	if _, err := svc.AddDeckAsset(ctx, "deck", "sha256-abc", "data:image/png;base64,AAAA"); err != nil {
		t.Fatal(err)
	}
	divider := func(id, title string) string {
		return `<ast-slide id="` + id + `">` +
			`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
			`<ast-text id="ph-1" x="80" y="480" w="1760" h="80">` + title + `</ast-text></ast-slide>`
	}
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: divider("a", "Early Life")}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 1, Markup: divider("b", "Founding Apple")}); err != nil {
		t.Fatal(err)
	}
	result, err := reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewFindingsWithCode(result.Findings, "sparse_section"); len(got) != 1 {
		t.Fatalf("two title-only dividers should be sparse_section, got %#v", result.Findings)
	}
	if got := reviewFindingsWithCode(result.Findings, "sparse_slide"); len(got) == 0 {
		t.Fatalf("second title-only slide should be sparse_slide, got %#v", result.Findings)
	}
}

func TestReviewDeckRecipeEyebrowAndNominalTitle(t *testing.T) {
	backend := &memoryDocsStore{}
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Theme: map[string]string{themeKeyTemplateName: "brand"}}); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: backend}
	if _, err := svc.AddDeckAsset(ctx, "deck", "sha256-abc", "data:image/png;base64,AAAA"); err != nil {
		t.Fatal(err)
	}
	markup := `<ast-slide id="s">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-text id="eyebrow" x="120" y="72" w="800" h="28"></ast-text>` +
		`<ast-text id="headline" x="120" y="140" w="1600" h="80" size="56">Early life</ast-text>` +
		`<ast-text id="body_1" x="120" y="400" w="800" h="200">A paragraph that uses the canvas.</ast-text>` +
		`<ast-text id="item_1_title" x="1000" y="400" w="700" h="40">Craft</ast-text>` +
		`<ast-text id="item_1_body" x="1000" y="450" w="700" h="80">A complete sentence about craft.</ast-text>` +
		`<ast-text id="chrome-footer" x="120" y="1036" w="800" h="24">Deck</ast-text>` +
		`</ast-slide>`
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: markup}); err != nil {
		t.Fatal(err)
	}
	result, err := reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewFindingsWithCode(result.Findings, "missing_eyebrow"); len(got) != 1 {
		t.Fatalf("empty eyebrow should warn, got %#v", result.Findings)
	}
	if got := reviewFindingsWithCode(result.Findings, "missing_chrome"); len(got) != 0 {
		t.Fatalf("recipe layout should not be missing_chrome, got %#v", result.Findings)
	}
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 1, Markup: markup}); err != nil {
		t.Fatal(err)
	}
	result, err = reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewFindingsWithCode(result.Findings, "nominal_title"); len(got) == 0 {
		t.Fatalf("Early life should be nominal_title on a body slide, got %#v", result.Findings)
	}
}

func TestReviewDeckEmptyCardAndMissingTitle(t *testing.T) {
	backend := &memoryDocsStore{}
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Theme: map[string]string{themeKeyTemplateName: "brand"}}); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: backend}
	if _, err := svc.AddDeckAsset(ctx, "deck", "sha256-abc", "data:image/png;base64,AAAA"); err != nil {
		t.Fatal(err)
	}
	emptyCards := `<ast-slide id="s">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-text id="ph-1" x="80" y="48" w="1600" h="80" size="36">The Journey</ast-text>` +
		`<ast-shape id="c-1" kind="rect" x="80" y="200" w="800" h="280" geom="roundRect" fill="#DBEAFE"></ast-shape>` +
		`<ast-shape id="c-2" kind="rect" x="960" y="200" w="800" h="280" geom="roundRect" fill="#A6E0FF"></ast-shape>` +
		`</ast-slide>`
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: emptyCards}); err != nil {
		t.Fatal(err)
	}
	result, err := reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewFindingsWithCode(result.Findings, "empty_card"); len(got) < 2 {
		t.Fatalf("empty roundRects should be empty_card, got %#v", result.Findings)
	}

	noTitle := `<ast-slide id="s">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-text id="ph-1" x="80" y="500" w="280" h="40" size="16">1997</ast-text>` +
		`</ast-slide>`
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: noTitle}); err != nil {
		t.Fatal(err)
	}
	result, err = reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewFindingsWithCode(result.Findings, "missing_title"); len(got) != 1 {
		t.Fatalf("small mid-slide caption should be missing_title, got %#v", result.Findings)
	}

	photoCard := `<ast-slide id="s">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-text id="ph-1" x="80" y="48" w="1600" h="80" size="36">Title</ast-text>` +
		`<ast-shape id="c-1" kind="rect" x="900" y="200" w="900" h="700" geom="roundRect" fill="#DBEAFE"></ast-shape>` +
		`<ast-image id="pic" x="910" y="210" w="880" h="680" asset-ref="sha256-abc"></ast-image>` +
		`</ast-slide>`
	if _, err := writeSlide(ctx, WriteSlideArgs{DeckSlug: "deck", Position: 0, Markup: photoCard}); err != nil {
		t.Fatal(err)
	}
	result, err = reviewDeck(ctx, ReviewDeckArgs{Slug: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewFindingsWithCode(result.Findings, "empty_card"); len(got) != 0 {
		t.Fatalf("a card covered by a photograph is not an empty text card, got %#v", result.Findings)
	}
}

func TestFillSlidesRequiresEveryTextSlot(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	markup := `<ast-slide id="p"><ast-text id="ph-1" x="10" y="10" w="400" h="80" size="24"><ast-run>{{TITLE}}</ast-run></ast-text>` +
		`<ast-text id="ph-2" x="10" y="100" w="400" h="80" size="20"><ast-run>{{BODY}}</ast-run></ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "brand",
		Tokens: map[string]string{"surface": "#FFFFFF"},
		Archetypes: []themes.Archetype{
			{Kind: "pattern", Markup: markup, FillSlots: []string{"ph-1", "ph-2"}, SlotHints: []themes.SlotHint{{ID: "ph-1", Role: "title"}, {ID: "ph-2", Role: "body"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "brand"}); err != nil {
		t.Fatal(err)
	}
	_, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "deck",
		Slides: []FillSlideSpec{
			{Position: 0, Kind: "pattern", Fills: map[string]string{"ph-1": "Only the title"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "ph-2") {
		t.Fatalf("expected missing ph-2 error, got %v", err)
	}
}

func TestCreateDeckCatalogOmitsPatternsKeepsTitleFamily(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	mk := func(kind, title string) themes.Archetype {
		return themes.Archetype{Kind: kind, Title: title, Tier: "fixed",
			Markup:    `<ast-slide id="` + kind + `"><ast-text id="h" x="0" y="0" w="1920" h="200" size="72">{{TITLE}}</ast-text></ast-slide>`,
			FillSlots: []string{"h"}}
	}
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "gco",
		Tokens: map[string]string{"surface": "#0b1220", "ink": "#fff", "accent": "#3b82f6"},
		Archetypes: []themes.Archetype{
			mk("title", "White cover"),
			mk("title-2", "Blue cover"),
			mk("closing", "Thank you"),
			mk("section", "Divider"),
			{Kind: "pattern", Title: "Cards", Tier: "flexible", Markup: `<ast-slide id="p"></ast-slide>`},
			{Kind: "content", Title: "Title and Text", Markup: `<ast-slide id="c"></ast-slide>`},
		},
	}); err != nil {
		t.Fatal(err)
	}
	created, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "gco", TitleKind: "title-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !catalogHasKind(created.Catalog, "title") || !catalogHasKind(created.Catalog, "title-2") || !catalogHasKind(created.Catalog, "closing") {
		t.Fatalf("catalog missing title family: %#v", created.Catalog)
	}
	if catalogHasKind(created.Catalog, "pattern") || catalogHasKind(created.Catalog, "section") || catalogHasKind(created.Catalog, "content") {
		t.Fatalf("catalog should omit pattern/section/content: %#v", created.Catalog)
	}
	if created.Deck.Theme[themeKeyTitleKind] != "title-2" {
		t.Fatalf("title kind not stamped: %#v", created.Deck.Theme)
	}
	if created.Deck.Theme[themeKeyClosingKind] != "closing" {
		t.Fatalf("single closing should be auto-stamped: %#v", created.Deck.Theme)
	}
	raw := jsonDump(t, created)
	if strings.Contains(raw, "pattern") && strings.Contains(raw, `"kind":"pattern"`) {
		t.Fatalf("create_deck JSON leaked pattern into catalog: %s", raw)
	}
}

func TestCreateDeckRequiresTitleKindWhenMultipleCovers(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	mk := func(kind, title string) themes.Archetype {
		return themes.Archetype{Kind: kind, Title: title, Tier: "fixed",
			Markup:    `<ast-slide id="` + kind + `"><ast-text id="h" x="0" y="0" w="1920" h="200" size="72">{{TITLE}}</ast-text></ast-slide>`,
			FillSlots: []string{"h"}}
	}
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name: "gco", Tokens: map[string]string{"surface": "#fff"},
		Archetypes: []themes.Archetype{mk("title", "White cover"), mk("title-2", "Blue cover")},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "gco"})
	if err == nil || !strings.Contains(err.Error(), "titleKind is required") {
		t.Fatalf("expected titleKind required, got %v", err)
	}
	created, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "gco", TitleKind: "title-2"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Deck.Theme[themeKeyTitleKind] != "title-2" {
		t.Fatalf("stamped %#v", created.Deck.Theme)
	}
}

func TestCreateDeckRequiresTitleImageWhenCoverHasWell(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	markup := `<ast-slide id="t"><ast-text id="ph-1" x="40" y="40" w="800" h="80">{{TITLE}}</ast-text>` +
		`<ast-shape id="ph-pic-2" kind="rect" x="960" y="0" w="960" h="1080" fill="#89D1FF"></ast-shape></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name: "gco", Tokens: map[string]string{"surface": "#fff", "muted": "#89D1FF"},
		Archetypes: []themes.Archetype{{
			Kind: "title", Title: "Split cover", Tier: "fixed", Markup: markup,
			FillSlots: []string{"ph-1", "ph-pic-2"}, SlotHints: []themes.SlotHint{{ID: "ph-pic-2", Role: "image"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "gco", TitleKind: "title"})
	if err == nil || !strings.Contains(err.Error(), "titleImage is required") {
		t.Fatalf("expected titleImage required, got %v", err)
	}
	created, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "gco", TitleKind: "title", TitleImage: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Deck.Theme[themeKeyTitleImage] != "" {
		t.Fatalf("none must not stamp a photo: %#v", created.Deck.Theme)
	}
}

func TestFillSlidesUsesStampedTitleKind(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	white := `<ast-slide id="white"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-text id="ph-1" x="160" y="380" w="800" h="120">{{TITLE}}</ast-text></ast-slide>`
	blue := `<ast-slide id="blue"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#0011AA" decorative="true"></ast-shape>` +
		`<ast-text id="ph-1" x="160" y="380" w="800" h="120">{{TITLE}}</ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name: "gco", Tokens: map[string]string{"surface": "#fff"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Title: "White cover", Tier: "fixed", Markup: white, FillSlots: []string{"ph-1"}},
			{Kind: "title-2", Title: "Blue cover", Tier: "fixed", Markup: blue, FillSlots: []string{"ph-1"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "gco", TitleKind: "title-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "deck",
		Slides:   []FillSlideSpec{{Position: 0, Kind: "title", Fills: map[string]string{"ph-1": "Steve Jobs"}}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "deck", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Slide.Content, `fill="#0011AA"`) || strings.Contains(got.Slide.Content, `fill="#FFFFFF"`) {
		t.Fatalf("stamped title-2 should win over fill kind=title:\n%s", got.Slide.Content)
	}
}

func TestCreateDeckProductPaletteOverlaysTokens(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	created, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "product", Palette: "orange"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Deck.Theme["accent"] != "#F97316" {
		t.Fatalf("orange palette accent = %q", created.Deck.Theme["accent"])
	}
	if created.Deck.Theme[themeKeyPalette] != "orange" {
		t.Fatalf("palette id not stamped: %#v", created.Deck.Theme)
	}
	if len(created.Palettes) < 8 {
		t.Fatalf("create_deck should list product palettes, got %d", len(created.Palettes))
	}
	listed, err := listTemplates(ctx, ListTemplatesArgs{})
	if err != nil {
		t.Fatal(err)
	}
	var product *TemplateSummary
	for i := range listed.Templates {
		if listed.Templates[i].Name == "product" {
			product = &listed.Templates[i]
			break
		}
	}
	if product == nil || product.Label != "Product Deck" || len(product.Palettes) < 8 {
		t.Fatalf("list_slide_templates product: %#v", product)
	}
}

func TestCreateDeckUnknownPalette(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	_, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "product", Palette: "hot-pink"})
	if err == nil || !strings.Contains(err.Error(), "unknown palette") {
		t.Fatalf("expected unknown palette error, got %v", err)
	}
}

func TestFillSlidesOfficialTitleDoesNotRenderRecipe(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	markup := `<ast-slide id="title-2"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#0011AA" decorative="true"></ast-shape>` +
		`<ast-text id="ph-1" x="160" y="380" w="1600" h="200" size="72">{{TITLE}}</ast-text>` +
		`<ast-text id="ph-2" x="160" y="620" w="1600" h="140" size="36">{{BODY}}</ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "gco",
		Tokens: map[string]string{"surface": "#0011AA", "ink": "#FFFFFF", "accent": "#FFFFFF"},
		Archetypes: []themes.Archetype{
			{Kind: "title-2", Title: "Blue cover", Tier: "fixed", Markup: markup, FillSlots: []string{"ph-1", "ph-2"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "gco"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "deck",
		Slides: []FillSlideSpec{
			{Position: 0, Kind: "title-2", Fills: map[string]string{"headline": "Steve Jobs", "dek": "1955–2011"}},
		},
	}); err != nil {
		t.Fatalf("fill official title with headline/dek aliases: %v", err)
	}
	got, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "deck", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	body := got.Slide.Content
	if !strings.Contains(body, "Steve Jobs") || !strings.Contains(body, "1955") {
		t.Fatalf("bookend aliases not applied:\n%s", body)
	}
	if strings.Contains(body, "recipe-cover") || strings.Contains(body, `id="eyebrow"`) {
		t.Fatalf("official title should not run the recipe renderer:\n%s", body)
	}
	if strings.Contains(body, "{{TITLE}}") || strings.Contains(body, "{{BODY}}") {
		t.Fatalf("placeholders remain:\n%s", body)
	}
}

func TestFillSlidesProductPaletteRecolorsRecipes(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "product", Palette: "orange"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "deck",
		Slides: []FillSlideSpec{{
			Position: 0, Kind: RecipeCover,
			Fills: map[string]string{
				"eyebrow": "LEGACY", "headline": "Stay hungry", "dek": "A one-sentence thesis that fits the dek.",
				"meta_1_label": "Ask", "meta_1_value": "Remember", "meta_2_label": "When", "meta_2_value": "2005",
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "deck", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	body := got.Slide.Content
	if !strings.Contains(body, "#F97316") {
		t.Fatalf("orange palette should appear in recipe markup:\n%s", body)
	}
}

func TestAskUserSlidesPalettePicker(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	res, err := askUser(ctx, AskUserArgs{
		Kind:                "select",
		Prompt:              "Which color?",
		SlidesTemplate:      "product",
		SlidesPalettePicker: true,
	})
	if err != nil {
		t.Fatalf("askUser palette: %v", err)
	}
	prod, ok := themes.LookupTemplate("product")
	if !ok {
		t.Fatal("missing product")
	}
	visual := optionsWithoutDefault(res.Options)
	if len(visual) != len(prod.Palettes) {
		t.Fatalf("expected %d palette options (+ default), got %d: %#v", len(prod.Palettes), len(res.Options), optionLabels(res.Options))
	}
	if !hasDefaultAskOption(res.Options) {
		t.Fatal("palette picker should include Use the default")
	}
	accents := map[string]bool{}
	for _, o := range visual {
		if o.Thumbnail == nil {
			t.Fatalf("palette %q missing thumbnail", o.Label)
		}
		if o.Thumbnail.Kind != "slides-archetype" {
			t.Fatalf("palette %q kind = %q", o.Label, o.Thumbnail.Kind)
		}
		if strings.TrimSpace(o.Thumbnail.Markup) == "" {
			t.Fatalf("palette %q empty markup", o.Label)
		}
		if o.Thumbnail.Template != "product" {
			t.Fatalf("palette %q template = %q", o.Label, o.Thumbnail.Template)
		}
		acc := o.Thumbnail.Theme["accent"]
		if acc == "" {
			t.Fatalf("palette %q missing accent in thumbnail theme: %#v", o.Label, o.Thumbnail.Theme)
		}
		accents[acc] = true
	}
	if len(accents) < 5 {
		t.Fatalf("palette thumbnails should differ by accent, got %v", accents)
	}
	raw := jsonDump(t, res)
	if strings.Contains(raw, "data:image") {
		t.Fatalf("palette picker must not embed data: bytes")
	}
}

func TestAskUserSlidesPalettePickerRequiresPalettes(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	_, err := askUser(ctx, AskUserArgs{
		Kind:                "select",
		Prompt:              "Which color?",
		SlidesTemplate:      "midnight",
		SlidesPalettePicker: true,
	})
	if err == nil || !strings.Contains(err.Error(), "no color palettes") {
		t.Fatalf("expected no-palettes error, got %v", err)
	}
}

func optionLabels(opts []AskUserOptionPayload) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.ID+":"+o.Label)
	}
	return out
}

func TestAskUserTitlePickerLeadsWithRicherCover(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	white := `<ast-slide id="white"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#FFFFFF" decorative="true"></ast-shape>` +
		`<ast-text id="ph-1" x="45" y="426" w="857" h="157" size="54">{{TITLE}}</ast-text></ast-slide>`
	blue := `<ast-slide id="blue"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#0057D2" decorative="true"></ast-shape>` +
		`<ast-image id="hero" x="960" y="0" w="960" h="1080" asset-ref="sha256-abc" fit="cover" decorative="true"></ast-image>` +
		`<ast-text id="ph-1" x="45" y="426" w="800" h="157" size="54" color="#FFFFFF">{{TITLE}}</ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "gco",
		Tokens: map[string]string{"surface": "#FFFFFF"},
		Archetypes: []themes.Archetype{
			{Kind: "title-2", Title: "White cover with blue pattern", Tier: "fixed", Markup: white, FillSlots: []string{"ph-1"}},
			{Kind: "title", Title: "1_Blue cover, anvil and image", Tier: "fixed", Markup: blue, FillSlots: []string{"ph-1", "ph-pic-2"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := askUser(ctx, AskUserArgs{Kind: "select", Prompt: "Which cover?", SlidesTemplate: "gco", SlidesKind: "title"})
	if err != nil {
		t.Fatal(err)
	}
	visual := optionsWithoutDefault(res.Options)
	if len(visual) < 2 {
		t.Fatalf("got %#v", res.Options)
	}
	if visual[0].Label != "1_Blue cover, anvil and image" {
		t.Fatalf("richest cover should be first, got %q then %q", visual[0].Label, visual[1].Label)
	}
	if visual[0].ID != "title" {
		t.Fatalf("option id should be catalog kind, got %q", visual[0].ID)
	}
}

func TestFillSlidesStripsUnselectedTitlePhotos(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	markup := `<ast-slide id="title"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#0057D2" decorative="true"></ast-shape>` +
		`<ast-image id="hero" x="0" y="0" w="1920" h="1080" asset-ref="sha256-coverphoto" fit="cover" decorative="true"></ast-image>` +
		`<ast-image id="ph-pic-2" x="1234" y="0" w="686" h="1080" asset-ref="sha256-hero" fit="cover" decorative="true"></ast-image>` +
		`<ast-image id="logo" x="40" y="40" w="120" h="40" asset-ref="sha256-logo" decorative="true"></ast-image>` +
		`<ast-text id="ph-1" x="45" y="426" w="800" h="157" size="54" color="#FFFFFF">{{TITLE}}</ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "gco",
		Tokens: map[string]string{"surface": "#0057D2", "muted": "#89D1FF"},
		Assets: map[string]string{
			"sha256-coverphoto": "data:image/png;base64,AAAA",
			"sha256-hero":       "data:image/png;base64,BBBB",
			"sha256-logo":       "data:image/png;base64,CCCC",
		},
		Archetypes: []themes.Archetype{
			{Kind: "title", Title: "Blue cover", Tier: "fixed", Markup: markup, FillSlots: []string{"ph-1", "ph-pic-2"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "gco", TitleKind: "title", TitleImage: "none"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "deck",
		Slides: []FillSlideSpec{
			{Position: 0, Kind: "title", Fills: map[string]string{"ph-1": "Steve Jobs"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "deck", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	body := got.Slide.Content
	if !strings.Contains(body, "Steve Jobs") {
		t.Fatalf("title text missing:\n%s", body)
	}
	if strings.Contains(body, `asset-ref="sha256-coverphoto"`) || strings.Contains(body, `asset-ref="sha256-hero"`) {
		t.Fatalf("unselected sample photos must not remain:\n%s", body)
	}
	if !strings.Contains(body, `id="ph-pic-2"`) || !strings.Contains(body, `fill="#89D1FF"`) {
		t.Fatalf("image well should stay as muted shape:\n%s", body)
	}
	if !strings.Contains(body, `asset-ref="sha256-logo"`) {
		t.Fatalf("logo must remain:\n%s", body)
	}
}

func TestFillSlidesAppliesTitleImage(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	markup := `<ast-slide id="title"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#0057D2" decorative="true"></ast-shape>` +
		`<ast-image id="ph-pic-2" x="1234" y="0" w="686" h="1080" asset-ref="sha256-hero" fit="cover" decorative="true"></ast-image>` +
		`<ast-text id="ph-1" x="45" y="426" w="800" h="157" size="54" color="#FFFFFF">{{TITLE}}</ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "gco",
		Tokens: map[string]string{"surface": "#0057D2"},
		Assets: map[string]string{
			"sha256-hero":     "data:image/png;base64,BBBB",
			"sha256-aabbccdd": "data:image/png;base64,DDDD",
		},
		Archetypes: []themes.Archetype{
			{Kind: "title", Title: "Blue cover", Tier: "fixed", Markup: markup, FillSlots: []string{"ph-1", "ph-pic-2"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := createDeck(ctx, CreateDeckArgs{Slug: "deck", Title: "Deck", Template: "gco", TitleKind: "title", TitleImage: "sha256-aabbccdd"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fillSlides(ctx, FillSlidesArgs{
		DeckSlug: "deck",
		Slides: []FillSlideSpec{
			{Position: 0, Kind: "title", Fills: map[string]string{"ph-1": "Steve Jobs"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := readSlide(ctx, ReadSlideArgs{DeckSlug: "deck", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	body := got.Slide.Content
	if !strings.Contains(body, `asset-ref="sha256-aabbccdd"`) {
		t.Fatalf("chosen titleImage missing:\n%s", body)
	}
	if strings.Contains(body, `asset-ref="sha256-hero"`) {
		t.Fatalf("sample photo should be replaced:\n%s", body)
	}
}

func TestAskUserTitlePickerStripsSamplePhotos(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	blue := `<ast-slide id="blue"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#0057D2" decorative="true"></ast-shape>` +
		`<ast-shape id="anvil" kind="rect" x="80" y="200" w="800" h="500" fill="#2B7FFF" decorative="true"></ast-shape>` +
		`<ast-image id="hero" x="960" y="0" w="960" h="1080" asset-ref="sha256-bike" fit="cover" decorative="true"></ast-image>` +
		`<ast-text id="ph-1" x="45" y="426" w="800" h="157" size="54" color="#FFFFFF">{{TITLE}}</ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "gco",
		Tokens: map[string]string{"surface": "#FFFFFF", "muted": "#89D1FF"},
		Assets: map[string]string{"sha256-bike": "data:image/png;base64,AAAA"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Title: "Blue cover, anvil and image", Tier: "fixed", Markup: blue, FillSlots: []string{"ph-1", "ph-pic-2"}, ThumbnailRef: "thumb/title"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := askUser(ctx, AskUserArgs{Kind: "select", Prompt: "Which cover?", SlidesTemplate: "gco", SlidesKind: "title"})
	if err != nil {
		t.Fatal(err)
	}
	var thumb *AskUserThumbnail
	for _, o := range res.Options {
		if o.ID == "title" {
			thumb = o.Thumbnail
		}
	}
	if thumb == nil {
		t.Fatalf("missing title thumbnail: %#v", res.Options)
	}
	if thumb.Kind != "slides-archetype" {
		t.Fatalf("title picker must live-render layout, not baked sample PNG, got %#v", thumb)
	}
	if strings.Contains(thumb.Markup, "sha256-bike") {
		t.Fatalf("sample photo must not appear on title layout tiles:\n%s", thumb.Markup)
	}
	if !strings.Contains(thumb.Markup, `fill="#0057D2"`) || !strings.Contains(thumb.Markup, `id="anvil"`) {
		t.Fatalf("layout chrome missing:\n%s", thumb.Markup)
	}
}

func TestAskUserImagePickerListsTitlePhotos(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	cover := `<ast-slide id="t"><ast-image id="hero" x="960" y="0" w="960" h="1080" asset-ref="sha256-bike" fit="cover"></ast-image>` +
		`<ast-text id="ph-1" x="40" y="40" w="400" h="80">{{TITLE}}</ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "gco",
		Tokens: map[string]string{"surface": "#FFFFFF"},
		Assets: map[string]string{
			"sha256-bike": "data:image/png;base64,AAAA",
			"sha256-logo": "data:image/png;base64,BBBB",
		},
		Archetypes: []themes.Archetype{
			{Kind: "title", Title: "Blue cover", Tier: "fixed", Markup: cover, FillSlots: []string{"ph-1"}},
			{Kind: "title-2", Title: "Blue cover copy", Tier: "fixed", Markup: cover, FillSlots: []string{"ph-1"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := askUser(ctx, AskUserArgs{Kind: "select", Prompt: "Which photo?", SlidesTemplate: "gco", SlidesImagePicker: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := optionLabels(res.Options)
	if !strings.Contains(strings.Join(ids, ","), "sha256-bike") {
		t.Fatalf("expected bike photo option, got %v", ids)
	}
	nPhotos := 0
	for _, o := range res.Options {
		if o.ID == "sha256-bike" {
			nPhotos++
			if o.Thumbnail == nil || o.Thumbnail.Kind != "image" || o.Thumbnail.AssetRef != "sha256-bike" {
				t.Fatalf("photo thumb: %#v", o.Thumbnail)
			}
		}
		if o.ID == "sha256-logo" {
			t.Fatal("logos must not be cover-photo options")
		}
	}
	if nPhotos != 1 {
		t.Fatalf("duplicate sample photo listed %d times: %v", nPhotos, ids)
	}
}

func TestAskUserImagePickerIncludesExampleSlidePhotos(t *testing.T) {
	backend := newMultiDeckStore()
	ctx := toolContext(t, backend)
	svc := Service{Store: backend}
	cover := `<ast-slide id="t"><ast-image id="hero" x="960" y="0" w="960" h="1080" asset-ref="sha256-aa" fit="cover"></ast-image>` +
		`<ast-text id="ph-1" x="40" y="40" w="400" h="80">{{TITLE}}</ast-text></ast-slide>`
	if err := svc.SaveTemplate(ctx, themes.Template{
		Name:   "gco",
		Tokens: map[string]string{"surface": "#FFFFFF"},
		Assets: map[string]string{
			"sha256-aa":     "data:image/png;base64,AAAA",
			"sha256-bb":     "data:image/png;base64,BBBB",
			"sha256-cc":     "data:image/png;base64,CCCC",
			"sha256-icon":   "data:image/png;base64,DDDD",
			"sha256-vector": "data:image/svg+xml;base64,PHN2Zz4=",
			"sha256-logo":   "data:image/png;base64,EEEE",
		},
		Archetypes: []themes.Archetype{
			{Kind: "title", Title: "Blue cover", Tier: "fixed", Markup: cover, FillSlots: []string{"ph-1"}},
		},
		Model: &themes.TemplateModel{
			Slides: []themes.IRLayout{
				{Name: "Slide 1", Objects: []themes.IRChrome{
					{Kind: "image", MediaKey: "sha256-bb", W: 979, H: 1080},
				}},
				{Name: "Slide 2", Objects: []themes.IRChrome{
					{Kind: "image", MediaKey: "sha256-cc", W: 686, H: 1080},
					{Kind: "image", MediaKey: "sha256-icon", W: 89, H: 89},
					{Kind: "image", MediaKey: "sha256-vector", W: 901, H: 442},
				}},
				{Name: "Slide 1 again", Objects: []themes.IRChrome{
					{Kind: "image", MediaKey: "sha256-bb", W: 979, H: 1080},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := askUser(ctx, AskUserArgs{Kind: "select", Prompt: "Which photo?", SlidesTemplate: "gco", SlidesImagePicker: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := optionLabels(res.Options)
	joined := strings.Join(ids, ",")
	for _, want := range []string{"sha256-bb", "sha256-cc", "sha256-aa"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, ids)
		}
	}
	for _, ban := range []string{"sha256-icon", "sha256-vector", "sha256-logo"} {
		if strings.Contains(joined, ban) {
			t.Fatalf("should omit %s: %v", ban, ids)
		}
	}
	nBB := 0
	for _, o := range res.Options {
		if o.ID == "sha256-bb" {
			nBB++
			if o.Label != "Slide 1" {
				t.Fatalf("example-slide label = %q", o.Label)
			}
		}
	}
	if nBB != 1 {
		t.Fatalf("duplicate example photo listed %d times: %v", nBB, ids)
	}
}
