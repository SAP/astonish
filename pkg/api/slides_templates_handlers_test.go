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
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/store"
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
	if len(resp.Templates) < 3 {
		t.Fatalf("expected >=3 built-in templates, got %d", len(resp.Templates))
	}
	names := map[string]bool{}
	for _, tmpl := range resp.Templates {
		names[tmpl.Name] = true
	}
	if !names["midnight"] || !names["light-corporate"] {
		t.Fatalf("expected built-ins midnight and light-corporate, got %v", names)
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
	if len(resp.Templates) < 3 {
		t.Fatalf("expected >=3 built-in templates without service, got %d", len(resp.Templates))
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
	// A .pptx part exceeding the 25MB limit -> 413. Fill with a byte pattern
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
}
