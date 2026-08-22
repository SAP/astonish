package slides

import (
	"context"
	"errors"
	"testing"

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
