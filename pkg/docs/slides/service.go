package slides

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// templatePrefix namespaces template decks so they can be persisted in the same
// scope as regular decks while staying hidden from ListDecks.
const templatePrefix = "tmpl/"

// ErrDeckExists is returned by CopyDeckTo when the destination scope already
// has a deck with the same slug (unique-slug constraint in the target store).
var ErrDeckExists = errors.New("deck already exists in destination scope")

type Service struct{ Store store.DocsStore }

func (s Service) CreateDeck(ctx context.Context, slug, title, description string, theme map[string]string) (*store.DeckManifest, error) {
	return s.CreateDeckWithAssets(ctx, slug, title, description, theme, nil)
}

// CreateDeckWithAssets mirrors CreateDeck but also persists an optional asset
// map (e.g. seeded from a template). SchemaVersion stays SchemaV1 for regular
// decks so existing CreateDeck behavior is unchanged.
func (s Service) CreateDeckWithAssets(ctx context.Context, slug, title, description string, theme, assets map[string]string) (*store.DeckManifest, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("docs store unavailable")
	}
	if slug == "" || title == "" {
		return nil, fmt.Errorf("slug and title are required")
	}
	d := &store.DeckManifest{ID: uuid.NewString(), Slug: slug, Title: title, Description: description, SchemaVersion: SchemaV1, Theme: theme, Assets: assets}
	if err := s.Store.CreateDeck(ctx, d); err != nil {
		return nil, fmt.Errorf("create deck: %w", err)
	}
	return d, nil
}
func (s Service) WriteSlide(ctx context.Context, deckSlug string, position int, markup, notes string) (*store.SlideContent, []Diagnostic, error) {
	if s.Store == nil {
		return nil, nil, fmt.Errorf("docs store unavailable")
	}
	deck, err := s.Store.GetDeck(ctx, deckSlug)
	if err != nil {
		return nil, nil, fmt.Errorf("get deck: %w", err)
	}
	parsed, diags, err := ParseSlide(markup)
	if err != nil {
		return nil, nil, err
	}
	if HasErrors(diags) {
		return nil, diags, fmt.Errorf("slide validation failed")
	}
	item := &store.SlideContent{ID: uuid.NewString(), DeckID: deck.ID, Position: position, Title: parsed.Title, Content: markup, Notes: notes, SchemaVersion: SchemaV1}
	existing, err := s.Store.ListSlides(ctx, deck.ID)
	if err != nil {
		return nil, diags, fmt.Errorf("list slides: %w", err)
	}
	for _, slide := range existing {
		if slide.Position == position {
			item.ID = slide.ID
			break
		}
	}
	if err := s.Store.UpsertSlide(ctx, item); err != nil {
		return nil, diags, fmt.Errorf("write slide: %w", err)
	}
	return item, diags, nil
}
func (s Service) Deck(ctx context.Context, slug string) (*store.DeckManifest, []*store.SlideContent, error) {
	if s.Store == nil {
		return nil, nil, fmt.Errorf("docs store unavailable")
	}
	d, err := s.Store.GetDeck(ctx, slug)
	if err != nil {
		return nil, nil, err
	}
	slides, err := s.Store.ListSlides(ctx, d.ID)
	return d, slides, err
}

func (s Service) ListDecks(ctx context.Context) ([]*store.DeckManifest, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("docs store unavailable")
	}
	decks, err := s.Store.ListDecks(ctx)
	if err != nil {
		return nil, err
	}
	out := decks[:0]
	for _, d := range decks {
		if strings.HasPrefix(d.Slug, templatePrefix) {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// ListDecksLite mirrors ListDecks (same template-deck filtering) but uses the
// store's field-projected read so the heavy Assets/TemplateModel fields are
// never loaded. Use this for list views; use ListDecks when those fields are
// needed.
func (s Service) ListDecksLite(ctx context.Context) ([]*store.DeckManifest, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("docs store unavailable")
	}
	decks, err := s.Store.ListDecksLite(ctx)
	if err != nil {
		return nil, err
	}
	out := decks[:0]
	for _, d := range decks {
		if strings.HasPrefix(d.Slug, templatePrefix) {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// SaveTemplate persists a Template as a hidden deck (slug "tmpl/<name>") in this
// service's scope. Persistence is idempotent: an existing template deck with the
// same slug is deleted and recreated. Archetype markup is stored directly via
// UpsertSlide (bypassing the WriteSlide validation gate) because the {{TITLE}}
// and {{BODY}} placeholders are inert text content and validity is guaranteed by
// TestBuiltinTemplateArchetypesAreValidASD.
func (s Service) SaveTemplate(ctx context.Context, tmpl themes.Template) error {
	if s.Store == nil {
		return fmt.Errorf("docs store unavailable")
	}
	if tmpl.Name == "" {
		return fmt.Errorf("template name is required")
	}
	slug := templatePrefix + tmpl.Name
	if existing, err := s.Store.GetDeck(ctx, slug); err == nil && existing != nil {
		if err := s.Store.DeleteDeck(ctx, slug); err != nil {
			return fmt.Errorf("replace template deck: %w", err)
		}
	} else if err != nil && !errors.Is(err, store.ErrDocsNotFound) {
		return fmt.Errorf("check template deck: %w", err)
	}
	title := tmpl.Label
	if title == "" {
		title = tmpl.Name
	}
	// IR-backed imported templates carry the lossless TemplateModel; persist it
	// verbatim as raw JSON and mark the deck SchemaV3 so it is distinguishable
	// from plain scoped templates (SchemaV2). Built-in/plain templates stay V2.
	schemaVersion := SchemaV2
	templateModelJSON := ""
	if tmpl.Model != nil {
		raw, err := json.Marshal(tmpl.Model)
		if err != nil {
			return fmt.Errorf("marshal template model: %w", err)
		}
		templateModelJSON = string(raw)
		schemaVersion = SchemaV3
	}
	deck := &store.DeckManifest{
		ID:            uuid.NewString(),
		Slug:          slug,
		Title:         title,
		Description:   tmpl.Description,
		SchemaVersion: schemaVersion,
		Theme:         tmpl.Tokens,
		Assets:        tmpl.Assets,
		TemplateModel: templateModelJSON,
	}
	if err := s.Store.CreateDeck(ctx, deck); err != nil {
		return fmt.Errorf("create template deck: %w", err)
	}
	for i, arch := range tmpl.Archetypes {
		slide := &store.SlideContent{
			ID:       uuid.NewString(),
			DeckID:   deck.ID,
			Position: i,
			Title:    arch.Kind,
			// Notes carries the human-readable variant label (arch.Title) so a
			// template offering several variants per role (title, title-2, ...)
			// can surface friendly names; Kind stays in SlideContent.Title.
			Notes:         arch.Title,
			Content:       arch.Markup,
			SchemaVersion: SchemaV2,
		}
		if err := s.Store.UpsertSlide(ctx, slide); err != nil {
			return fmt.Errorf("write template archetype %d: %w", i, err)
		}
	}
	return nil
}

// ListTemplates reconstructs the Templates persisted in this service's scope from
// their hidden "tmpl/" decks. Built-in templates are not included; use
// resolveTemplate to merge built-ins and scoped templates by name.
func (s Service) ListTemplates(ctx context.Context) ([]themes.Template, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("docs store unavailable")
	}
	decks, err := s.Store.ListDecks(ctx)
	if err != nil {
		return nil, err
	}
	var out []themes.Template
	for _, deck := range decks {
		if !strings.HasPrefix(deck.Slug, templatePrefix) {
			continue
		}
		slides, err := s.Store.ListSlides(ctx, deck.ID)
		if err != nil {
			return nil, fmt.Errorf("list template archetypes: %w", err)
		}
		archetypes := make([]themes.Archetype, 0, len(slides))
		for _, slide := range slides {
			archetypes = append(archetypes, themes.Archetype{Kind: slide.Title, Title: slide.Notes, Markup: slide.Content})
		}
		// Rehydrate the lossless IR when this is an IR-backed imported template.
		var model *themes.TemplateModel
		if deck.TemplateModel != "" {
			var m themes.TemplateModel
			if err := json.Unmarshal([]byte(deck.TemplateModel), &m); err == nil {
				model = &m
			}
		}
		out = append(out, themes.Template{
			Schema:      SchemaV2,
			Name:        strings.TrimPrefix(deck.Slug, templatePrefix),
			Label:       deck.Title,
			Description: deck.Description,
			Tokens:      deck.Theme,
			Assets:      deck.Assets,
			Archetypes:  archetypes,
			Scope:       "scope",
			Model:       model,
		})
	}
	return out, nil
}

// resolveTemplate looks up a template by name, preferring a built-in over a
// scoped template of the same name.
func (s Service) resolveTemplate(ctx context.Context, name string) (themes.Template, bool) {
	if t, ok := themes.LookupTemplate(name); ok {
		return t, true
	}
	scoped, err := s.ListTemplates(ctx)
	if err != nil {
		return themes.Template{}, false
	}
	for _, t := range scoped {
		if t.Name == name {
			return t, true
		}
	}
	return themes.Template{}, false
}

// Template returns a single SCOPED template by name (reconstructed from its
// hidden tmpl/<name> deck), or found=false when no scoped template with that
// name exists. It intentionally does NOT fall back to built-ins — callers that
// manage scoped templates (duplicate/recolor/delete) must distinguish a
// built-in (which is read-only) from a scoped template. Use themes.LookupTemplate
// for built-ins, or resolveTemplate to merge both.
func (s Service) Template(ctx context.Context, name string) (themes.Template, bool, error) {
	scoped, err := s.ListTemplates(ctx)
	if err != nil {
		return themes.Template{}, false, err
	}
	for _, t := range scoped {
		if t.Name == name {
			return t, true, nil
		}
	}
	return themes.Template{}, false, nil
}

// TemplateSlug returns the canonical store slug for a scoped template of the
// given name (the hidden tmpl/<name> deck). Exposed so HTTP handlers reference
// the same prefix the service uses instead of hardcoding the literal.
func (s Service) TemplateSlug(name string) string { return templatePrefix + name }

func (s Service) Slide(ctx context.Context, deckSlug string, position int) (*store.SlideContent, error) {
	_, deckSlides, err := s.Deck(ctx, deckSlug)
	if err != nil {
		return nil, err
	}
	for _, slide := range deckSlides {
		if slide.Position == position {
			return slide, nil
		}
	}
	return nil, store.ErrDocsNotFound
}

func (s Service) DeleteDeck(ctx context.Context, slug string) error {
	if s.Store == nil {
		return fmt.Errorf("docs store unavailable")
	}
	return s.Store.DeleteDeck(ctx, slug)
}

// CopyDeckTo duplicates a deck (manifest + all slides) from this service's
// scope into dst's scope. It mirrors the personal↔team copy that
// AppPublishToTeamHandler performs for apps, but is deck-aware because
// DocsStore splits a deck into a DeckManifest and N SlideContent rows.
//
// The destination deck and every slide get freshly minted UUIDs (CreateDeck
// and WriteSlide re-key), so the two scopes never share ids. Slides are
// written through the validated WriteSlide path, guaranteeing the promoted
// deck is valid ASD in the destination scope.
func (s Service) CopyDeckTo(ctx context.Context, dst Service, slug string) (*store.DeckManifest, error) {
	deck, srcSlides, err := s.Deck(ctx, slug)
	if err != nil {
		return nil, err
	}
	if dst.Store == nil {
		return nil, fmt.Errorf("destination docs store unavailable")
	}
	if existing, err := dst.Store.GetDeck(ctx, deck.Slug); err == nil && existing != nil {
		return nil, ErrDeckExists
	} else if err != nil && !errors.Is(err, store.ErrDocsNotFound) {
		return nil, fmt.Errorf("check destination deck: %w", err)
	}
	newDeck, err := dst.CreateDeck(ctx, deck.Slug, deck.Title, deck.Description, deck.Theme)
	if err != nil {
		return nil, fmt.Errorf("create destination deck: %w", err)
	}
	for _, srcSlide := range srcSlides {
		if _, _, err := dst.WriteSlide(ctx, newDeck.Slug, srcSlide.Position, srcSlide.Content, srcSlide.Notes); err != nil {
			return nil, fmt.Errorf("copy slide %d: %w", srcSlide.Position, err)
		}
	}
	return newDeck, nil
}

func (s Service) Scene(ctx context.Context, slug string) (SceneGraph, []Diagnostic, error) {
	deck, deckSlides, err := s.Deck(ctx, slug)
	if err != nil {
		return SceneGraph{}, nil, err
	}
	scene := SceneGraph{SchemaVersion: deck.SchemaVersion, Title: deck.Title, Theme: deck.Theme, Assets: deck.Assets}
	var diagnostics []Diagnostic
	for _, persisted := range deckSlides {
		slide, slideDiagnostics, err := ParseSlide(persisted.Content)
		if err != nil {
			return SceneGraph{}, diagnostics, fmt.Errorf("parse slide %d: %w", persisted.Position, err)
		}
		if persisted.Notes != "" {
			slide.Notes = persisted.Notes
		}
		scene.Slides = append(scene.Slides, slide)
		diagnostics = append(diagnostics, slideDiagnostics...)
	}
	return scene, diagnostics, nil
}
