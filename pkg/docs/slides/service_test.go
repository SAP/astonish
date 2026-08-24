package slides

import (
	"context"
	"encoding/json"
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
func (m *memoryDocsStore) ListDecksLite(context.Context) ([]*store.DeckManifest, error) {
	if m.deck == nil {
		return nil, nil
	}
	lite := *m.deck
	lite.Assets = nil
	lite.TemplateModel = ""
	return []*store.DeckManifest{&lite}, nil
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
func (m *multiDeckStore) ListDecksLite(context.Context) ([]*store.DeckManifest, error) {
	out := make([]*store.DeckManifest, 0, len(m.decks))
	for _, d := range m.decks {
		cp := *d
		cp.Assets = nil
		cp.TemplateModel = ""
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

// TestServiceListDecksLiteOmitsHeavyFields asserts the field-projection contract:
// ListDecksLite returns decks with Assets/TemplateModel cleared but identity and
// theme intact, while ListDecks still returns them fully populated. Both hide
// template ("tmpl/") decks.
func TestServiceListDecksLiteOmitsHeavyFields(t *testing.T) {
	ctx := context.Background()
	backend := newMultiDeckStore()
	svc := Service{Store: backend}

	heavy := &store.DeckManifest{
		ID:            "deck-1",
		Slug:          "q4-review",
		Title:         "Q4 Review",
		Description:   "desc",
		SchemaVersion: SchemaV3,
		Theme:         map[string]string{"surface": "#0B1020", "accent": "#FFB81C"},
		Assets:        map[string]string{"logo": "data:image/png;base64,AAAA"},
		TemplateModel: `{"schema":3,"layouts":[{"id":"x"}]}`,
	}
	if err := backend.CreateDeck(ctx, heavy); err != nil {
		t.Fatal(err)
	}
	// A hidden template deck must not appear in either listing.
	if err := backend.CreateDeck(ctx, &store.DeckManifest{ID: "tmpl-1", Slug: templatePrefix + "acme", Title: "Acme"}); err != nil {
		t.Fatal(err)
	}

	lite, err := svc.ListDecksLite(ctx)
	if err != nil {
		t.Fatalf("ListDecksLite: %v", err)
	}
	if len(lite) != 1 {
		t.Fatalf("expected 1 non-template deck, got %d", len(lite))
	}
	d := lite[0]
	if d.Slug != "q4-review" || d.Title != "Q4 Review" {
		t.Fatalf("identity not preserved in lite: %#v", d)
	}
	if d.Theme["accent"] != "#FFB81C" {
		t.Fatalf("theme not preserved in lite: %#v", d.Theme)
	}
	if len(d.Assets) != 0 {
		t.Fatalf("lite deck should omit assets, got %#v", d.Assets)
	}
	if d.TemplateModel != "" {
		t.Fatalf("lite deck should omit templateModel, got %q", d.TemplateModel)
	}

	full, err := svc.ListDecks(ctx)
	if err != nil {
		t.Fatalf("ListDecks: %v", err)
	}
	if len(full) != 1 {
		t.Fatalf("expected 1 non-template deck (full), got %d", len(full))
	}
	if len(full[0].Assets) != 1 || full[0].TemplateModel == "" {
		t.Fatalf("full deck must retain assets+templateModel: %#v", full[0])
	}
}

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
			// An imported sample slide surfaced as an example-* archetype: it
			// carries a human-readable label (Title) which must survive the
			// SaveTemplate (label -> SlideContent.Notes) / ListTemplates round-trip.
			{Kind: "example", Title: "GCID meeting title", Markup: `<ast-slide id="e"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#101820" decorative="true"></ast-shape><ast-text id="ph-title" x="160" y="420" w="1600" h="200" size="72" color="#F2F2F2" weight="bold"><ast-run>{{TITLE}}</ast-run></ast-text><ast-text id="ph-body" x="160" y="640" w="1600" h="100" size="32" color="#FFB81C"><ast-run>{{BODY}}</ast-run></ast-text></ast-slide>`},
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
	if len(rt.Archetypes) != 3 {
		t.Fatalf("expected 3 archetypes, got %d: %#v", len(rt.Archetypes), rt.Archetypes)
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
		if got.Title != orig.Title {
			t.Fatalf("archetype %q label mismatch: got=%q want=%q", orig.Kind, got.Title, orig.Title)
		}
	}
	// The example archetype's friendly label must survive the round-trip.
	if byKind["example"].Title != "GCID meeting title" {
		t.Fatalf("example archetype label not roundtripped: %q", byKind["example"].Title)
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

func TestSaveAndListTemplateWithModelRoundtrip(t *testing.T) {
	ctx := context.Background()
	svc := Service{Store: newMultiDeckStore()}
	tmpl := themes.Template{
		Schema: SchemaV2,
		Name:   "corp",
		Label:  "Corp Brand",
		Tokens: map[string]string{"surface": "#FFFFFF", "ink": "#172033"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#172033" size="72">{{TITLE}}</ast-text></ast-slide>`},
		},
		Model: &themes.TemplateModel{
			Schema: 3,
			Size:   themes.IRSize{W: 1920, H: 1080},
			Theme:  map[string]string{"surface": "#FFFFFF", "ink": "#172033"},
			Layouts: []themes.IRLayout{{
				ID:         "layout-1",
				Name:       "Title Slide",
				Background: themes.IRBackground{Kind: "solid", Color: "#FFFFFF"},
				Objects: []themes.IRChrome{{
					Kind: "rect", X: 0, Y: 480, W: 1920, H: 120,
					Fill: &themes.IRFill{Kind: "solid", Color: "#172033"},
				}},
				Placeholders: []themes.IRPlaceholder{{Name: "title-1", Type: "title", X: 160, Y: 200, W: 1600, H: 200}},
			}},
		},
	}
	if err := svc.SaveTemplate(ctx, tmpl); err != nil {
		t.Fatalf("save template: %v", err)
	}
	got, err := svc.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 template, got %d", len(got))
	}
	rt := got[0]
	if rt.Model == nil {
		t.Fatal("template Model was not persisted/rehydrated")
	}
	if rt.Model.Schema != 3 || len(rt.Model.Layouts) != 1 {
		t.Fatalf("Model round-trip mismatch: %#v", rt.Model)
	}
	l := rt.Model.Layouts[0]
	if l.ID != "layout-1" || l.Background.Kind != "solid" || l.Background.Color != "#FFFFFF" {
		t.Fatalf("layout background not preserved: %#v", l)
	}
	if len(l.Objects) != 1 || l.Objects[0].Fill == nil || l.Objects[0].Fill.Color != "#172033" {
		t.Fatalf("chrome object not preserved: %#v", l.Objects)
	}
	if len(l.Placeholders) != 1 || l.Placeholders[0].Type != "title" {
		t.Fatalf("placeholder not preserved: %#v", l.Placeholders)
	}
}

// TestWorkerTemplateJSONWithWarningsDecodes guards the worker<->Go contract for
// the template IR: the import worker must emit templateModel.warnings as an array
// of structured objects ({code,message,layout?}) — NOT a flat string array —
// because themes.TemplateModel.Warnings is []IRWarning. A real corporate .pptx
// routinely produces warnings (gradient-approximated-as-solid, EMF skipped, ...),
// so if the worker emitted strings here the import handler's unmarshal into
// themes.Template would 500 with "import worker returned invalid template".
// This is a pure-decode regression test (no node required).
func TestWorkerTemplateJSONWithWarningsDecodes(t *testing.T) {
	// Minimal worker-shaped template payload with a non-empty structured
	// warnings list, mirroring import_worker.mjs `ok(template)` output.
	payload := []byte(`{
		"schema": 2,
		"name": "imported-template",
		"label": "Imported Template",
		"tokens": {"surface": "#FFFFFF", "ink": "#172033"},
		"archetypes": [
			{"kind": "title", "title": "Title", "markup": "<ast-slide id=\"t\"></ast-slide>"}
		],
		"templateModel": {
			"schema": 3,
			"size": {"w": 1920, "h": 1080},
			"theme": {"surface": "#FFFFFF"},
			"layouts": [{"id": "layout-1", "name": "Title", "background": {"kind": "solid", "color": "#FFFFFF"}}],
			"warnings": [
				{"code": "import", "message": "Gradient approximated as solid in archetype (sp-3)"},
				{"code": "import", "message": "EMF media skipped (no raster sibling)", "layout": "Title"}
			]
		}
	}`)

	var tmpl themes.Template
	if err := json.Unmarshal(payload, &tmpl); err != nil {
		t.Fatalf("worker template JSON must decode into themes.Template, got: %v", err)
	}
	if tmpl.Model == nil {
		t.Fatal("expected templateModel to populate Template.Model")
	}
	if len(tmpl.Model.Warnings) != 2 {
		t.Fatalf("expected 2 structured warnings, got %d: %#v", len(tmpl.Model.Warnings), tmpl.Model.Warnings)
	}
	if tmpl.Model.Warnings[0].Message == "" || tmpl.Model.Warnings[0].Code != "import" {
		t.Fatalf("warning not decoded into IRWarning fields: %#v", tmpl.Model.Warnings[0])
	}
	if tmpl.Model.Warnings[1].Layout != "Title" {
		t.Fatalf("expected warning layout to round-trip, got %q", tmpl.Model.Warnings[1].Layout)
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
