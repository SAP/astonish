package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// docsStoreStub is a minimal in-memory store.DocsStore for handler tests.
// Only ListDecks is exercised; the rest satisfy the interface.
type docsStoreStub struct {
	decks []*store.DeckManifest
}

func (s *docsStoreStub) CreateDeck(context.Context, *store.DeckManifest) error { return nil }
func (s *docsStoreStub) GetDeck(context.Context, string) (*store.DeckManifest, error) {
	return nil, store.ErrDocsNotFound
}
func (s *docsStoreStub) ListDecks(context.Context) ([]*store.DeckManifest, error) {
	out := make([]*store.DeckManifest, len(s.decks))
	for i, d := range s.decks {
		clone := *d
		out[i] = &clone
	}
	return out, nil
}
func (s *docsStoreStub) UpdateDeck(context.Context, *store.DeckManifest) error { return nil }
func (s *docsStoreStub) DeleteDeck(context.Context, string) error             { return nil }
func (s *docsStoreStub) UpsertSlide(context.Context, *store.SlideContent) error { return nil }
func (s *docsStoreStub) GetSlide(context.Context, string, string) (*store.SlideContent, error) {
	return nil, store.ErrDocsNotFound
}
func (s *docsStoreStub) ListSlides(context.Context, string) ([]*store.SlideContent, error) {
	return nil, nil
}
func (s *docsStoreStub) DeleteSlide(context.Context, string, string) error     { return nil }
func (s *docsStoreStub) ReorderSlides(context.Context, string, []string) error { return nil }

var _ store.DocsStore = (*docsStoreStub)(nil)

func TestListDocsHandlerMergesScopes(t *testing.T) {
	personal := &docsStoreStub{decks: []*store.DeckManifest{{Slug: "p1", Title: "Personal One"}}}
	team := &docsStoreStub{decks: []*store.DeckManifest{{Slug: "t1", Title: "Team One"}}}

	req := httptest.NewRequest(http.MethodGet, "/api/docs", nil)
	req = req.WithContext(store.WithServices(req.Context(), &store.Services{PersonalDocs: personal, Docs: team}))
	rec := httptest.NewRecorder()

	ListDocsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Type  string                `json:"type"`
		Decks []*store.DeckManifest `json:"decks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type != "slides" || len(resp.Decks) != 2 {
		t.Fatalf("unexpected response: %#v", resp)
	}
	scopes := map[string]string{}
	for _, d := range resp.Decks {
		scopes[d.Slug] = d.Scope
	}
	if scopes["p1"] != "personal" || scopes["t1"] != "team" {
		t.Fatalf("scope annotations wrong: %#v", scopes)
	}
}

func TestListDocsHandlerExplicitScopeIsSingle(t *testing.T) {
	personal := &docsStoreStub{decks: []*store.DeckManifest{{Slug: "p1", Title: "Personal One"}}}
	team := &docsStoreStub{decks: []*store.DeckManifest{{Slug: "t1", Title: "Team One"}}}

	req := httptest.NewRequest(http.MethodGet, "/api/docs?scope=team", nil)
	req = req.WithContext(store.WithServices(req.Context(), &store.Services{PersonalDocs: personal, Docs: team}))
	rec := httptest.NewRecorder()

	ListDocsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Decks []*store.DeckManifest `json:"decks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Decks) != 1 || resp.Decks[0].Slug != "t1" || resp.Decks[0].Scope != "team" {
		t.Fatalf("expected only team deck, got %#v", resp.Decks)
	}
}
