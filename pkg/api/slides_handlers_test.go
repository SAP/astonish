package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
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
