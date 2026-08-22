package slides

import (
	"context"
	"testing"

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
	want := []string{"create_deck", "write_slide", "get_deck", "list_decks", "validate_deck"}
	if len(got) != len(want) {
		t.Fatalf("got %d tools, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name() != name {
			t.Fatalf("tool %d = %q, want %q", i, got[i].Name(), name)
		}
	}
}
