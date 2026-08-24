package slides

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/store"
)

type memoryDocsStore struct {
	deck   *store.DeckManifest
	slides []*store.SlideContent
}

func (m *memoryDocsStore) CreateDeck(_ context.Context, deck *store.DeckManifest) error {
	copy := *deck
	m.deck = &copy
	return nil
}
func (m *memoryDocsStore) GetDeck(_ context.Context, slug string) (*store.DeckManifest, error) {
	if m.deck == nil || m.deck.Slug != slug {
		return nil, store.ErrDocsNotFound
	}
	copy := *m.deck
	return &copy, nil
}
func (m *memoryDocsStore) ListDecks(context.Context) ([]*store.DeckManifest, error) {
	if m.deck == nil {
		return nil, nil
	}
	return []*store.DeckManifest{m.deck}, nil
}
func (m *memoryDocsStore) UpdateDeck(_ context.Context, deck *store.DeckManifest) error {
	m.deck = deck
	return nil
}
func (m *memoryDocsStore) DeleteDeck(context.Context, string) error { return nil }
func (m *memoryDocsStore) UpsertSlide(_ context.Context, slide *store.SlideContent) error {
	for i, existing := range m.slides {
		if existing.ID == slide.ID {
			copy := *slide
			m.slides[i] = &copy
			return nil
		}
	}
	copy := *slide
	m.slides = append(m.slides, &copy)
	return nil
}
func (m *memoryDocsStore) GetSlide(_ context.Context, deckID, slideID string) (*store.SlideContent, error) {
	for _, slide := range m.slides {
		if slide.DeckID == deckID && slide.ID == slideID {
			return slide, nil
		}
	}
	return nil, store.ErrDocsNotFound
}
func (m *memoryDocsStore) ListSlides(_ context.Context, deckID string) ([]*store.SlideContent, error) {
	var out []*store.SlideContent
	for _, slide := range m.slides {
		if slide.DeckID == deckID {
			copy := *slide
			out = append(out, &copy)
		}
	}
	return out, nil
}
func (m *memoryDocsStore) DeleteSlide(context.Context, string, string) error     { return nil }
func (m *memoryDocsStore) ReorderSlides(context.Context, string, []string) error { return nil }

func TestServiceWriteSlideReplacesPosition(t *testing.T) {
	ctx := context.Background()
	backend := &memoryDocsStore{}
	svc := Service{Store: backend}
	deck, err := svc.CreateDeck(ctx, "quarterly", "Quarterly", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	first, diagnostics, err := svc.WriteSlide(ctx, deck.Slug, 0, `<ast-slide id="human-readable"><ast-text x="0" y="0" w="100" h="100">First</ast-text></ast-slide>`, "notes one")
	if err != nil || HasErrors(diagnostics) {
		t.Fatalf("first write: diagnostics=%#v err=%v", diagnostics, err)
	}
	second, diagnostics, err := svc.WriteSlide(ctx, deck.Slug, 0, `<ast-slide id="another-label"><ast-text x="0" y="0" w="100" h="100">Second</ast-text></ast-slide>`, "notes two")
	if err != nil || HasErrors(diagnostics) {
		t.Fatalf("second write: diagnostics=%#v err=%v", diagnostics, err)
	}
	if first.ID != second.ID {
		t.Fatalf("replacement changed storage identity: %s != %s", first.ID, second.ID)
	}
	if len(backend.slides) != 1 || backend.slides[0].Notes != "notes two" {
		t.Fatalf("position was not replaced: %#v", backend.slides)
	}
}

func TestServiceFailsClosedWithoutStore(t *testing.T) {
	svc := Service{}
	if _, err := svc.CreateDeck(context.Background(), "slug", "title", "", nil); err == nil {
		t.Fatal("expected unavailable store error")
	}
	if _, _, err := svc.Deck(context.Background(), "slug"); err == nil {
		t.Fatal("expected unavailable store error")
	}
}

func TestServicePreservesStoreNotFound(t *testing.T) {
	_, _, err := (Service{Store: &memoryDocsStore{}}).Deck(context.Background(), "missing")
	if !errors.Is(err, store.ErrDocsNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceCopyDeckTo(t *testing.T) {
	ctx := context.Background()
	srcBackend := &memoryDocsStore{}
	src := Service{Store: srcBackend}
	deck, err := src.CreateDeck(ctx, "quarterly", "Quarterly", "desc", map[string]string{"surface": "#0B1020"})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := src.WriteSlide(ctx, deck.Slug, 0, `<ast-slide id="intro"><ast-text x="0" y="0" w="100" h="100">First</ast-text></ast-slide>`, "notes one"); err != nil || HasErrors(diags) {
		t.Fatalf("write slide 0: diags=%#v err=%v", diags, err)
	}
	if _, diags, err := src.WriteSlide(ctx, deck.Slug, 1, `<ast-slide id="second"><ast-text x="0" y="0" w="100" h="100">Second</ast-text></ast-slide>`, "notes two"); err != nil || HasErrors(diags) {
		t.Fatalf("write slide 1: diags=%#v err=%v", diags, err)
	}

	dstBackend := &memoryDocsStore{}
	dst := Service{Store: dstBackend}
	newDeck, err := src.CopyDeckTo(ctx, dst, deck.Slug)
	if err != nil {
		t.Fatalf("copy deck: %v", err)
	}
	if newDeck.Slug != deck.Slug || newDeck.Title != deck.Title {
		t.Fatalf("copied deck manifest mismatch: %#v", newDeck)
	}
	if newDeck.ID == deck.ID {
		t.Fatalf("copied deck should be re-keyed, got same id %s", newDeck.ID)
	}
	if newDeck.Theme["surface"] != "#0B1020" {
		t.Fatalf("theme not copied: %#v", newDeck.Theme)
	}

	_, dstSlides, err := dst.Deck(ctx, newDeck.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(dstSlides) != 2 {
		t.Fatalf("expected 2 slides copied, got %d", len(dstSlides))
	}
	_, srcSlides, _ := src.Deck(ctx, deck.Slug)
	byPos := map[int]*store.SlideContent{}
	for _, s := range dstSlides {
		byPos[s.Position] = s
	}
	for _, orig := range srcSlides {
		got, ok := byPos[orig.Position]
		if !ok {
			t.Fatalf("missing copied slide at position %d", orig.Position)
		}
		if got.Content != orig.Content || got.Notes != orig.Notes {
			t.Fatalf("slide %d content/notes mismatch: %#v vs %#v", orig.Position, got, orig)
		}
		if got.ID == orig.ID {
			t.Fatalf("slide %d should be re-keyed, got same id %s", orig.Position, got.ID)
		}
	}
}

func TestServiceCopyDeckToFailsWithoutDestination(t *testing.T) {
	ctx := context.Background()
	src := Service{Store: &memoryDocsStore{}}
	if _, err := src.CreateDeck(ctx, "d", "D", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CopyDeckTo(ctx, Service{}, "d"); err == nil {
		t.Fatal("expected destination unavailable error")
	}
}

// multiDeckStore is a minimal DocsStore that holds any number of decks, which
// the single-deck memoryDocsStore cannot. It backs the template persistence
// tests where a template deck coexists with regular decks.
type multiDeckStore struct {
	decks  map[string]*store.DeckManifest // by slug
	slides map[string][]*store.SlideContent
}

func newMultiDeckStore() *multiDeckStore {
	return &multiDeckStore{decks: map[string]*store.DeckManifest{}, slides: map[string][]*store.SlideContent{}}
}

func (m *multiDeckStore) CreateDeck(_ context.Context, d *store.DeckManifest) error {
	cp := *d
	m.decks[d.Slug] = &cp
	return nil
}
func (m *multiDeckStore) GetDeck(_ context.Context, slug string) (*store.DeckManifest, error) {
	d, ok := m.decks[slug]
	if !ok {
		return nil, store.ErrDocsNotFound
	}
	cp := *d
	return &cp, nil
}
func (m *multiDeckStore) ListDecks(context.Context) ([]*store.DeckManifest, error) {
	out := make([]*store.DeckManifest, 0, len(m.decks))
	for _, d := range m.decks {
		cp := *d
		out = append(out, &cp)
	}
	return out, nil
}
func (m *multiDeckStore) UpdateDeck(_ context.Context, d *store.DeckManifest) error {
	cp := *d
	m.decks[d.Slug] = &cp
	return nil
}
func (m *multiDeckStore) DeleteDeck(_ context.Context, slug string) error {
	if d, ok := m.decks[slug]; ok {
		delete(m.slides, d.ID)
	}
	delete(m.decks, slug)
	return nil
}
func (m *multiDeckStore) UpsertSlide(_ context.Context, slide *store.SlideContent) error {
	list := m.slides[slide.DeckID]
	for i, existing := range list {
		if existing.ID == slide.ID {
			cp := *slide
			list[i] = &cp
			m.slides[slide.DeckID] = list
			return nil
		}
	}
	cp := *slide
	m.slides[slide.DeckID] = append(list, &cp)
	return nil
}
func (m *multiDeckStore) GetSlide(_ context.Context, deckID, slideID string) (*store.SlideContent, error) {
	for _, s := range m.slides[deckID] {
		if s.ID == slideID {
			cp := *s
			return &cp, nil
		}
	}
	return nil, store.ErrDocsNotFound
}
func (m *multiDeckStore) ListSlides(_ context.Context, deckID string) ([]*store.SlideContent, error) {
	var out []*store.SlideContent
	for _, s := range m.slides[deckID] {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}
func (m *multiDeckStore) DeleteSlide(context.Context, string, string) error     { return nil }
func (m *multiDeckStore) ReorderSlides(context.Context, string, []string) error { return nil }

func TestSaveAndListTemplatesRoundtrip(t *testing.T) {
	ctx := context.Background()
	svc := Service{Store: newMultiDeckStore()}
	tmpl := themes.Template{
		Schema:      SchemaV2,
		Name:        "acme",
		Label:       "Acme Brand",
		Description: "corporate template",
		Tokens:      map[string]string{"surface": "#101820", "ink": "#F2F2F2", "accent": "#FFB81C"},
		Assets:      map[string]string{"logo": "acme.png"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#F2F2F2" size="72">{{TITLE}}</ast-text></ast-slide>`},
			{Kind: "content", Markup: `<ast-slide id="c"><ast-text id="b" x="160" y="320" w="1600" h="600" color="#F2F2F2" size="36">{{BODY}}</ast-text></ast-slide>`},
		},
	}
	if err := svc.SaveTemplate(ctx, tmpl); err != nil {
		t.Fatalf("save template: %v", err)
	}
	// Idempotent overwrite must not duplicate.
	if err := svc.SaveTemplate(ctx, tmpl); err != nil {
		t.Fatalf("re-save template: %v", err)
	}

	got, err := svc.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 template, got %d: %#v", len(got), got)
	}
	rt := got[0]
	if rt.Name != "acme" || rt.Label != "Acme Brand" || rt.Description != "corporate template" {
		t.Fatalf("template metadata mismatch: %#v", rt)
	}
	if rt.Scope != "scope" {
		t.Fatalf("expected Scope=scope, got %q", rt.Scope)
	}
	if rt.Tokens["accent"] != "#FFB81C" || rt.Assets["logo"] != "acme.png" {
		t.Fatalf("tokens/assets not roundtripped: %#v tokens=%#v assets=%#v", rt, rt.Tokens, rt.Assets)
	}
	if len(rt.Archetypes) != 2 {
		t.Fatalf("expected 2 archetypes, got %d: %#v", len(rt.Archetypes), rt.Archetypes)
	}
	byKind := map[string]themes.Archetype{}
	for _, a := range rt.Archetypes {
		byKind[a.Kind] = a
	}
	for _, orig := range tmpl.Archetypes {
		got, ok := byKind[orig.Kind]
		if !ok {
			t.Fatalf("missing archetype kind %q", orig.Kind)
		}
		if got.Markup != orig.Markup {
			t.Fatalf("archetype %q markup mismatch:\n got=%s\nwant=%s", orig.Kind, got.Markup, orig.Markup)
		}
	}

	// resolveTemplate should find the scoped template by name.
	if _, ok := svc.resolveTemplate(ctx, "acme"); !ok {
		t.Fatal("resolveTemplate did not find scoped template")
	}
	// Built-ins still resolve.
	if _, ok := svc.resolveTemplate(ctx, "midnight"); !ok {
		t.Fatal("resolveTemplate did not find built-in template")
	}
}

func TestServiceTemplateFetchesScoped(t *testing.T) {
	ctx := context.Background()
	svc := Service{Store: newMultiDeckStore()}
	tmpl := themes.Template{
		Schema: SchemaV2,
		Name:   "acme",
		Label:  "Acme Brand",
		Tokens: map[string]string{"surface": "#101820", "ink": "#F2F2F2", "accent": "#FFB81C"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#F2F2F2" size="72">{{TITLE}}</ast-text></ast-slide>`},
		},
	}
	if err := svc.SaveTemplate(ctx, tmpl); err != nil {
		t.Fatalf("save template: %v", err)
	}

	got, found, err := svc.Template(ctx, "acme")
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	if !found {
		t.Fatal("expected to find scoped template 'acme'")
	}
	if got.Name != "acme" || got.Label != "Acme Brand" || got.Tokens["accent"] != "#FFB81C" {
		t.Fatalf("fetched template mismatch: %#v", got)
	}

	// Unknown scoped name -> found=false, no error.
	if _, found, err := svc.Template(ctx, "does-not-exist"); err != nil || found {
		t.Fatalf("expected not found for unknown template, got found=%v err=%v", found, err)
	}

	// A built-in name is NOT a scoped template: Template must not surface it.
	if _, found, err := svc.Template(ctx, "midnight"); err != nil || found {
		t.Fatalf("Template must not return built-ins, got found=%v err=%v", found, err)
	}

	// TemplateSlug returns the canonical hidden-deck slug.
	if slug := svc.TemplateSlug("acme"); slug != "tmpl/acme" {
		t.Fatalf("TemplateSlug = %q, want tmpl/acme", slug)
	}
}

func TestListDecksHidesTemplateDecks(t *testing.T) {
	ctx := context.Background()
	svc := Service{Store: newMultiDeckStore()}
	if _, err := svc.CreateDeck(ctx, "quarterly", "Quarterly", "", nil); err != nil {
		t.Fatal(err)
	}
	tmpl := themes.Template{
		Name:   "acme",
		Tokens: map[string]string{"surface": "#FFFFFF"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#172033" size="72">{{TITLE}}</ast-text></ast-slide>`},
		},
	}
	if err := svc.SaveTemplate(ctx, tmpl); err != nil {
		t.Fatalf("save template: %v", err)
	}
	decks, err := svc.ListDecks(ctx)
	if err != nil {
		t.Fatalf("list decks: %v", err)
	}
	for _, d := range decks {
		if d.Slug == "tmpl/acme" {
			t.Fatalf("ListDecks leaked template deck: %#v", d)
		}
	}
	found := false
	for _, d := range decks {
		if d.Slug == "quarterly" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListDecks dropped the regular deck; got %#v", decks)
	}
}
