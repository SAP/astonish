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
	// TemplateModel holds the lossless imported-template IR as raw JSON
	// (themes.TemplateModel marshaled). It is set only for high-fidelity
	// imported templates (SchemaVersion=3) and is empty for all other decks.
	// Stored as a raw string to avoid coupling the store package to the slides
	// IR types.
	TemplateModel string    `json:"templateModel,omitempty"`
	// ThumbnailReady is true when at least one slide has been baked to a
	// static PNG thumbnail. Used by the list DTO so the frontend can skip
	// issuing an image request for decks that have no thumbnails.
	ThumbnailReady bool      `json:"thumbnailReady,omitempty"`
	// SessionID links the deck to the chat session that created it. Empty
	// means the deck is saved/permanent; non-empty means session-scoped.
	SessionID   string `json:"sessionId,omitempty"`
	// Version is the current version number (bumps on override-save).
	Version     int    `json:"version"`
	// SourceSlug links an enhance-copy to the saved deck it was cloned from.
	SourceSlug  string `json:"sourceSlug,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	// Scope is a non-persisted, in-memory annotation ("personal" or "team")
	// set only by the list handler when merging scopes. There is no Deck
	// column for it; CreateDeck/UpdateDeck use explicit setters so this field
	// is ignored by writes.
	Scope string `json:"scope,omitempty"`
}

// SlideContent is one ordered, canonical ASD slide fragment.
type SlideContent struct {
	ID            string    `json:"id"`
	DeckID        string    `json:"deckId"`
	Position      int       `json:"position"`
	Title         string    `json:"title,omitempty"`
	Content       string    `json:"content"`
	Notes         string    `json:"notes,omitempty"`
	ThumbnailRef  string    `json:"thumbnailRef,omitempty"`
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
	// ListDecksLite returns the same decks as ListDecks but OMITS the two heavy
	// DeckManifest fields — Assets (base64 data: URIs) and TemplateModel (the
	// multi-MB imported-template IR). Implementations MUST use storage-level
	// field projection so the megabytes are never read/deserialized, not merely
	// nulled out after loading. Use this for list views (the Slides list) where
	// those fields are unused; use ListDecks when Assets/TemplateModel are
	// needed (e.g. ListTemplates rehydrating the IR).
	ListDecksLite(context.Context) ([]*DeckManifest, error)
	UpdateDeck(context.Context, *DeckManifest) error
	DeleteDeck(context.Context, string) error

	UpsertSlide(context.Context, *SlideContent) error
	GetSlide(context.Context, string, string) (*SlideContent, error)
	ListSlides(context.Context, string) ([]*SlideContent, error)
	DeleteSlide(context.Context, string, string) error
	ReorderSlides(context.Context, string, []string) error

	// DeleteDecksBySessionID removes all decks (and their slides) that belong
	// to the given session. Used for cascade cleanup when a session is deleted.
	DeleteDecksBySessionID(ctx context.Context, sessionID string) error

	// SaveDeckVersion archives a snapshot of a deck at a version number.
	// Implementations should prune versions exceeding 5 per deck.
	SaveDeckVersion(ctx context.Context, v *DeckVersionSnapshot) error
	// ListDeckVersions returns all archived versions for a deck, newest first.
	ListDeckVersions(ctx context.Context, deckSlug string) ([]*DeckVersionSnapshot, error)
	// GetDeckVersion retrieves a specific version snapshot.
	GetDeckVersion(ctx context.Context, deckSlug string, version int) (*DeckVersionSnapshot, error)
}

// DeckVersionSnapshot is a historical snapshot of a deck at a specific version.
type DeckVersionSnapshot struct {
	ID        string    `json:"id"`
	DeckSlug  string    `json:"deckSlug"`
	Version   int       `json:"version"`
	Title     string    `json:"title"`
	Snapshot  string    `json:"snapshot"` // JSON: {theme, assets, slides[]}
	CreatedAt time.Time `json:"createdAt"`
}
