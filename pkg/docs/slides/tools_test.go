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
	want := []string{"create_deck", "write_slide", "get_deck", "list_decks", "list_templates", "validate_deck", "list_deck_assets", "add_deck_image"}
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

	res, err := listDeckAssets(ctx, ListDeckAssetsArgs{DeckSlug: "gallery"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Assets) != 2 {
		t.Fatalf("expected 2 assets, got %d: %#v", len(res.Assets), res.Assets)
	}
	// Deterministic order (sorted by ref): sha256-logo < sha256-photo.
	if res.Assets[0].Ref != "sha256-logo" || res.Assets[0].Kind != "logo" || res.Assets[0].MIME != "image/svg+xml" {
		t.Fatalf("logo asset misclassified: %#v", res.Assets[0])
	}
	if res.Assets[1].Ref != "sha256-photo" || res.Assets[1].Kind != "image" || res.Assets[1].MIME != "image/png" {
		t.Fatalf("photo asset misclassified: %#v", res.Assets[1])
	}
	// The catalog must never leak the heavy data: URI bytes.
	blob, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "data:image") {
		t.Fatalf("list_deck_assets leaked a data: URI: %s", blob)
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
