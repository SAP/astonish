package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// memDocsStore is a functional in-memory store.DocsStore supporting the
// deck+slides create/get/list/upsert path CopyDeckTo needs.
type memDocsStore struct {
	decks  map[string]*store.DeckManifest   // by slug
	slides map[string][]*store.SlideContent // by deck ID
}

func newMemDocsStore() *memDocsStore {
	return &memDocsStore{decks: map[string]*store.DeckManifest{}, slides: map[string][]*store.SlideContent{}}
}

func (m *memDocsStore) CreateDeck(_ context.Context, d *store.DeckManifest) error {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	clone := *d
	m.decks[d.Slug] = &clone
	return nil
}
func (m *memDocsStore) GetDeck(_ context.Context, slug string) (*store.DeckManifest, error) {
	d, ok := m.decks[slug]
	if !ok {
		return nil, store.ErrDocsNotFound
	}
	clone := *d
	return &clone, nil
}
func (m *memDocsStore) ListDecks(context.Context) ([]*store.DeckManifest, error) {
	out := make([]*store.DeckManifest, 0, len(m.decks))
	for _, d := range m.decks {
		clone := *d
		out = append(out, &clone)
	}
	return out, nil
}

// ListDecksLite mirrors ListDecks but nils out the heavy fields (in-memory cost
// is irrelevant here; this satisfies the DocsStore interface and mirrors the
// Ent stores' field-projected behavior for tests).
func (m *memDocsStore) ListDecksLite(ctx context.Context) ([]*store.DeckManifest, error) {
	decks, err := m.ListDecks(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range decks {
		d.Assets = nil
		d.TemplateModel = ""
	}
	return decks, nil
}
func (m *memDocsStore) UpdateDeck(_ context.Context, d *store.DeckManifest) error {
	clone := *d
	m.decks[d.Slug] = &clone
	return nil
}
func (m *memDocsStore) DeleteDeck(_ context.Context, slug string) error {
	if deck := m.decks[slug]; deck != nil {
		delete(m.slides, deck.ID)
	}
	delete(m.decks, slug)
	return nil
}
func (m *memDocsStore) UpsertSlide(_ context.Context, s *store.SlideContent) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	list := m.slides[s.DeckID]
	for i, existing := range list {
		if existing.ID == s.ID || existing.Position == s.Position {
			clone := *s
			list[i] = &clone
			m.slides[s.DeckID] = list
			return nil
		}
	}
	clone := *s
	m.slides[s.DeckID] = append(list, &clone)
	return nil
}
func (m *memDocsStore) GetSlide(_ context.Context, deckID, slideID string) (*store.SlideContent, error) {
	for _, s := range m.slides[deckID] {
		if s.ID == slideID {
			return s, nil
		}
	}
	return nil, store.ErrDocsNotFound
}
func (m *memDocsStore) ListSlides(_ context.Context, deckID string) ([]*store.SlideContent, error) {
	out := make([]*store.SlideContent, 0)
	for _, s := range m.slides[deckID] {
		clone := *s
		out = append(out, &clone)
	}
	return out, nil
}
func (m *memDocsStore) DeleteSlide(_ context.Context, deckID, slideID string) error {
	list := m.slides[deckID]
	for i, slide := range list {
		if slide.ID == slideID {
			m.slides[deckID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	return nil
}
func (m *memDocsStore) ReorderSlides(context.Context, string, []string) error { return nil }
func (m *memDocsStore) DeleteDecksBySessionID(_ context.Context, sessionID string) error {
	for slug, d := range m.decks {
		if d.SessionID == sessionID {
			delete(m.slides, d.ID)
			delete(m.decks, slug)
		}
	}
	return nil
}
func (m *memDocsStore) SaveDeckVersion(context.Context, *store.DeckVersionSnapshot) error {
	return nil
}
func (m *memDocsStore) ListDeckVersions(context.Context, string) ([]*store.DeckVersionSnapshot, error) {
	return nil, nil
}
func (m *memDocsStore) GetDeckVersion(context.Context, string, int) (*store.DeckVersionSnapshot, error) {
	return nil, store.ErrDocsNotFound
}

var _ store.DocsStore = (*memDocsStore)(nil)

// seedDeck writes a deck + one valid slide into the given store scope.
func seedDeck(t *testing.T, backend store.DocsStore, slug, title string) {
	t.Helper()
	svc := slides.Service{Store: backend}
	deck, err := svc.CreateDeck(context.Background(), slug, title, "desc", map[string]string{"surface": "#0B1020"})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := svc.WriteSlide(context.Background(), deck.Slug, 0,
		`<ast-slide id="intro"><ast-text x="0" y="0" w="100" h="100">Hello</ast-text></ast-slide>`, "note"); err != nil || slides.HasErrors(diags) {
		t.Fatalf("seed slide: diags=%#v err=%v", diags, err)
	}
}

func docsSharingRequest(t *testing.T, path string, body any, svc *store.Services, pu *PlatformUser) *http.Request {
	t.Helper()
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(bodyBytes))
	r.Header.Set("Content-Type", "application/json")
	ctx := store.WithServices(r.Context(), svc)
	if pu != nil {
		ctx = WithPlatformUser(ctx, pu)
	}
	return r.WithContext(ctx)
}

func TestSlidesPublishToTeam(t *testing.T) {
	personal := newMemDocsStore()
	team := newMemDocsStore()
	seedDeck(t, personal, "quarterly", "Quarterly")

	svc := &store.Services{Mode: store.ModePlatform, PersonalDocs: personal, Docs: team}
	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := docsSharingRequest(t, "/api/docs/slides/publish", DeckPublishRequest{Slug: "quarterly"}, svc, pu)
	w := httptest.NewRecorder()
	SlidesPublishToTeamHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["published"] != true || resp["scope"] != "team" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	// Team copy exists with re-keyed ids and identical slide content.
	teamSvc := slides.Service{Store: team}
	teamDeck, teamSlides, err := teamSvc.Deck(context.Background(), "quarterly")
	if err != nil {
		t.Fatalf("team deck missing: %v", err)
	}
	persDeck := personal.decks["quarterly"]
	if teamDeck.ID == persDeck.ID {
		t.Fatalf("team deck should be re-keyed, got same id %s", teamDeck.ID)
	}
	if len(teamSlides) != 1 || teamSlides[0].Content != personal.slides[persDeck.ID][0].Content {
		t.Fatalf("slide content not copied: %#v", teamSlides)
	}
	if teamSlides[0].ID == personal.slides[persDeck.ID][0].ID {
		t.Fatalf("slide should be re-keyed")
	}
}

func TestSlidesForkToPersonal(t *testing.T) {
	personal := newMemDocsStore()
	team := newMemDocsStore()
	seedDeck(t, team, "shared", "Shared")

	svc := &store.Services{Mode: store.ModePlatform, PersonalDocs: personal, Docs: team}
	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := docsSharingRequest(t, "/api/docs/slides/fork", DeckForkRequest{Slug: "shared", Source: "team"}, svc, pu)
	w := httptest.NewRecorder()
	SlidesForkToPersonalHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["forked"] != true || resp["scope"] != "personal" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if _, ok := personal.decks["shared"]; !ok {
		t.Fatalf("personal copy should exist after fork")
	}
}

func TestSlidesPublishMissingSlug(t *testing.T) {
	svc := &store.Services{Mode: store.ModePlatform, PersonalDocs: newMemDocsStore(), Docs: newMemDocsStore()}
	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := docsSharingRequest(t, "/api/docs/slides/publish", DeckPublishRequest{}, svc, pu)
	w := httptest.NewRecorder()
	SlidesPublishToTeamHandler(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing slug, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSlidesPublishUnknownSlug(t *testing.T) {
	svc := &store.Services{Mode: store.ModePlatform, PersonalDocs: newMemDocsStore(), Docs: newMemDocsStore()}
	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := docsSharingRequest(t, "/api/docs/slides/publish", DeckPublishRequest{Slug: "nope"}, svc, pu)
	w := httptest.NewRecorder()
	SlidesPublishToTeamHandler(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown slug, got %d: %s", w.Code, w.Body.String())
	}
}
