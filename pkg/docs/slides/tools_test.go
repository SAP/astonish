package slides

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/store"
)

func toolContext(t *testing.T, docs store.DocsStore) context.Context {
	t.Helper()
	return store.WithServices(context.Background(), &store.Services{PersonalDocs: docs})
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
	if got.SlideCount != 1 || len(got.Slides) != 1 || got.Slides[0].Title != "Overview" {
		t.Fatalf("unexpected deck result: %#v", got)
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
	want := []string{"create_deck", "write_slide", "get_deck", "list_decks", "list_templates", "validate_deck"}
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
	if len(created.Archetypes) == 0 {
		t.Fatalf("expected non-empty archetypes from template, got %#v", created.Archetypes)
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
	if len(created.Archetypes) != 1 {
		t.Fatalf("expected 1 archetype from scoped template, got %#v", created.Archetypes)
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
	for _, want := range []string{"light-corporate", "midnight", "aurora", "acme"} {
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
	for _, want := range []string{"light-corporate", "midnight", "aurora"} {
		if !seen[want] {
			t.Fatalf("built-in %q missing after import; got %#v", want, seen)
		}
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
