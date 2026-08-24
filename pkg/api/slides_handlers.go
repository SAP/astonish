package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/docs/slides/components"
	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/pdfgen"
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
	// Decide where Chrome renders BEFORE loading the scene, so a misconfigured
	// sandbox (required but no in-container browser) fails fast with a clear
	// error rather than after scene work. When the sandbox is enabled the deck
	// HTML MUST render inside the user's isolated container (security boundary +
	// the platform host is not guaranteed to have Chromium). There is NO host
	// fallback in that case: it renders in the container or the request fails.
	// When the sandbox is disabled (personal/local dev), host headless Chrome is
	// the legitimate path.
	required, backendLabel := sandboxBrowserRequiredFn()
	var browserProv pdfgen.BrowserProvider
	timeout := time.Duration(0) // pdfgen applies its bounded default when 0
	sessionID := ""
	if required {
		sessionID = "slides-pdf-" + effectiveUserID(r) // dedicated per-user PDF session, mirrors app-mcp-<user>
		mgr, err := slidesPDFBrowserManagerFn(sessionID)
		if err != nil {
			slog.Error("slides PDF: sandbox browser unavailable", "deck", mux.Vars(r)["deckSlug"], "backend", backendLabel, "error", err)
			http.Error(w, "slides PDF export requires a sandbox browser but none is available: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// No-fallback guard: if the sandbox is required but no in-container
		// browser callbacks were ever registered (e.g. K8s/OpenShell where no
		// chat has been wired yet), SandboxEnabled is false. Fail loudly
		// instead of rendering on the host.
		if !mgr.SandboxEnabled {
			slog.Error("slides PDF: sandbox required but no in-container browser callbacks registered", "backend", backendLabel)
			http.Error(w, "slides PDF export requires an in-container browser which is not available on this backend", http.StatusInternalServerError)
			return
		}
		browserProv = mgr
		// Ensure the idle reaper is running so warm slides-pdf-<user>
		// containers are destroyed after inactivity. StartIdleWatchdog is
		// idempotent (internal started guard); destroyIdle reaps any tracked
		// session id generically via DestroySession, so slides-pdf-* ids are
		// handled the same as app-mcp-* ids.
		appMCPIdleTracker.StartIdleWatchdog(context.Background(), 10*time.Minute)
		// Cold container launch + session provisioning can exceed the 30s
		// readiness default; give the first render more headroom.
		timeout = 90 * time.Second
	} else {
		browserProv = GetLocalPDFBrowserManager() // sandbox disabled → host Chrome is legitimate
	}

	scene, ok := loadSlidesScene(w, r)
	if !ok {
		return
	}

	result, err := (slides.PDFExporter{Browser: browserProv, RuntimeJS: webassets.GetSlidesRuntime(), Timeout: timeout}).Export(scene)
	if err != nil {
		slog.Error("slides PDF export failed", "deck", mux.Vars(r)["deckSlug"], "scope", r.URL.Query().Get("scope"), "error", err)
		http.Error(w, "export slides PDF: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// On the sandbox path, keep the warm per-user container alive between
	// exports and let the idle watchdog reap it after inactivity.
	if required {
		appMCPIdleTracker.touch(sessionID)
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", attachmentName(mux.Vars(r)["deckSlug"], "pdf"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Bytes)
}

func ExportSlidesPPTXHandler(w http.ResponseWriter, r *http.Request) {
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	deckSlug := mux.Vars(r)["deckSlug"]
	scene, _, err := svc.Scene(r.Context(), deckSlug)
	if err != nil {
		writeSlidesError(w, err)
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
		slog.Error("slides PPTX export failed", "deck", deckSlug, "error", err)
		http.Error(w, "export slides PPTX: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	w.Header().Set("Content-Disposition", attachmentName(deckSlug, "pptx"))
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
		slog.Error("slides HTML render failed", "deck", mux.Vars(r)["deckSlug"], "error", err)
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
