package slides

import (
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type Service struct{ Store store.DocsStore }

func (s Service) CreateDeck(ctx context.Context, slug, title, description string, theme map[string]string) (*store.DeckManifest, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("docs store unavailable")
	}
	if slug == "" || title == "" {
		return nil, fmt.Errorf("slug and title are required")
	}
	d := &store.DeckManifest{ID: uuid.NewString(), Slug: slug, Title: title, Description: description, SchemaVersion: SchemaV1, Theme: theme}
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
