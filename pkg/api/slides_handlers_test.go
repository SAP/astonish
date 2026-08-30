package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
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
func (s *docsStoreStub) ListDecksLite(context.Context) ([]*store.DeckManifest, error) {
	out := make([]*store.DeckManifest, len(s.decks))
	for i, d := range s.decks {
		clone := *d
		clone.Assets = nil
		clone.TemplateModel = ""
		out[i] = &clone
	}
	return out, nil
}
func (s *docsStoreStub) UpdateDeck(context.Context, *store.DeckManifest) error  { return nil }
func (s *docsStoreStub) DeleteDeck(context.Context, string) error               { return nil }
func (s *docsStoreStub) UpsertSlide(context.Context, *store.SlideContent) error { return nil }
func (s *docsStoreStub) GetSlide(context.Context, string, string) (*store.SlideContent, error) {
	return nil, store.ErrDocsNotFound
}
func (s *docsStoreStub) ListSlides(context.Context, string) ([]*store.SlideContent, error) {
	return nil, nil
}
func (s *docsStoreStub) DeleteSlide(context.Context, string, string) error     { return nil }
func (s *docsStoreStub) ReorderSlides(context.Context, string, []string) error { return nil }
func (s *docsStoreStub) DeleteDecksBySessionID(context.Context, string) error  { return nil }
func (s *docsStoreStub) SaveDeckVersion(context.Context, *store.DeckVersionSnapshot) error {
	return nil
}
func (s *docsStoreStub) ListDeckVersions(context.Context, string) ([]*store.DeckVersionSnapshot, error) {
	return nil, nil
}
func (s *docsStoreStub) GetDeckVersion(context.Context, string, int) (*store.DeckVersionSnapshot, error) {
	return nil, store.ErrDocsNotFound
}

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

// TestGetLocalPDFBrowserManagerIsNotSandboxed guards the slides PDF export fix:
// the dedicated local PDF browser manager must NEVER enable the container
// sandbox. Session-less exports (the slides deck export is triggered from the
// docs UI with no chat session) previously called GetPDFBrowserManager(""),
// which under sandbox mode tried to resolve a session container from an empty
// session ID and failed with "failed to launch browser: failed to resolve
// session container", surfacing as a 500. GetLocalPDFBrowserManager always uses
// a local headless Chrome, so it needs no session/container.
func TestGetLocalPDFBrowserManagerIsNotSandboxed(t *testing.T) {
	mgr := GetLocalPDFBrowserManager()
	if mgr == nil {
		t.Fatal("GetLocalPDFBrowserManager returned nil")
	}
	if mgr.SandboxEnabled {
		t.Fatal("local PDF browser manager must not enable the container sandbox; " +
			"a session-less slides PDF export cannot resolve a session container")
	}
	// The manager is memoized via sync.Once — a second call returns the same
	// instance and must still be non-sandboxed.
	if again := GetLocalPDFBrowserManager(); again != mgr {
		t.Fatal("expected GetLocalPDFBrowserManager to return the memoized instance")
	}
}

// slugStore is a docsStoreStub extended with a working GetDeck/ListSlides so the
// deck-detail handler (GetSlidesDeckHandler) can be exercised end-to-end.
type slugStore struct {
	docsStoreStub
	deck   *store.DeckManifest
	slides []*store.SlideContent
}

func (s *slugStore) GetDeck(_ context.Context, slug string) (*store.DeckManifest, error) {
	if s.deck != nil && s.deck.Slug == slug {
		clone := *s.deck
		return &clone, nil
	}
	return nil, store.ErrDocsNotFound
}
func (s *slugStore) ListSlides(context.Context, string) ([]*store.SlideContent, error) {
	return s.slides, nil
}

// TestSlidesResponsesOmitHeavyManifestFields is the perf regression guard: the
// Slides list (GET /api/docs) and the deck-detail (GET /api/docs/slides/{slug})
// responses must NOT carry the multi-megabyte imported-template IR
// (templateModel) or the base64 asset map (assets). No client consumer reads
// them, and serializing them is what made imported-template decks slow to list
// and hang on open. Assets still reach the renderer server-side via
// Service.Scene, so this is purely a response-shape trim.
func TestSlidesResponsesOmitHeavyManifestFields(t *testing.T) {
	heavyModel := `{"schema":3,"layouts":[{"id":"l1"}]}`
	heavyAssets := map[string]string{"sha256-abc": "data:image/png;base64,AAAA"}

	// --- list ---
	personal := &docsStoreStub{decks: []*store.DeckManifest{{
		Slug: "imported", Title: "Imported", SchemaVersion: slides.SchemaV3,
		TemplateModel: heavyModel, Assets: heavyAssets,
	}}}
	reqList := httptest.NewRequest(http.MethodGet, "/api/docs", nil)
	reqList = reqList.WithContext(store.WithServices(reqList.Context(), &store.Services{PersonalDocs: personal}))
	recList := httptest.NewRecorder()
	ListDocsHandler(recList, reqList)
	if recList.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", recList.Code, recList.Body.String())
	}
	listBody := recList.Body.String()
	if strings.Contains(listBody, "templateModel") {
		t.Fatalf("list response leaks templateModel: %s", listBody)
	}
	if strings.Contains(listBody, `"assets"`) || strings.Contains(listBody, "data:image/") {
		t.Fatalf("list response leaks assets: %s", listBody)
	}
	if !strings.Contains(listBody, "imported") {
		t.Fatalf("list response missing deck: %s", listBody)
	}

	// --- deck detail ---
	detail := &slugStore{deck: &store.DeckManifest{
		ID: "id1", Slug: "imported", Title: "Imported", SchemaVersion: slides.SchemaV3,
		TemplateModel: heavyModel, Assets: heavyAssets,
	}}
	reqGet := httptest.NewRequest(http.MethodGet, "/api/docs/slides/imported", nil)
	reqGet = mux.SetURLVars(reqGet, map[string]string{"deckSlug": "imported"})
	reqGet = reqGet.WithContext(store.WithServices(reqGet.Context(), &store.Services{PersonalDocs: detail}))
	recGet := httptest.NewRecorder()
	GetSlidesDeckHandler(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", recGet.Code, recGet.Body.String())
	}
	getBody := recGet.Body.String()
	if strings.Contains(getBody, "templateModel") {
		t.Fatalf("deck-detail response leaks templateModel: %s", getBody)
	}
	if strings.Contains(getBody, `"assets"`) || strings.Contains(getBody, "data:image/") {
		t.Fatalf("deck-detail response leaks assets: %s", getBody)
	}
	if !strings.Contains(getBody, "imported") {
		t.Fatalf("deck-detail response missing deck: %s", getBody)
	}
}

func seedSaveSourceDeck(t *testing.T, docs *memDocsStore, slug string) {
	t.Helper()
	ctx := context.Background()
	deck := &store.DeckManifest{
		ID: uuid.NewString(), Slug: slug, Title: "Session deck", SessionID: "session-1",
		SchemaVersion: slides.SchemaV2, Assets: map[string]string{},
	}
	if err := docs.CreateDeck(ctx, deck); err != nil {
		t.Fatal(err)
	}
	for position := range 2 {
		if err := docs.UpsertSlide(ctx, &store.SlideContent{
			ID: uuid.NewString(), DeckID: deck.ID, Position: position,
			Content:       `<ast-slide id="slide"><ast-text x="0" y="0" w="100" h="100">Hello</ast-text></ast-slide>`,
			SchemaVersion: slides.SchemaV2,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func saveSlidesDeckRequestForTest(t *testing.T, docs store.DocsStore, sourceSlug, targetSlug string) *http.Request {
	t.Helper()
	body, err := json.Marshal(saveSlidesDeckRequest{TargetSlug: targetSlug})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/docs/slides/"+sourceSlug+"/save?scope=personal", bytes.NewReader(body))
	req = mux.SetURLVars(req, map[string]string{"deckSlug": sourceSlug})
	return req.WithContext(store.WithServices(req.Context(), &store.Services{PersonalDocs: docs}))
}

func TestSaveSlidesDeckRequiresThumbnailsAndRollsBackNewDeck(t *testing.T) {
	docs := newMemDocsStore()
	seedSaveSourceDeck(t, docs, "session-deck")

	original := generateRequiredDeckThumbnailsFn
	generateRequiredDeckThumbnailsFn = func(context.Context, *http.Request, slides.Service, string) error {
		return errors.New("render failed")
	}
	t.Cleanup(func() { generateRequiredDeckThumbnailsFn = original })

	rec := httptest.NewRecorder()
	SaveSlidesDeckHandler(rec, saveSlidesDeckRequestForTest(t, docs, "session-deck", "saved-deck"))

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "generate thumbnails") {
		t.Fatalf("status = %d body=%s, want thumbnail failure", rec.Code, rec.Body.String())
	}
	if _, err := docs.GetDeck(context.Background(), "saved-deck"); !errors.Is(err, store.ErrDocsNotFound) {
		t.Fatalf("new deck was not rolled back: %v", err)
	}
}

func TestSaveSlidesDeckWaitsForPersistedThumbnails(t *testing.T) {
	docs := newMemDocsStore()
	seedSaveSourceDeck(t, docs, "session-deck")

	original := generateRequiredDeckThumbnailsFn
	generateRequiredDeckThumbnailsFn = func(ctx context.Context, _ *http.Request, svc slides.Service, slug string) error {
		deck, deckSlides, err := svc.Deck(ctx, slug)
		if err != nil {
			return err
		}
		if deck.Assets == nil {
			deck.Assets = map[string]string{}
		}
		for _, slide := range deckSlides {
			ref := fmt.Sprintf("slidethumb/v2/%d", slide.Position)
			deck.Assets[ref] = "data:image/png;base64,cG5n"
			slide.ThumbnailRef = ref
			if err := svc.Store.UpsertSlide(ctx, slide); err != nil {
				return err
			}
		}
		deck.ThumbnailReady = true
		return svc.Store.UpdateDeck(ctx, deck)
	}
	t.Cleanup(func() { generateRequiredDeckThumbnailsFn = original })

	rec := httptest.NewRecorder()
	SaveSlidesDeckHandler(rec, saveSlidesDeckRequestForTest(t, docs, "session-deck", "saved-deck"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var response slidesDeckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Slides) != 2 {
		t.Fatalf("response slides = %d, want 2", len(response.Slides))
	}
	for position, slide := range response.Slides {
		want := fmt.Sprintf("slidethumb/v2/%d", position)
		if slide.ThumbnailRef != want {
			t.Fatalf("slide %d thumbnailRef = %q, want %q", position, slide.ThumbnailRef, want)
		}
	}
	deck, err := docs.GetDeck(context.Background(), "saved-deck")
	if err != nil {
		t.Fatal(err)
	}
	if !deck.ThumbnailReady || len(deck.Assets) != 2 {
		t.Fatalf("persisted deck thumbnails incomplete: ready=%v assets=%d", deck.ThumbnailReady, len(deck.Assets))
	}
}

func TestSaveSlidesDeckThumbnailFailureRestoresOverwrittenDeck(t *testing.T) {
	docs := newMemDocsStore()
	seedSaveSourceDeck(t, docs, "session-deck")
	ctx := context.Background()
	oldDeck := &store.DeckManifest{
		ID: uuid.NewString(), Slug: "saved-deck", Title: "Original", SchemaVersion: slides.SchemaV2,
		Version: 4, ThumbnailReady: true, Assets: map[string]string{"old-thumb": "data:image/png;base64,b2xk"},
	}
	if err := docs.CreateDeck(ctx, oldDeck); err != nil {
		t.Fatal(err)
	}
	oldSlide := &store.SlideContent{
		ID: uuid.NewString(), DeckID: oldDeck.ID, Position: 0, Content: `<ast-slide id="old"></ast-slide>`,
		ThumbnailRef: "old-thumb", SchemaVersion: slides.SchemaV2,
	}
	if err := docs.UpsertSlide(ctx, oldSlide); err != nil {
		t.Fatal(err)
	}

	original := generateRequiredDeckThumbnailsFn
	generateRequiredDeckThumbnailsFn = func(context.Context, *http.Request, slides.Service, string) error {
		return errors.New("render failed")
	}
	t.Cleanup(func() { generateRequiredDeckThumbnailsFn = original })

	rec := httptest.NewRecorder()
	SaveSlidesDeckHandler(rec, saveSlidesDeckRequestForTest(t, docs, "session-deck", "saved-deck"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	deck, deckSlides, err := (slides.Service{Store: docs}).Deck(ctx, "saved-deck")
	if err != nil {
		t.Fatal(err)
	}
	if deck.Title != "Original" || deck.Version != 4 || !deck.ThumbnailReady || deck.Assets["old-thumb"] == "" {
		t.Fatalf("overwritten deck was not restored: %#v", deck)
	}
	if len(deckSlides) != 1 || deckSlides[0].Content != oldSlide.Content || deckSlides[0].ThumbnailRef != "old-thumb" {
		t.Fatalf("overwritten slides were not restored: %#v", deckSlides)
	}
}

// ---------------------------------------------------------------------------

// seedExportDeck writes a deck manifest (with optional assets) and one ASD slide
// into the store, returning the slug.
func seedExportDeck(t *testing.T, s *memDocsStore, slug string, assets map[string]string) string {
	t.Helper()
	deckID := uuid.NewString()
	if err := s.CreateDeck(context.Background(), &store.DeckManifest{
		ID: deckID, Slug: slug, Title: "Deck", SchemaVersion: slides.SchemaV2, Assets: assets,
	}); err != nil {
		t.Fatal(err)
	}
	markup := `<ast-slide id="s1">` +
		`<ast-text id="h" x="160" y="120" w="1600" h="140" size="72" weight="bold" color="#172033">Quarter Results</ast-text>` +
		`<ast-text id="b" x="160" y="320" w="1600" h="600" size="36" color="#172033">Revenue is up</ast-text>` +
		`</ast-slide>`
	if err := s.UpsertSlide(context.Background(), &store.SlideContent{
		ID: uuid.NewString(), DeckID: deckID, Position: 0, Content: markup, SchemaVersion: slides.SchemaV2,
	}); err != nil {
		t.Fatal(err)
	}
	return slug
}

func exportPPTXRequest(t *testing.T, personal store.DocsStore, slug string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/docs/slides/"+slug+"/export/pptx?scope=personal", nil)
	req = mux.SetURLVars(req, map[string]string{"deckSlug": slug})
	req = withDocsServices(req, personal, newMemDocsStore())
	rec := httptest.NewRecorder()
	ExportSlidesPPTXHandler(rec, req)
	return rec
}

// TestExportSlidesPPTXPlainDeckUsesPptxGenJS verifies a deck WITHOUT an origin
// asset still exports via the existing ASD->PptxGenJS path and returns a valid
// .pptx.
func TestExportSlidesPPTXPlainDeckUsesPptxGenJS(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping node-dependent export test")
	}
	repo := apiRepoRoot(t)
	workingDir := filepath.Join(repo, "web")
	for _, mod := range []string{"pptxgenjs", "jszip"} {
		if _, err := os.Stat(filepath.Join(workingDir, "node_modules", mod)); err != nil {
			t.Skipf("web/node_modules/%s missing", mod)
		}
	}
	personal := newMemDocsStore()
	slug := seedExportDeck(t, personal, "plain-deck", nil) // no origin asset

	rec := exportPPTXRequest(t, personal, slug)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if _, err := zip.NewReader(bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("plain-deck export output not a valid .pptx zip: %v", err)
	}
}

// deckThumbStore is a docsStoreStub that also serves a single deck (with Assets)
// and its slides, for the per-slide thumbnail endpoint tests.
type deckThumbStore struct {
	docsStoreStub
	deck   *store.DeckManifest
	slides []*store.SlideContent
}

func (s *deckThumbStore) GetDeck(_ context.Context, slug string) (*store.DeckManifest, error) {
	if s.deck == nil || s.deck.Slug != slug {
		return nil, store.ErrDocsNotFound
	}
	clone := *s.deck
	return &clone, nil
}
func (s *deckThumbStore) ListSlides(_ context.Context, deckID string) ([]*store.SlideContent, error) {
	out := make([]*store.SlideContent, 0, len(s.slides))
	for _, sc := range s.slides {
		if sc.DeckID == deckID {
			clone := *sc
			out = append(out, &clone)
		}
	}
	return out, nil
}

// tinyPNG is a minimal valid base64 payload (the handler decodes but does not
// validate PNG structure, so any base64 works).
const tinyPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAAAAAA6fptVAAAACklEQVR4nGNgAAAAAgAB"

func deckThumbRequest(t *testing.T, store2 store.DocsStore, slug, idx string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/"+slug+"/thumbnails/"+idx, nil)
	req = req.WithContext(store.WithServices(req.Context(), &store.Services{PersonalDocs: store2, Docs: store2}))
	req = mux.SetURLVars(req, map[string]string{"deckSlug": slug, "idx": idx})
	rec := httptest.NewRecorder()
	GetSlidesDeckSlideThumbnailHandler(rec, req)
	return rec
}

func TestGetSlidesDeckSlideThumbnail(t *testing.T) {
	deckID := uuid.NewString()
	backend := &deckThumbStore{
		deck: &store.DeckManifest{
			ID:     deckID,
			Slug:   "my-deck",
			Title:  "My Deck",
			Assets: map[string]string{"slidethumb/0": "data:image/png;base64," + tinyPNGB64},
		},
		slides: []*store.SlideContent{
			{ID: uuid.NewString(), DeckID: deckID, Position: 0, Content: "x", ThumbnailRef: "slidethumb/0"},
			{ID: uuid.NewString(), DeckID: deckID, Position: 1, Content: "y"}, // no thumbnail baked
		},
	}

	t.Run("served for a baked slide", func(t *testing.T) {
		rec := deckThumbRequest(t, backend, "my-deck", "0")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
			t.Fatalf("content-type = %q", ct)
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("missing immutable cache header: %q", rec.Header().Get("Cache-Control"))
		}
		if rec.Body.Len() == 0 {
			t.Fatal("empty PNG body")
		}
	})

	t.Run("404 for a slide with no baked thumbnail", func(t *testing.T) {
		rec := deckThumbRequest(t, backend, "my-deck", "1")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404", rec.Code)
		}
	})

	t.Run("404 for an unknown slide index", func(t *testing.T) {
		rec := deckThumbRequest(t, backend, "my-deck", "9")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404", rec.Code)
		}
	})

	t.Run("404 for an unknown deck", func(t *testing.T) {
		rec := deckThumbRequest(t, backend, "no-such-deck", "0")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404", rec.Code)
		}
	})
}

func TestPatchSlideMovesElement(t *testing.T) {
	personal := newMemDocsStore()
	svc := slides.Service{Store: personal}
	ctx := context.Background()
	deck, err := svc.CreateDeck(ctx, "edit-deck", "Edit", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	markup := `<ast-slide id="s0"><ast-text id="headline" x="160" y="380" w="400" h="80">Title</ast-text></ast-slide>`
	if _, diags, err := svc.WriteSlide(ctx, deck.Slug, 0, markup, ""); err != nil || slides.HasErrors(diags) {
		t.Fatalf("seed: diags=%#v err=%v", diags, err)
	}

	body := bytes.NewBufferString(`{"moves":[{"id":"headline","x":200,"y":400}]}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/docs/slides/edit-deck/slides/0?scope=personal", body)
	req.Header.Set("Content-Type", "application/json")
	req = mux.SetURLVars(req, map[string]string{"deckSlug": "edit-deck", "idx": "0"})
	req = withDocsServices(req, personal, newMemDocsStore())
	rec := httptest.NewRecorder()
	PatchSlideHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var item store.SlideContent
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(item.Content, `x="200"`) || !strings.Contains(item.Content, `y="400"`) {
		t.Fatalf("geometry not patched: %s", item.Content)
	}

	bad := httptest.NewRequest(http.MethodPatch, "/api/docs/slides/edit-deck/slides/0?scope=personal", bytes.NewBufferString(`{"moves":[{"id":"nope","x":1,"y":1}]}`))
	bad = mux.SetURLVars(bad, map[string]string{"deckSlug": "edit-deck", "idx": "0"})
	bad = withDocsServices(bad, personal, newMemDocsStore())
	badRec := httptest.NewRecorder()
	PatchSlideHandler(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("missing id status = %d, want 400", badRec.Code)
	}
	if got := strings.TrimSpace(badRec.Body.String()); got != "invalid slide edit" {
		t.Fatalf("body = %q, want safe edit error", got)
	}
}

func TestPatchSlideResizesImage(t *testing.T) {
	personal := newMemDocsStore()
	svc := slides.Service{Store: personal}
	ctx := context.Background()
	deck, err := svc.CreateDeck(ctx, "resize-deck", "Resize", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	markup := `<ast-slide id="s0"><ast-image id="photo" x="100" y="120" w="400" h="200" asset-ref="sha256-photo"></ast-image></ast-slide>`
	if _, diags, err := svc.WriteSlide(ctx, deck.Slug, 0, markup, ""); err != nil || slides.HasErrors(diags) {
		t.Fatalf("seed: diags=%#v err=%v", diags, err)
	}

	body := bytes.NewBufferString(`{"resizes":[{"id":"photo","x":100,"y":120,"w":600,"h":300}]}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/docs/slides/resize-deck/slides/0?scope=personal", body)
	req.Header.Set("Content-Type", "application/json")
	req = mux.SetURLVars(req, map[string]string{"deckSlug": "resize-deck", "idx": "0"})
	req = withDocsServices(req, personal, newMemDocsStore())
	rec := httptest.NewRecorder()
	PatchSlideHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var item store.SlideContent
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	for _, attr := range []string{`x="100"`, `y="120"`, `w="600"`, `h="300"`} {
		if !strings.Contains(item.Content, attr) {
			t.Fatalf("resized geometry missing %s: %s", attr, item.Content)
		}
	}
}

func TestPatchSlideAppliesTextAndDelete(t *testing.T) {
	personal := newMemDocsStore()
	svc := slides.Service{Store: personal}
	ctx := context.Background()
	deck, err := svc.CreateDeck(ctx, "edit-deck", "Edit", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	markup := `<ast-slide id="s0"><ast-text id="headline" x="160" y="380" w="400" h="80">Title</ast-text><ast-text id="dek" x="160" y="500" w="400" h="40">Sub</ast-text></ast-slide>`
	if _, diags, err := svc.WriteSlide(ctx, deck.Slug, 0, markup, ""); err != nil || slides.HasErrors(diags) {
		t.Fatalf("seed: diags=%#v err=%v", diags, err)
	}

	body := bytes.NewBufferString(`{"texts":[{"id":"headline","text":"Hello"}],"deletes":["dek"]}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/docs/slides/edit-deck/slides/0?scope=personal", body)
	req.Header.Set("Content-Type", "application/json")
	req = mux.SetURLVars(req, map[string]string{"deckSlug": "edit-deck", "idx": "0"})
	req = withDocsServices(req, personal, newMemDocsStore())
	rec := httptest.NewRecorder()
	PatchSlideHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var item store.SlideContent
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(item.Content, "Hello") {
		t.Fatalf("text not patched: %s", item.Content)
	}
	if strings.Contains(item.Content, `id="dek"`) {
		t.Fatalf("dek should be deleted: %s", item.Content)
	}
}
