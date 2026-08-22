package store

import (
	"context"
	"errors"
	"time"
)

var ErrDocsNotFound = errors.New("docs content not found")

// DeckManifest is the storage representation of an Astonish Slides deck.
type DeckManifest struct {
	ID            string            `json:"id"`
	Slug          string            `json:"slug"`
	Title         string            `json:"title"`
	Description   string            `json:"description,omitempty"`
	SchemaVersion int               `json:"schemaVersion"`
	Theme         map[string]string `json:"theme,omitempty"`
	Assets        map[string]string `json:"assets,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

// SlideContent is one ordered, canonical ASD slide fragment.
type SlideContent struct {
	ID            string    `json:"id"`
	DeckID        string    `json:"deckId"`
	Position      int       `json:"position"`
	Title         string    `json:"title,omitempty"`
	Content       string    `json:"content"`
	Notes         string    `json:"notes,omitempty"`
	SchemaVersion int       `json:"schemaVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// DocsStore persists decks and their ordered slides inside one already-resolved
// personal or team scope. It deliberately contains no tenant identifiers.
type DocsStore interface {
	CreateDeck(context.Context, *DeckManifest) error
	GetDeck(context.Context, string) (*DeckManifest, error)
	ListDecks(context.Context) ([]*DeckManifest, error)
	UpdateDeck(context.Context, *DeckManifest) error
	DeleteDeck(context.Context, string) error

	UpsertSlide(context.Context, *SlideContent) error
	GetSlide(context.Context, string, string) (*SlideContent, error)
	ListSlides(context.Context, string) ([]*SlideContent, error)
	DeleteSlide(context.Context, string, string) error
	ReorderSlides(context.Context, string, []string) error
}
