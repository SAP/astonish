package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/docs/slides/components"
	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/store"
	webassets "github.com/SAP/astonish/web"
	"github.com/gorilla/mux"
)

type slidesDeckResponse struct {
	Deck   *store.DeckManifest   `json:"deck"`
	Slides []*store.SlideContent `json:"slides"`
}

type slidesValidateRequest struct {
	Markup string `json:"markup"`
}

func docsService(r *http.Request) (slides.Service, error) {
	svc := store.FromRequest(r)
	if svc == nil {
		return slides.Service{}, fmt.Errorf("request services unavailable")
	}
	var docs store.DocsStore
	switch r.URL.Query().Get("scope") {
	case "", "personal":
		docs = svc.PersonalDocs
	case "team":
		docs = svc.Docs
	default:
		return slides.Service{}, fmt.Errorf("scope must be personal or team")
	}
	if docs == nil {
		return slides.Service{}, fmt.Errorf("docs store unavailable")
	}
	return slides.Service{Store: docs}, nil
}

func ListDocsHandler(w http.ResponseWriter, r *http.Request) {
	if docType := r.URL.Query().Get("type"); docType != "" && docType != "slides" {
		http.Error(w, "unsupported docs type", http.StatusBadRequest)
		return
	}

	// Back-compat: an explicit ?scope=personal|team lists only that scope
	// (existing single-scope callers/tools are unaffected). With no scope we
	// merge personal + team and annotate each deck, mirroring ListAppsHandler.
	if scope := r.URL.Query().Get("scope"); scope != "" {
		svc, ok := requireDocsService(w, r)
		if !ok {
			return
		}
		decks, err := svc.ListDecks(r.Context())
		if err != nil {
			writeSlidesError(w, err)
			return
		}
		for i := range decks {
			decks[i].Scope = scope
		}
		writeSlidesJSON(w, http.StatusOK, map[string]any{"type": "slides", "decks": decks})
		return
	}

	svc := store.FromRequest(r)
	if svc == nil {
		http.Error(w, "request services unavailable", http.StatusServiceUnavailable)
		return
	}

	merged := make([]*store.DeckManifest, 0)
	if svc.PersonalDocs != nil {
		decks, err := (slides.Service{Store: svc.PersonalDocs}).ListDecks(r.Context())
		if err != nil {
			slog.Warn("failed to list personal decks", "error", err)
		} else {
			for i := range decks {
				decks[i].Scope = "personal"
			}
			merged = append(merged, decks...)
		}
	}
	if svc.Docs != nil {
		decks, err := (slides.Service{Store: svc.Docs}).ListDecks(r.Context())
		if err != nil {
			slog.Warn("failed to list team decks", "error", err)
		} else {
			for i := range decks {
				decks[i].Scope = "team"
			}
			merged = append(merged, decks...)
		}
	}
	writeSlidesJSON(w, http.StatusOK, map[string]any{"type": "slides", "decks": merged})
}

func GetSlidesDeckHandler(w http.ResponseWriter, r *http.Request) {
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	deck, deckSlides, err := svc.Deck(r.Context(), mux.Vars(r)["deckSlug"])
	if err != nil {
		writeSlidesError(w, err)
		return
	}
	writeSlidesJSON(w, http.StatusOK, slidesDeckResponse{Deck: deck, Slides: deckSlides})
}

func GetSlideHandler(w http.ResponseWriter, r *http.Request) {
	position, err := strconv.Atoi(mux.Vars(r)["idx"])
	if err != nil || position < 0 {
		http.Error(w, "slide index must be a non-negative integer", http.StatusBadRequest)
		return
	}
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	item, err := svc.Slide(r.Context(), mux.Vars(r)["deckSlug"], position)
	if err != nil {
		writeSlidesError(w, err)
		return
	}
	writeSlidesJSON(w, http.StatusOK, item)
}

func ValidateSlidesHandler(w http.ResponseWriter, r *http.Request) {
	var request slidesValidateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil || request.Markup == "" {
		http.Error(w, "invalid validation request", http.StatusBadRequest)
		return
	}
	writeSlidesJSON(w, http.StatusOK, slides.ValidateMarkup(request.Markup))
}

func PresentSlidesHandler(w http.ResponseWriter, r *http.Request) {
	result, ok := exportSlidesHTML(w, r, false)
	if !ok {
		return
	}
	setSlidesDocumentHeaders(w, false)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Bytes)
}

func ExportSlidesHTMLHandler(w http.ResponseWriter, r *http.Request) {
	result, ok := exportSlidesHTML(w, r, false)
	if !ok {
		return
	}
	setSlidesDocumentHeaders(w, true)
	w.Header().Set("Content-Disposition", attachmentName(mux.Vars(r)["deckSlug"], "html"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Bytes)
}

func ExportSlidesPDFHandler(w http.ResponseWriter, r *http.Request) {
	scene, ok := loadSlidesScene(w, r)
	if !ok {
		return
	}
	result, err := (slides.PDFExporter{Browser: GetPDFBrowserManager(""), RuntimeJS: webassets.GetSlidesRuntime()}).Export(scene)
	if err != nil {
		http.Error(w, "export slides PDF", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", attachmentName(mux.Vars(r)["deckSlug"], "pdf"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Bytes)
}

func ExportSlidesPPTXHandler(w http.ResponseWriter, r *http.Request) {
	scene, ok := loadSlidesScene(w, r)
	if !ok {
		return
	}
	_, currentFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
	exporter := slides.PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repoRoot, "web"),
		ScriptPath: filepath.Join(repoRoot, "pkg/docs/slides/pptxworker/worker.mjs"),
	}}
	result, err := exporter.Export(r.Context(), scene, r.URL.Query().Get("strictNative") == "true")
	if err != nil {
		http.Error(w, "export slides PPTX", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	w.Header().Set("Content-Disposition", attachmentName(mux.Vars(r)["deckSlug"], "pptx"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Bytes)
}

func DeleteSlidesDeckHandler(w http.ResponseWriter, r *http.Request) {
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteDeck(r.Context(), mux.Vars(r)["deckSlug"]); err != nil {
		writeSlidesError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func ListSlidesThemesHandler(w http.ResponseWriter, _ *http.Request) {
	theme, err := themes.Lookup("light-corporate")
	if err != nil {
		http.Error(w, "slides themes unavailable", http.StatusInternalServerError)
		return
	}
	writeSlidesJSON(w, http.StatusOK, map[string]any{"themes": []themes.Theme{theme}})
}

func ListSlidesComponentsHandler(w http.ResponseWriter, _ *http.Request) {
	definitions := make([]components.Definition, 0)
	for _, tag := range components.TagsV1() {
		definition, _ := components.LookupV1(tag)
		definitions = append(definitions, definition)
	}
	writeSlidesJSON(w, http.StatusOK, map[string]any{"schemaVersion": slides.SchemaV1, "components": definitions})
}

func requireDocsService(w http.ResponseWriter, r *http.Request) (slides.Service, bool) {
	svc, err := docsService(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return slides.Service{}, false
	}
	return svc, true
}

func loadSlidesScene(w http.ResponseWriter, r *http.Request) (slides.SceneGraph, bool) {
	svc, ok := requireDocsService(w, r)
	if !ok {
		return slides.SceneGraph{}, false
	}
	scene, _, err := svc.Scene(r.Context(), mux.Vars(r)["deckSlug"])
	if err != nil {
		writeSlidesError(w, err)
		return slides.SceneGraph{}, false
	}
	return scene, true
}

func exportSlidesHTML(w http.ResponseWriter, r *http.Request, print bool) (slides.ExportResult, bool) {
	scene, ok := loadSlidesScene(w, r)
	if !ok {
		return slides.ExportResult{}, false
	}
	result, err := (slides.HTMLExporter{RuntimeJS: webassets.GetSlidesRuntime(), Print: print}).Export(scene)
	if err != nil {
		http.Error(w, "render slides", http.StatusInternalServerError)
		return slides.ExportResult{}, false
	}
	return result, true
}

func setSlidesDocumentHeaders(w http.ResponseWriter, download bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !download {
		w.Header().Set("Content-Disposition", "inline")
	}
}

func attachmentName(slug, extension string) string {
	return fmt.Sprintf("attachment; filename=%q", slug+"."+extension)
}

func writeSlidesJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeSlidesError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrDocsNotFound) {
		http.Error(w, "slides content not found", http.StatusNotFound)
		return
	}
	http.Error(w, "slides request failed", http.StatusInternalServerError)
}
