package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// memDocsStore, newMemDocsStore live in docs_sharing_handlers_test.go and are
// reused here for a functional in-memory docs backend.

func withDocsServices(req *http.Request, personal, team store.DocsStore) *http.Request {
	return req.WithContext(store.WithServices(req.Context(), &store.Services{PersonalDocs: personal, Docs: team}))
}

func TestListSlidesTemplatesReturnsBuiltins(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/templates", nil)
	req = withDocsServices(req, newMemDocsStore(), newMemDocsStore())
	rec := httptest.NewRecorder()

	ListSlidesTemplatesHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Templates []struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Templates) < 2 {
		t.Fatalf("expected >=2 built-in templates, got %d", len(resp.Templates))
	}
	names := map[string]bool{}
	for _, tmpl := range resp.Templates {
		names[tmpl.Name] = true
	}
	if !names["classic"] || !names["modern"] {
		t.Fatalf("expected built-ins classic and modern, got %v", names)
	}
}

func TestListSlidesTemplatesReturnsBuiltinsWithoutService(t *testing.T) {
	// No services in context: built-ins must still come back with 200.
	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/templates", nil)
	rec := httptest.NewRecorder()

	ListSlidesTemplatesHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Templates []json.RawMessage `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Templates) < 2 {
		t.Fatalf("expected >=2 built-in templates without service, got %d", len(resp.Templates))
	}
}

func buildMultipartUpload(t *testing.T, fieldName, filename string, contentType string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	var (
		part io.Writer
		err  error
	)
	if contentType != "" {
		h := make(map[string][]string)
		h["Content-Disposition"] = []string{`form-data; name="` + fieldName + `"; filename="` + filename + `"`}
		h["Content-Type"] = []string{contentType}
		part, err = writer.CreatePart(h)
	} else {
		part, err = writer.CreateFormFile(fieldName, filename)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func TestImportSlidesTemplateBadUpload(t *testing.T) {
	// A non-.pptx filename with no matching content-type -> 400.
	body, contentType := buildMultipartUpload(t, "file", "notes.txt", "", []byte("hello"))
	req := httptest.NewRequest(http.MethodPost, "/api/docs/slides/import", body)
	req.Header.Set("Content-Type", contentType)
	req = withDocsServices(req, newMemDocsStore(), newMemDocsStore())
	rec := httptest.NewRecorder()

	ImportSlidesTemplateHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-pptx upload, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestImportSlidesTemplateEmptyUpload(t *testing.T) {
	body, contentType := buildMultipartUpload(t, "file", "deck.pptx", "", nil)
	req := httptest.NewRequest(http.MethodPost, "/api/docs/slides/import", body)
	req.Header.Set("Content-Type", contentType)
	req = withDocsServices(req, newMemDocsStore(), newMemDocsStore())
	rec := httptest.NewRecorder()

	ImportSlidesTemplateHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty upload, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestImportSlidesTemplateOversized(t *testing.T) {
	// A .pptx part exceeding the 75MB limit -> 413. Fill with a byte pattern
	// so the multipart body is deterministic and > maxImportPPTXBytes.
	oversized := bytes.Repeat([]byte("A"), maxImportPPTXBytes+1024)
	body, contentType := buildMultipartUpload(t, "file", "big.pptx", "", oversized)
	req := httptest.NewRequest(http.MethodPost, "/api/docs/slides/import", body)
	req.Header.Set("Content-Type", contentType)
	req = withDocsServices(req, newMemDocsStore(), newMemDocsStore())
	rec := httptest.NewRecorder()

	ImportSlidesTemplateHandler(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized upload, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSlugifyTemplateName(t *testing.T) {
	cases := map[string]string{
		"My Deck.pptx": "my-deck-pptx",
		"Hello_World":  "hello-world",
		"  spaced  ":   "spaced",
		"Q1/2024 Plan": "q1-2024-plan",
		"":             "",
		"----":         "",
	}
	for in, want := range cases {
		if got := slugifyTemplateName(in); got != want {
			t.Errorf("slugifyTemplateName(%q) = %q, want %q", in, got, want)
		}
	}
}

// apiRepoRoot resolves the repository root relative to this test file
// (…/pkg/api), matching the handler's own runtime.Caller computation.
func apiRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

// TestImportSlidesTemplateRoundTrip exercises the full node import path: it
// generates a real .pptx via the export worker, POSTs it to the import handler,
// asserts a 200 with the persisted identity, then confirms a subsequent
// templates listing includes the imported scope template. It skips cleanly when
// node or the required node_modules are absent.
func TestImportSlidesTemplateRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping node-dependent import test")
	}
	repo := apiRepoRoot(t)
	workingDir := filepath.Join(repo, "web")
	for _, mod := range []string{"jszip", "fast-xml-parser", "pptxgenjs"} {
		if _, err := os.Stat(filepath.Join(workingDir, "node_modules", mod)); err != nil {
			t.Skipf("web/node_modules/%s missing; skipping", mod)
		}
	}
	exportScript := filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs")

	scene := map[string]any{
		"schemaVersion": 2,
		"title":         "Import handler fixture",
		"theme":         map[string]any{"surface": "FFFFFF", "ink": "172033", "accent": "2563EB"},
		"slides": []any{
			map[string]any{
				"id": "s1",
				"nodes": []any{
					map[string]any{
						"id": "title", "type": "text",
						"geometry": map[string]any{"x": 160, "y": 80, "w": 1600, "h": 120},
						"runs":     []any{map[string]any{"text": "Title", "bold": true}},
					},
				},
			},
		},
	}
	sceneJSON, err := json.Marshal(scene)
	if err != nil {
		t.Fatal(err)
	}
	exportResp, err := (pptxworker.Runner{WorkingDir: workingDir, ScriptPath: exportScript, Timeout: 30 * time.Second}).
		Run(context.Background(), pptxworker.Request{ProtocolVersion: pptxworker.ProtocolVersion, Scene: sceneJSON})
	if err != nil {
		t.Fatalf("export worker failed generating fixture: %v", err)
	}
	pptxBytes, err := base64.StdEncoding.DecodeString(exportResp.PPTXBase64)
	if err != nil {
		t.Fatalf("decode fixture pptx: %v", err)
	}

	personal := newMemDocsStore()
	team := newMemDocsStore()

	body, contentType := buildMultipartUpload(t, "file", "Brand Deck.pptx", "", pptxBytes)
	req := httptest.NewRequest(http.MethodPost, "/api/docs/slides/import?scope=personal", body)
	req.Header.Set("Content-Type", contentType)
	req = withDocsServices(req, personal, team)
	rec := httptest.NewRecorder()

	ImportSlidesTemplateHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from import, got %d body=%s", rec.Code, rec.Body.String())
	}
	var importResp struct {
		Template struct {
			Name  string `json:"name"`
			Label string `json:"label"`
			Scope string `json:"scope"`
		} `json:"template"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &importResp); err != nil {
		t.Fatal(err)
	}
	if importResp.Template.Name != "brand-deck" {
		t.Fatalf("expected slugified name brand-deck, got %q", importResp.Template.Name)
	}
	if importResp.Template.Label != "Brand Deck" {
		t.Fatalf("expected label 'Brand Deck', got %q", importResp.Template.Label)
	}

	// A subsequent listing in the same (personal) scope must include it.
	listReq := httptest.NewRequest(http.MethodGet, "/api/docs/slides/templates?scope=personal", nil)
	listReq = withDocsServices(listReq, personal, team)
	listRec := httptest.NewRecorder()
	ListSlidesTemplatesHandler(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Templates []struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tmpl := range listResp.Templates {
		if tmpl.Name == "brand-deck" {
			found = true
		}
	}
	if !found {
		t.Fatalf("imported template brand-deck not found in listing: %+v", listResp.Templates)
	}

	// The import must persist a usable (lossy ASD) template deck, but it must
	// NOT persist the original uploaded .pptx bytes anymore (the automizer
	// fidelity path was removed). Assert the deck exists and carries no
	// origin-pptx asset.
	tmplDeck, err := personal.GetDeck(context.Background(), "tmpl/brand-deck")
	if err != nil {
		t.Fatalf("get persisted template deck: %v", err)
	}
	for k := range tmplDeck.Assets {
		if k == "origin-pptx" {
			t.Fatalf("import must not persist origin .pptx bytes; found asset key %q", k)
		}
	}
}

// seedScopedTemplate persists a scoped template into the given store via the
// slides service; used to set up delete/duplicate/recolor tests without the
// node import path.
func seedScopedTemplate(t *testing.T, backend store.DocsStore, name, label string) {
	t.Helper()
	tmpl := themes.Template{
		Schema: 2,
		Name:   name,
		Label:  label,
		Tokens: map[string]string{"surface": "#FFFFFF", "ink": "#172033", "accent": "#1E40AF"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"><ast-text id="h" x="160" y="380" w="1600" h="200" color="#172033" size="72">{{TITLE}}</ast-text></ast-slide>`},
		},
	}
	if err := (slides.Service{Store: backend}).SaveTemplate(context.Background(), tmpl); err != nil {
		t.Fatalf("seed scoped template %q: %v", name, err)
	}
}

// TestListSlidesTemplatesOmitsAssetsAndFlagsKind verifies the lightweight list
// DTO: it never ships the assets map (no "assets" key) and correctly reports
// scope for a scoped template vs a built-in.
func TestListSlidesTemplatesOmitsAssetsAndFlagsScope(t *testing.T) {
	personal := newMemDocsStore()
	seedScopedTemplate(t, personal, "corp", "Corp")

	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/templates?scope=personal", nil)
	req = withDocsServices(req, personal, newMemDocsStore())
	rec := httptest.NewRecorder()
	ListSlidesTemplatesHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "\"assets\"") {
		t.Fatalf("list response must not include an assets map: %s", rec.Body.String())
	}
	var resp struct {
		Templates []struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var corp, builtin bool
	for _, tmpl := range resp.Templates {
		if tmpl.Name == "corp" {
			corp = true
			if tmpl.Scope != "personal" {
				t.Fatalf("corp template scope wrong: %+v", tmpl)
			}
		}
		if tmpl.Name == "classic" {
			builtin = true
			if tmpl.Scope != "builtin" {
				t.Fatalf("built-in scope wrong: %+v", tmpl)
			}
		}
	}
	if !corp || !builtin {
		t.Fatalf("expected both corp (scoped) and classic (builtin); got %+v", resp.Templates)
	}
}

func TestListSlidesTemplatesIncludesCover(t *testing.T) {
	personal := newMemDocsStore()
	baked := themes.Template{
		Schema: 2,
		Name:   "brand",
		Label:  "Brand",
		Tokens: map[string]string{"surface": "#FFFFFF", "ink": "#111111", "accent": "#2563eb"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Title: "Blue cover", Markup: `<ast-slide id="t"></ast-slide>`, ThumbnailRef: "thumb/title"},
			{Kind: "content", Title: "Body", Markup: `<ast-slide id="c"></ast-slide>`},
		},
	}
	if err := (slides.Service{Store: personal}).SaveTemplate(context.Background(), baked); err != nil {
		t.Fatalf("seed baked template: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/templates?scope=personal", nil)
	req = withDocsServices(req, personal, newMemDocsStore())
	rec := httptest.NewRecorder()
	ListSlidesTemplatesHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Templates []struct {
			Name  string `json:"name"`
			Cover *struct {
				Kind         string `json:"kind"`
				ThumbnailRef string `json:"thumbnailRef"`
				Markup       string `json:"markup"`
			} `json:"cover"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	var sawBuiltin, sawBaked bool
	for _, tmpl := range resp.Templates {
		switch tmpl.Name {
		case "classic":
			sawBuiltin = true
			if tmpl.Cover == nil || tmpl.Cover.Kind != "title" {
				t.Fatalf("classic cover = %+v, want kind=title", tmpl.Cover)
			}
			if tmpl.Cover.ThumbnailRef != "" {
				t.Fatalf("built-in cover must not ship a thumbnailRef, got %q", tmpl.Cover.ThumbnailRef)
			}
			if !strings.Contains(tmpl.Cover.Markup, "<ast-slide") {
				t.Fatalf("built-in cover must ship live markup, got %q", tmpl.Cover.Markup)
			}
		case "brand":
			sawBaked = true
			if tmpl.Cover == nil || tmpl.Cover.Kind != "title" || tmpl.Cover.ThumbnailRef != "thumb/title" {
				t.Fatalf("brand cover = %+v, want kind=title thumbnailRef=thumb/title", tmpl.Cover)
			}
			if tmpl.Cover.Markup != "" {
				t.Fatalf("baked cover must omit markup, got %q", tmpl.Cover.Markup)
			}
		}
	}
	if !sawBuiltin || !sawBaked {
		t.Fatalf("expected classic + brand covers; got %+v", resp.Templates)
	}
}

func TestTemplateCoverDTOPrefersTitleVariant(t *testing.T) {
	tmpl := themes.Template{
		Archetypes: []themes.Archetype{
			{Kind: "section", Markup: "section"},
			{Kind: "title-2", Markup: "cover-2", ThumbnailRef: "thumb/title-2"},
			{Kind: "title", Markup: "cover"},
		},
	}
	cover := templateCoverDTO(tmpl)
	if cover == nil || cover.Kind != "title-2" || cover.ThumbnailRef != "thumb/title-2" {
		t.Fatalf("cover = %+v, want first title* (title-2) with thumbnailRef", cover)
	}
	if cover.Markup != "" {
		t.Fatalf("markup must be omitted when thumbnailRef is set, got %q", cover.Markup)
	}
}

func deleteTemplateRequest(t *testing.T, personal store.DocsStore, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/docs/slides/templates/"+name+"?scope=personal", nil)
	req = mux.SetURLVars(req, map[string]string{"name": name})
	req = withDocsServices(req, personal, newMemDocsStore())
	rec := httptest.NewRecorder()
	DeleteSlidesTemplateHandler(rec, req)
	return rec
}

func TestDeleteSlidesTemplateRejectsBuiltin(t *testing.T) {
	rec := deleteTemplateRequest(t, newMemDocsStore(), "midnight")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 deleting built-in, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteSlidesTemplateRemovesScoped(t *testing.T) {
	personal := newMemDocsStore()
	seedScopedTemplate(t, personal, "corp", "Corp")

	rec := deleteTemplateRequest(t, personal, "corp")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting scoped template, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := personal.GetDeck(context.Background(), "tmpl/corp"); err == nil {
		t.Fatal("template deck still present after delete")
	}
	// Idempotent: deleting again is still 204.
	if rec2 := deleteTemplateRequest(t, personal, "corp"); rec2.Code != http.StatusNoContent {
		t.Fatalf("expected idempotent 204, got %d", rec2.Code)
	}
}

func TestDeleteSlidesTemplateRejectsEmptyName(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/docs/slides/templates/", nil)
	req = mux.SetURLVars(req, map[string]string{"name": ""})
	req = withDocsServices(req, newMemDocsStore(), newMemDocsStore())
	rec := httptest.NewRecorder()
	DeleteSlidesTemplateHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty name, got %d", rec.Code)
	}
}

func duplicateTemplateReq(t *testing.T, personal store.DocsStore, name, bodyJSON string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if bodyJSON != "" {
		reader = strings.NewReader(bodyJSON)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/docs/slides/templates/"+name+"/duplicate?scope=personal", reader)
	req = mux.SetURLVars(req, map[string]string{"name": name})
	req = withDocsServices(req, personal, newMemDocsStore())
	rec := httptest.NewRecorder()
	DuplicateSlidesTemplateHandler(rec, req)
	return rec
}

func TestDuplicateSlidesTemplateFromBuiltin(t *testing.T) {
	personal := newMemDocsStore()
	rec := duplicateTemplateReq(t, personal, "midnight", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 duplicating built-in, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Template struct {
			Name  string `json:"name"`
			Label string `json:"label"`
		} `json:"template"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Template.Name != "midnight-copy" {
		t.Fatalf("expected midnight-copy, got %q", resp.Template.Name)
	}
	// The new scoped template must be persisted.
	if _, err := personal.GetDeck(context.Background(), "tmpl/midnight-copy"); err != nil {
		t.Fatalf("duplicated template not persisted: %v", err)
	}
}

func TestDuplicateSlidesTemplateUniquifies(t *testing.T) {
	personal := newMemDocsStore()
	seedScopedTemplate(t, personal, "corp", "Corp")
	// Pre-create corp-copy so the duplicate must uniquify to corp-copy-2.
	seedScopedTemplate(t, personal, "corp-copy", "Corp Copy")

	rec := duplicateTemplateReq(t, personal, "corp", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Template struct {
			Name string `json:"name"`
		} `json:"template"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Template.Name != "corp-copy-2" {
		t.Fatalf("expected uniquified corp-copy-2, got %q", resp.Template.Name)
	}
	// The uniquified duplicate must be persisted.
	if _, err := personal.GetDeck(context.Background(), "tmpl/corp-copy-2"); err != nil {
		t.Fatalf("duplicate not persisted: %v", err)
	}
}

func TestDuplicateSlidesTemplateNotFound(t *testing.T) {
	rec := duplicateTemplateReq(t, newMemDocsStore(), "nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown template, got %d", rec.Code)
	}
}

func recolorTemplateReq(t *testing.T, personal store.DocsStore, name, bodyJSON string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/docs/slides/templates/"+name+"/recolor?scope=personal", strings.NewReader(bodyJSON))
	req = mux.SetURLVars(req, map[string]string{"name": name})
	req = withDocsServices(req, personal, newMemDocsStore())
	rec := httptest.NewRecorder()
	RecolorSlidesTemplateHandler(rec, req)
	return rec
}

func TestRecolorSlidesTemplateRejectsBuiltin(t *testing.T) {
	rec := recolorTemplateReq(t, newMemDocsStore(), "midnight", `{"tokens":{"accent":"#ABCDEF"}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 recoloring built-in, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRecolorSlidesTemplateValidatesInput(t *testing.T) {
	personal := newMemDocsStore()
	seedScopedTemplate(t, personal, "corp", "Corp")

	// Bad hex.
	if rec := recolorTemplateReq(t, personal, "corp", `{"tokens":{"accent":"red"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad hex, got %d", rec.Code)
	}
	// Unknown key.
	if rec := recolorTemplateReq(t, personal, "corp", `{"tokens":{"border":"#ABCDEF"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown key, got %d", rec.Code)
	}
	// Empty tokens.
	if rec := recolorTemplateReq(t, personal, "corp", `{"tokens":{}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty tokens, got %d", rec.Code)
	}
}

func TestRecolorSlidesTemplateUpdatesTokens(t *testing.T) {
	personal := newMemDocsStore()
	seedScopedTemplate(t, personal, "corp", "Corp")

	rec := recolorTemplateReq(t, personal, "corp", `{"tokens":{"accent":"#FF8800","surface":"#101010"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp slidesTemplateListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Tokens["accent"] != "#FF8800" || resp.Tokens["surface"] != "#101010" {
		t.Fatalf("tokens not updated in response: %#v", resp.Tokens)
	}
	// ink was not provided; the overlay must preserve it.
	if resp.Tokens["ink"] != "#172033" {
		t.Fatalf("ink token should be preserved, got %q", resp.Tokens["ink"])
	}
	// Persisted deck must reflect the new palette.
	deck, err := personal.GetDeck(context.Background(), "tmpl/corp")
	if err != nil {
		t.Fatal(err)
	}
	if deck.Theme["accent"] != "#FF8800" {
		t.Fatalf("persisted theme not recolored: %#v", deck.Theme)
	}
}

func TestRecolorSlidesTemplateNotFound(t *testing.T) {
	rec := recolorTemplateReq(t, newMemDocsStore(), "nope", `{"tokens":{"accent":"#ABCDEF"}}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown scoped template, got %d", rec.Code)
	}
}

// thumbnailReq drives GetSlidesTemplateThumbnailHandler through mux.SetURLVars
// with the given docs store bound in context.
func thumbnailReq(t *testing.T, personal store.DocsStore, name, kind string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/templates/"+name+"/thumbnails/"+kind, nil)
	req = withDocsServices(req, personal, newMemDocsStore())
	req = mux.SetURLVars(req, map[string]string{"name": name, "kind": kind})
	rec := httptest.NewRecorder()
	GetSlidesTemplateThumbnailHandler(rec, req)
	return rec
}

// seedThumbnailTemplate persists a scoped template carrying a single archetype
// with a baked PNG thumbnail (a valid base64 data URI) in its Assets map.
func seedThumbnailTemplate(t *testing.T, backend store.DocsStore, name, kind string) []byte {
	t.Helper()
	// A tiny valid PNG (1x1); the handler only base64-decodes, it does not
	// validate PNG structure, so any decodable payload would work.
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}
	ref := "thumb/" + kind
	tmpl := themes.Template{
		Schema: 2,
		Name:   name,
		Label:  name,
		Tokens: map[string]string{"surface": "#FFFFFF", "ink": "#172033", "accent": "#1E40AF"},
		Assets: map[string]string{ref: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)},
		Archetypes: []themes.Archetype{
			{Kind: kind, Markup: `<ast-slide id="t"></ast-slide>`, ThumbnailRef: ref},
		},
	}
	if err := (slides.Service{Store: backend}).SaveTemplate(context.Background(), tmpl); err != nil {
		t.Fatalf("seed thumbnail template %q: %v", name, err)
	}
	return png
}

func TestGetSlidesTemplateThumbnailServesPNG(t *testing.T) {
	personal := newMemDocsStore()
	png := seedThumbnailTemplate(t, personal, "thumbtpl", "title")

	rec := thumbnailReq(t, personal, "thumbtpl", "title")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("Cache-Control = %q, want immutable", cc)
	}
	if et := rec.Header().Get("ETag"); et != `"thumb/title"` {
		t.Fatalf("ETag = %q, want %q", et, `"thumb/title"`)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("body bytes did not match seeded PNG; got %d bytes", rec.Body.Len())
	}
}

func TestGetSlidesTemplateThumbnailExactKindOnly(t *testing.T) {
	personal := newMemDocsStore()
	seedThumbnailTemplate(t, personal, "thumbtpl", "title")

	// title-2 is a different cover. Do not serve the title thumbnail.
	rec := thumbnailReq(t, personal, "thumbtpl", "title-2")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("title-2 must not fall back to title, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetSlidesTemplateThumbnailUnknownTemplate(t *testing.T) {
	rec := thumbnailReq(t, newMemDocsStore(), "no-such-template", "title")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown template, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetSlidesTemplateMediaServesPNG(t *testing.T) {
	personal := newMemDocsStore()
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}
	ref := "sha256-deadbeef"
	tmpl := themes.Template{
		Schema: 2,
		Name:   "phototpl",
		Assets: map[string]string{ref: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"></ast-slide>`},
		},
	}
	if err := (slides.Service{Store: personal}).SaveTemplate(context.Background(), tmpl); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/templates/phototpl/media/"+ref, nil)
	req = withDocsServices(req, personal, newMemDocsStore())
	req = mux.SetURLVars(req, map[string]string{"name": "phototpl", "ref": ref})
	rec := httptest.NewRecorder()
	GetSlidesTemplateMediaHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("body mismatch")
	}
}

func TestGetSlidesTemplateMediaServesBuiltinDeclaredFont(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/templates/modern/media/font:Manrope:400", nil)
	req = mux.SetURLVars(req, map[string]string{"name": "modern", "ref": "font:Manrope:400"})
	rec := httptest.NewRecorder()
	GetSlidesTemplateMediaHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("modern declared Manrope should serve, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "font/woff2" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Body.Len() < 100 {
		t.Fatal("empty font body")
	}
}

func TestGetSlidesTemplateMediaBuiltinWithoutDeclaration(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/templates/aurora/media/font:Manrope:400", nil)
	req = mux.SetURLVars(req, map[string]string{"name": "aurora", "ref": "font:Manrope:400"})
	rec := httptest.NewRecorder()
	GetSlidesTemplateMediaHandler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("undeclared aurora font should 404, got %d", rec.Code)
	}
}

func TestGetSlidesTemplateMediaServesFont(t *testing.T) {
	personal := newMemDocsStore()
	tmpl := themes.Template{
		Name:   "phototpl",
		Assets: map[string]string{"font:SAP:regular": "data:font/ttf;base64,AAAA"},
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: `<ast-slide id="t"></ast-slide>`},
		},
	}
	if err := (slides.Service{Store: personal}).SaveTemplate(context.Background(), tmpl); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/templates/phototpl/media/font:SAP:regular", nil)
	req = withDocsServices(req, personal, newMemDocsStore())
	req = mux.SetURLVars(req, map[string]string{"name": "phototpl", "ref": "font:SAP:regular"})
	rec := httptest.NewRecorder()
	GetSlidesTemplateMediaHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fonts should serve, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "font/ttf" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestGetSlidesTemplateThumbnailUnknownKind(t *testing.T) {
	personal := newMemDocsStore()
	seedThumbnailTemplate(t, personal, "thumbtpl", "title")

	rec := thumbnailReq(t, personal, "thumbtpl", "chart")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown kind, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetSlidesTemplateThumbnailBuiltinWithoutRef(t *testing.T) {
	// Built-in templates predate the thumbnail pipeline: their archetypes carry
	// no ThumbnailRef, so the endpoint must 404 for them.
	builtins := themes.ListTemplates()
	if len(builtins) == 0 {
		t.Skip("no built-in templates registered")
	}
	tmpl := builtins[0]
	if len(tmpl.Archetypes) == 0 {
		t.Skip("built-in template has no archetypes")
	}
	kind := tmpl.Archetypes[0].Kind

	rec := thumbnailReq(t, newMemDocsStore(), tmpl.Name, kind)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for built-in archetype without thumbnail, got %d body=%s", rec.Code, rec.Body.String())
	}
}
