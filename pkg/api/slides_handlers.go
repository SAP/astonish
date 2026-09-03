package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/docs/slides/components"
	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/pdfgen"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	webassets "github.com/SAP/astonish/web"
	"github.com/gorilla/mux"
)

// slidesDeckDTO is the slim per-deck payload returned by the deck-detail
// endpoint. It deliberately OMITS the two heavy store.DeckManifest fields —
// Assets (a base64 data: URI per logo/image) and TemplateModel (the multi-MB
// lossless imported-template IR) — which no client consumer reads and which
// made opening an imported-template deck (and the chat SlidesDeckView) hang.
// The present iframe and the PPTX/PDF/HTML exporters still get Assets straight
// from the store via Service.Scene, so rendering is unaffected. Theme is kept
// (small) so any future client styling has it.
type slidesDeckDTO struct {
	ID            string            `json:"id"`
	Slug          string            `json:"slug"`
	Title         string            `json:"title"`
	Description   string            `json:"description,omitempty"`
	SchemaVersion int               `json:"schemaVersion"`
	Theme         map[string]string `json:"theme,omitempty"`
	Scope         string            `json:"scope,omitempty"`
	SessionID     string            `json:"sessionId,omitempty"`
	Version       int               `json:"version,omitempty"`
	SourceSlug    string            `json:"sourceSlug,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

// slidesDeckListItem is the slim summary returned by the merged deck list. Like
// slidesDeckDTO it drops Assets + TemplateModel; it also drops Theme because the
// list view never styles individual decks. Serializing the full manifest for
// EVERY deck is what made the Slides list slow when an imported template was
// present.
type slidesDeckListItem struct {
	ID             string    `json:"id"`
	Slug           string    `json:"slug"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	SchemaVersion  int       `json:"schemaVersion"`
	Scope          string    `json:"scope,omitempty"`
	ThumbnailReady bool      `json:"thumbnailReady,omitempty"`
	SessionID      string    `json:"sessionId,omitempty"`
	Version        int       `json:"version,omitempty"`
	SourceSlug     string    `json:"sourceSlug,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// slimDeck projects a store.DeckManifest to the list summary (no heavy fields).
func slimDeck(d *store.DeckManifest) slidesDeckListItem {
	if d == nil {
		return slidesDeckListItem{}
	}
	return slidesDeckListItem{
		ID:             d.ID,
		Slug:           d.Slug,
		Title:          d.Title,
		Description:    d.Description,
		SchemaVersion:  d.SchemaVersion,
		Scope:          d.Scope,
		ThumbnailReady: d.ThumbnailReady,
		SessionID:      d.SessionID,
		Version:        d.Version,
		SourceSlug:     d.SourceSlug,
		CreatedAt:      d.CreatedAt,
		UpdatedAt:      d.UpdatedAt,
	}
}

// slimDeckFull projects a store.DeckManifest to the deck-detail DTO (keeps Theme,
// drops Assets + TemplateModel).
func slimDeckFull(d *store.DeckManifest) *slidesDeckDTO {
	if d == nil {
		return nil
	}
	return &slidesDeckDTO{
		ID:            d.ID,
		Slug:          d.Slug,
		Title:         d.Title,
		Description:   d.Description,
		SchemaVersion: d.SchemaVersion,
		Theme:         d.Theme,
		Scope:         d.Scope,
		SessionID:     d.SessionID,
		Version:       d.Version,
		SourceSlug:    d.SourceSlug,
		CreatedAt:     d.CreatedAt,
		UpdatedAt:     d.UpdatedAt,
	}
}

// slimDecks maps a slice of manifests to list summaries.
func slimDecks(decks []*store.DeckManifest) []slidesDeckListItem {
	out := make([]slidesDeckListItem, 0, len(decks))
	for _, d := range decks {
		out = append(out, slimDeck(d))
	}
	return out
}

type slidesDeckResponse struct {
	Deck   *slidesDeckDTO        `json:"deck"`
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
		decks, err := svc.ListDecksLite(r.Context())
		if err != nil {
			writeSlidesError(w, err)
			return
		}
		for i := range decks {
			decks[i].Scope = scope
		}
		writeSlidesJSON(w, http.StatusOK, map[string]any{"type": "slides", "decks": slimDecks(decks)})
		return
	}

	svc := store.FromRequest(r)
	if svc == nil {
		http.Error(w, "request services unavailable", http.StatusServiceUnavailable)
		return
	}

	merged := make([]*store.DeckManifest, 0)
	if svc.PersonalDocs != nil {
		decks, err := (slides.Service{Store: svc.PersonalDocs}).ListDecksLite(r.Context())
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
		decks, err := (slides.Service{Store: svc.Docs}).ListDecksLite(r.Context())
		if err != nil {
			slog.Warn("failed to list team decks", "error", err)
		} else {
			for i := range decks {
				decks[i].Scope = "team"
			}
			merged = append(merged, decks...)
		}
	}
	writeSlidesJSON(w, http.StatusOK, map[string]any{"type": "slides", "decks": slimDecks(merged)})
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
	writeSlidesJSON(w, http.StatusOK, slidesDeckResponse{Deck: slimDeckFull(deck), Slides: deckSlides})
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

type slideMovesRequest struct {
	Moves    []slideMove       `json:"moves"`
	Resizes  []slideResize     `json:"resizes"`
	Texts    []slideText       `json:"texts"`
	Deletes  []string          `json:"deletes"`
	Attrs    []slideAttrChange `json:"attrs"`
	Creates  []slideCreate     `json:"creates"`
	Reorders []string          `json:"reorders"`
}

type slideMove struct {
	ID string `json:"id"`
	X  int    `json:"x"`
	Y  int    `json:"y"`
}

type slideResize struct {
	ID string `json:"id"`
	X  int    `json:"x"`
	Y  int    `json:"y"`
	W  int    `json:"w"`
	H  int    `json:"h"`
}

type slideText struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type slideAttrChange struct {
	ID    string            `json:"id"`
	Attrs map[string]string `json:"attrs"`
}

type slideCreate struct {
	ID    string            `json:"id"`
	Tag   string            `json:"tag"`
	Attrs map[string]string `json:"attrs"`
	Text  string            `json:"text,omitempty"`
}

// PatchSlideHandler applies canvas object moves, resizes, text edits, and deletes.
func PatchSlideHandler(w http.ResponseWriter, r *http.Request) {
	position, err := strconv.Atoi(mux.Vars(r)["idx"])
	if err != nil || position < 0 {
		http.Error(w, "slide index must be a non-negative integer", http.StatusBadRequest)
		return
	}
	var body slideMovesRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid edit payload", http.StatusBadRequest)
		return
	}
	if len(body.Moves) == 0 && len(body.Resizes) == 0 && len(body.Texts) == 0 && len(body.Deletes) == 0 && len(body.Attrs) == 0 && len(body.Creates) == 0 && len(body.Reorders) == 0 {
		http.Error(w, "moves, resizes, texts, deletes, attrs, or creates are required", http.StatusBadRequest)
		return
	}
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	edits := slides.SlideEdits{Deletes: append([]string(nil), body.Deletes...)}
	for _, m := range body.Moves {
		edits.Moves = append(edits.Moves, slides.ElementMove{ID: m.ID, X: m.X, Y: m.Y})
	}
	for _, resize := range body.Resizes {
		edits.Resizes = append(edits.Resizes, slides.ElementResize{ID: resize.ID, X: resize.X, Y: resize.Y, W: resize.W, H: resize.H})
	}
	for _, t := range body.Texts {
		edits.Texts = append(edits.Texts, slides.ElementText{ID: t.ID, Text: t.Text})
	}
	for _, a := range body.Attrs {
		edits.Attrs = append(edits.Attrs, slides.ElementAttrChange{ID: a.ID, Attrs: a.Attrs})
	}
	for _, c := range body.Creates {
		edits.Creates = append(edits.Creates, slides.ElementCreate{ID: c.ID, Tag: c.Tag, Attrs: c.Attrs, Text: c.Text})
	}
	edits.Reorders = append(edits.Reorders, body.Reorders...)
	item, diags, err := svc.ApplySlideEdits(r.Context(), mux.Vars(r)["deckSlug"], position, edits)
	if err != nil {
		if errors.Is(err, store.ErrDocsNotFound) {
			writeSlidesError(w, err)
			return
		}
		http.Error(w, "invalid slide edit", http.StatusBadRequest)
		return
	}
	if slides.HasErrors(diags) {
		http.Error(w, "slide validation failed", http.StatusBadRequest)
		return
	}
	writeSlidesJSON(w, http.StatusOK, item)
}

// UploadSlideAssetHandler handles POST /api/docs/slides/{deckSlug}/assets.
// It accepts a multipart file upload (field "file"), validates the image
// through AssetIngestor.Accept, stores it via AddDeckAsset, and returns the
// content-addressed asset-ref that ast-image elements use.
func UploadSlideAssetHandler(w http.ResponseWriter, r *http.Request) {
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	deckSlug := mux.Vars(r)["deckSlug"]
	if deckSlug == "" {
		http.Error(w, "deckSlug is required", http.StatusBadRequest)
		return
	}
	// Limit upload body to the asset max (20MB) plus some multipart overhead.
	r.Body = http.MaxBytesReader(w, r.Body, slides.MaxAssetBytes+1<<20)
	if err := r.ParseMultipartForm(slides.MaxAssetBytes); err != nil { //nolint:gosec // body already bounded by MaxBytesReader above
		http.Error(w, "invalid multipart upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file field is required: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read uploaded file", http.StatusBadRequest)
		return
	}
	// Use the file's declared content type from the multipart header, not the
	// request-level Content-Type (which is multipart/form-data).
	declaredMIME := ""
	if header != nil {
		declaredMIME = header.Header.Get("Content-Type")
	}
	asset, err := slides.AssetIngestor{}.Accept(body, declaredMIME)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ref := "sha256-" + asset.ID
	dataURI := "data:" + asset.MIME + ";base64," + base64.StdEncoding.EncodeToString(asset.Bytes)
	if _, err := svc.AddDeckAsset(r.Context(), deckSlug, ref, dataURI); err != nil {
		http.Error(w, "failed to store asset: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeSlidesJSON(w, http.StatusOK, map[string]string{
		"assetRef": ref,
		"mime":     asset.MIME,
	})
}

// deckSlideThumbnailPNGPrefix is the data-URI prefix stripped before
// base64-decoding a baked per-slide deck thumbnail asset.
const deckSlideThumbnailPNGPrefix = "data:image/png;base64,"

// GetSlidesDeckSlideThumbnailHandler serves the pre-baked PNG thumbnail for a
// single slide of a deck. It resolves the deck (full manifest, incl. Assets),
// finds the slide at Position==idx, reads its ThumbnailRef, decodes the base64
// PNG data URI stored in the deck's Assets under that ref, and streams it with
// long-lived immutable cache headers. Any missing deck/slide/ref/asset or
// decode failure returns 404 so the Slides view shows an EMPTY placeholder —
// it never falls back to a live render.
func GetSlidesDeckSlideThumbnailHandler(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(mux.Vars(r)["idx"])
	if err != nil || idx < 0 {
		http.Error(w, "slide index must be a non-negative integer", http.StatusBadRequest)
		return
	}
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	deck, deckSlides, err := svc.Deck(r.Context(), mux.Vars(r)["deckSlug"])
	if err != nil {
		http.Error(w, "thumbnail not found", http.StatusNotFound)
		return
	}
	var ref string
	for _, s := range deckSlides {
		if s.Position == idx {
			ref = s.ThumbnailRef
			break
		}
	}
	if ref == "" {
		http.Error(w, "thumbnail not found", http.StatusNotFound)
		return
	}
	asset, ok := deck.Assets[ref]
	if !ok {
		http.Error(w, "thumbnail not found", http.StatusNotFound)
		return
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(asset, deckSlideThumbnailPNGPrefix))
	if err != nil || len(png) == 0 {
		http.Error(w, "thumbnail not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+ref+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
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
	setSlidesDocumentHeaders(w, false, r.URL.Query().Get("presenter") == "1")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Bytes)
}

func ExportSlidesHTMLHandler(w http.ResponseWriter, r *http.Request) {
	result, ok := exportSlidesHTML(w, r, false)
	if !ok {
		return
	}
	setSlidesDocumentHeaders(w, true, false)
	w.Header().Set("Content-Disposition", attachmentName(mux.Vars(r)["deckSlug"], "html"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Bytes)
}

var (
	newSlidesPDFSandboxBrowserFn = newSlidesPDFSandboxBrowser
	slidesTemplateLayerChainFn   = resolveTemplateLayerChain
	slidesBaseLayerChainFn       = resolveBaseLayerChain
	slidesTemplateImageFn        = resolveTemplateImage
	slidesBaseImageFn            = resolveBaseImage
)

func slidesPDFSandboxSpec(ctx context.Context, r *http.Request, userID string) sandbox.SessionSpec {
	templateName := sandbox.BaseTemplateID
	if svc := store.FromRequest(r); svc != nil && svc.Settings != nil {
		if settings, err := svc.Settings.Get(ctx); err == nil && settings != nil && settings.TemplateName != "" {
			templateName = settings.TemplateName
		}
	}

	layerChain := slidesTemplateLayerChainFn(ctx, templateName)
	if len(layerChain) == 0 {
		layerChain = slidesBaseLayerChainFn(ctx)
	}
	image := slidesTemplateImageFn(ctx, templateName)
	if image == "" {
		image = slidesBaseImageFn(ctx)
	}

	return sandbox.SessionSpec{
		Type:       sandbox.SessionTypeChat,
		TemplateID: templateName,
		LayerChain: layerChain,
		Image:      image,
		UserID:     userID,
		Labels:     map[string]string{"purpose": "slides-pdf"},
	}
}

func slidesPDFSandboxSessionID(ctx context.Context, r *http.Request, userID string) string {
	// Include the filesystem source in the ID so a changed base/template does
	// not reuse a warm pod built from an obsolete layer chain or image.
	spec := slidesPDFSandboxSpec(ctx, r, userID)
	source := strings.Join(spec.LayerChain, "\x00") + "\x00" + spec.Image
	if source != "\x00" {
		suffix := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))[:12]
		return "slides-pdf-" + userID + "-" + suffix
	}
	return "slides-pdf-" + userID
}

func newSlidesPDFSandboxBrowser(ctx context.Context, r *http.Request, sessionID, userID string) (*browser.Manager, func(), error) {
	backend, cleanup, err := sandboxBackendForRequest(r)
	if err != nil {
		return nil, nil, err
	}
	failed := true
	defer func() {
		if failed && cleanup != nil {
			cleanup()
		}
	}()

	spec := slidesPDFSandboxSpec(ctx, r, userID)
	spec.SessionID = sessionID
	if _, err := backend.CreateSession(ctx, spec); err != nil {
		return nil, nil, fmt.Errorf("create slides PDF sandbox: %w", err)
	}
	if err := backend.StartSession(ctx, sessionID); err != nil {
		return nil, nil, fmt.Errorf("start slides PDF sandbox: %w", err)
	}
	if err := backend.WaitForSessionReady(ctx, sessionID); err != nil {
		return nil, nil, fmt.Errorf("wait for slides PDF sandbox: %w", err)
	}

	cfg := browser.DefaultConfig()
	cfg.Headless = true
	cfg.UserDataDir = ""
	if appCfg := effectiveAppConfig(r); appCfg != nil && appCfg.Browser.ChromePath != "" {
		cfg.ChromePath = appCfg.Browser.ChromePath
	}
	mgr := browser.NewManager(cfg)
	if !sandbox.WireBackendBrowserManager(mgr, backend, nil, nil, appMCPIdleTracker.touch) {
		return nil, nil, fmt.Errorf("sandbox backend %q does not support PDF browser execution", backend.Kind())
	}
	mgr.EnsureSessionID(sessionID)
	failed = false
	return mgr, cleanup, nil
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
		userID := effectiveUserID(r)
		sessionID = slidesPDFSandboxSessionID(r.Context(), r, userID)
		provisionCtx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		mgr, cleanup, err := newSlidesPDFSandboxBrowserFn(provisionCtx, r, sessionID, userID)
		cancel()
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			slog.Error("slides PDF: sandbox provisioning failed", "deck", mux.Vars(r)["deckSlug"], "backend", backendLabel, "error", err)
			http.Error(w, "slides PDF export could not prepare its sandbox: "+err.Error(), http.StatusInternalServerError)
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

	workingDir, scriptPath, err := slidesWorkerPaths("worker.mjs")
	if err != nil {
		slog.Error("slides PPTX worker unavailable", "deck", deckSlug, "error", err)
		http.Error(w, "export slides PPTX: "+err.Error(), http.StatusInternalServerError)
		return
	}
	exporter := slides.PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: workingDir,
		ScriptPath: scriptPath,
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
	// If the deck has no slides yet (e.g. freshly cloned, still being built),
	// return a minimal empty-state HTML document instead of failing with a 500.
	if len(scene.Slides) == 0 {
		empty := []byte(`<!doctype html><html><head><meta charset="utf-8"><title>` + scene.Title + `</title><style>body{display:flex;align-items:center;justify-content:center;height:100vh;margin:0;font-family:system-ui;color:#64748b;background:#0f172a}</style></head><body><p>Generating slides…</p></body></html>`)
		return slides.ExportResult{Bytes: empty}, true
	}
	result, err := (slides.HTMLExporter{RuntimeJS: webassets.GetSlidesRuntime(), Print: print}).Export(scene)
	if err != nil {
		slog.Error("slides HTML render failed", "deck", mux.Vars(r)["deckSlug"], "error", err)
		http.Error(w, "render slides", http.StatusInternalServerError)
		return slides.ExportResult{}, false
	}
	return result, true
}

var generateRequiredDeckThumbnailsFn = generateRequiredDeckThumbnails

// generateRequiredDeckThumbnails uses the request-scoped browser and reports
// every setup or rendering failure to the save handler.
func generateRequiredDeckThumbnails(ctx context.Context, r *http.Request, svc slides.Service, slug string) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("thumbnail generation panicked: %v", rec)
		}
	}()

	required, backendLabel := sandboxBrowserRequiredFn()
	var browserProv pdfgen.BrowserProvider
	if required {
		userID := effectiveUserID(r)
		sessionID := slidesPDFSandboxSessionID(ctx, r, userID)
		if backendLabel == string(sandbox.BackendKindK8s) {
			mgr, cleanup, createErr := newSlidesPDFSandboxBrowserFn(ctx, r, sessionID, userID)
			if cleanup != nil {
				defer cleanup()
			}
			if createErr != nil {
				return createErr
			}
			browserProv = mgr
		} else {
			mgr, managerErr := slidesPDFBrowserManagerFn(sessionID)
			if managerErr != nil {
				return managerErr
			}
			if !mgr.SandboxEnabled {
				return fmt.Errorf("sandbox browser is unavailable on backend %q", backendLabel)
			}
			browserProv = mgr
		}
		appMCPIdleTracker.StartIdleWatchdog(context.Background(), 10*time.Minute)
		defer appMCPIdleTracker.touch(sessionID)
	} else {
		browserProv = GetLocalPDFBrowserManager()
	}
	return slides.GenerateDeckThumbnails(ctx, svc, slug, webassets.GetSlidesRuntime(), browserProv)
}

// bakeDeckThumbnails renders thumbnails asynchronously for completed chat decks.
// Failures are logged because this path must not fail chat completion.
func bakeDeckThumbnails(ctx context.Context, svc slides.Service, slug, sessionSuffix string) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("deck slide thumbnail baking panicked", "deck", slug, "panic", rec)
		}
	}()

	required, backendLabel := sandboxBrowserRequiredFn()
	var browserProv pdfgen.BrowserProvider
	if required {
		sessionID := "slides-thumb-" + sessionSuffix
		mgr, err := slidesPDFBrowserManagerFn(sessionID)
		if err != nil {
			slog.Warn("deck slide thumbnail baking skipped: sandbox browser unavailable",
				"deck", slug, "backend", backendLabel, "error", err)
			return
		}
		// No-fallback guard: never render on the host when the sandbox is required.
		if !mgr.SandboxEnabled {
			slog.Warn("deck slide thumbnail baking skipped: sandbox required but no in-container browser",
				"deck", slug, "backend", backendLabel)
			return
		}
		browserProv = mgr
		appMCPIdleTracker.StartIdleWatchdog(context.Background(), 10*time.Minute)
		defer appMCPIdleTracker.touch(sessionID)
	} else {
		mgr := GetLocalPDFBrowserManager()
		if mgr == nil {
			slog.Warn("deck slide thumbnail baking skipped: no local browser manager", "deck", slug)
			return
		}
		browserProv = mgr
	}

	if err := slides.GenerateDeckThumbnails(ctx, svc, slug, webassets.GetSlidesRuntime(), browserProv); err != nil {
		slog.Warn("deck slide thumbnail baking failed", "deck", slug, "error", err)
	}
}

func setSlidesDocumentHeaders(w http.ResponseWriter, download, presenter bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	policy := "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
	if !presenter {
		policy = "sandbox allow-scripts; " + policy
	}
	w.Header().Set("Content-Security-Policy", policy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !download {
		w.Header().Set("Content-Disposition", "inline")
	}
}

func attachmentName(slug, extension string) string {
	return fmt.Sprintf("attachment; filename=%q", slug+"."+extension)
}

// --- Save / Versions / Restore handlers ---

// saveSlidesDeckRequest is the JSON body for POST /api/docs/slides/{deckSlug}/save.
type saveSlidesDeckRequest struct {
	TargetSlug string `json:"targetSlug"`
	Title      string `json:"title"`
	Override   bool   `json:"override"`
}

// deckSnapshotPayload is the JSON structure stored inside DeckVersionSnapshot.Snapshot.
type deckSnapshotPayload struct {
	Theme         map[string]string     `json:"theme,omitempty"`
	Assets        map[string]string     `json:"assets,omitempty"`
	Slides        []*store.SlideContent `json:"slides"`
	TemplateModel string                `json:"templateModel,omitempty"`
}

func assetsWithoutDeckThumbnails(assets map[string]string) map[string]string {
	clean := make(map[string]string, len(assets))
	for ref, data := range assets {
		if !strings.HasPrefix(ref, "slidethumb/") {
			clean[ref] = data
		}
	}
	return clean
}

func replaceDeckSlides(ctx context.Context, docs store.DocsStore, deckID string, current, replacements []*store.SlideContent) error {
	for _, slide := range current {
		if err := docs.DeleteSlide(ctx, deckID, slide.ID); err != nil {
			return fmt.Errorf("delete slide %q: %w", slide.ID, err)
		}
	}
	for _, source := range replacements {
		slide := &store.SlideContent{
			DeckID: deckID, Position: source.Position,
			Title: source.Title, Content: source.Content, Notes: source.Notes,
			ThumbnailRef: source.ThumbnailRef, SchemaVersion: source.SchemaVersion,
		}
		if err := docs.UpsertSlide(ctx, slide); err != nil {
			return fmt.Errorf("upsert slide at position %d: %w", source.Position, err)
		}
	}
	return nil
}

func restoreSavedDeck(ctx context.Context, docs store.DocsStore, deck *store.DeckManifest, deckSlides []*store.SlideContent) error {
	current, err := docs.ListSlides(ctx, deck.ID)
	if err != nil {
		return fmt.Errorf("list replacement slides: %w", err)
	}
	if err := docs.UpdateDeck(ctx, deck); err != nil {
		return fmt.Errorf("restore deck: %w", err)
	}
	if err := replaceDeckSlides(ctx, docs, deck.ID, current, deckSlides); err != nil {
		return fmt.Errorf("restore slides: %w", err)
	}
	return nil
}

// SaveSlidesDeckHandler handles POST /api/docs/slides/{deckSlug}/save.
// It copies a session-scoped deck into permanent storage. The session deck remains
// unchanged (like Apps: the session continues with its own copy).
// Body JSON: { "name": "my-deck-name" }
//   - If a saved deck with that name (slug) already exists: archives the old version
//     (up to 5), then overwrites it with the current session deck content. Version is bumped.
//   - If no saved deck with that name exists: creates a new permanent deck.
//
// The session deck is NEVER deleted or modified — it stays in the session for further edits.
func SaveSlidesDeckHandler(w http.ResponseWriter, r *http.Request) {
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	deckSlug := mux.Vars(r)["deckSlug"]

	var req saveSlidesDeckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req = saveSlidesDeckRequest{}
	}

	// Load the source deck.
	sourceDeck, sourceSlides, err := svc.Deck(ctx, deckSlug)
	if err != nil {
		writeSlidesError(w, err)
		return
	}

	// The source deck must be session-scoped (non-empty SessionID).
	if sourceDeck.SessionID == "" {
		http.Error(w, "deck is already saved", http.StatusBadRequest)
		return
	}

	// A name (target slug) is required — the user must choose a name.
	targetSlug := strings.TrimSpace(req.TargetSlug)
	if targetSlug == "" {
		http.Error(w, "name is required: provide targetSlug", http.StatusBadRequest)
		return
	}

	// Check if a saved deck with that slug already exists.
	existingDeck, _ := svc.Store.GetDeck(ctx, targetSlug)

	if existingDeck != nil && existingDeck.SessionID == "" {
		// Existing saved deck found — archive it and overwrite (version bump).
		existingSlides, _ := svc.Store.ListSlides(ctx, existingDeck.ID)
		previousDeck := *existingDeck
		previousDeck.Theme = cloneStringMap(existingDeck.Theme)
		previousDeck.Assets = cloneStringMap(existingDeck.Assets)
		snapPayload, _ := json.Marshal(deckSnapshotPayload{
			Theme:         existingDeck.Theme,
			Assets:        existingDeck.Assets,
			Slides:        existingSlides,
			TemplateModel: existingDeck.TemplateModel,
		})
		snapshot := &store.DeckVersionSnapshot{
			DeckSlug: existingDeck.Slug,
			Version:  existingDeck.Version,
			Title:    existingDeck.Title,
			Snapshot: string(snapPayload),
		}
		if err := svc.Store.SaveDeckVersion(ctx, snapshot); err != nil {
			slog.Warn("save deck version failed", "deck", existingDeck.Slug, "error", err)
		}

		// Overwrite with source content.
		overrideTitle := strings.TrimSpace(req.Title)
		if overrideTitle == "" {
			overrideTitle = sourceDeck.Title
		}
		existingDeck.Title = overrideTitle
		existingDeck.Theme = cloneStringMap(sourceDeck.Theme)
		existingDeck.Assets = assetsWithoutDeckThumbnails(sourceDeck.Assets)
		existingDeck.TemplateModel = sourceDeck.TemplateModel
		existingDeck.ThumbnailReady = false
		existingDeck.Version++
		if err := svc.Store.UpdateDeck(ctx, existingDeck); err != nil {
			writeSlidesError(w, err)
			return
		}

		// Replace slides on existing deck.
		if err := replaceDeckSlides(ctx, svc.Store, existingDeck.ID, existingSlides, sourceSlides); err != nil {
			if rollbackErr := restoreSavedDeck(ctx, svc.Store, &previousDeck, existingSlides); rollbackErr != nil {
				slog.Error("failed to roll back deck after slide replacement failure", "deck", existingDeck.Slug, "error", rollbackErr)
			}
			http.Error(w, "save slides: replace slides: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if err := generateRequiredDeckThumbnailsFn(ctx, r, svc, existingDeck.Slug); err != nil {
			slog.Error("saved deck thumbnail generation failed", "deck", existingDeck.Slug, "error", err)
			if rollbackErr := restoreSavedDeck(ctx, svc.Store, &previousDeck, existingSlides); rollbackErr != nil {
				slog.Error("failed to roll back deck after thumbnail failure", "deck", existingDeck.Slug, "error", rollbackErr)
			}
			http.Error(w, "save slides: generate thumbnails: "+err.Error(), http.StatusInternalServerError)
			return
		}
		existingDeck, sourceSlides, err = svc.Deck(ctx, existingDeck.Slug)
		if err != nil {
			writeSlidesError(w, err)
			return
		}
		writeSlidesJSON(w, http.StatusOK, slidesDeckResponse{Deck: slimDeckFull(existingDeck), Slides: sourceSlides})
	} else {
		// No existing saved deck — create a new permanent deck with the target slug.
		// Use the provided title, or fall back to the source deck's title.
		title := strings.TrimSpace(req.Title)
		if title == "" {
			title = sourceDeck.Title
		}
		newDeck := &store.DeckManifest{
			Slug:           targetSlug,
			Title:          title,
			Description:    sourceDeck.Description,
			SchemaVersion:  sourceDeck.SchemaVersion,
			Theme:          cloneStringMap(sourceDeck.Theme),
			Assets:         assetsWithoutDeckThumbnails(sourceDeck.Assets),
			TemplateModel:  sourceDeck.TemplateModel,
			ThumbnailReady: false,
			Version:        1,
			// SessionID is empty = permanent/saved
		}
		if err := svc.Store.CreateDeck(ctx, newDeck); err != nil {
			http.Error(w, "failed to create saved deck: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := replaceDeckSlides(ctx, svc.Store, newDeck.ID, nil, sourceSlides); err != nil {
			if deleteErr := svc.DeleteDeck(ctx, targetSlug); deleteErr != nil {
				slog.Error("failed to roll back deck after slide copy failure", "deck", targetSlug, "error", deleteErr)
			}
			http.Error(w, "save slides: copy slides: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if err := generateRequiredDeckThumbnailsFn(ctx, r, svc, targetSlug); err != nil {
			slog.Error("saved deck thumbnail generation failed", "deck", targetSlug, "error", err)
			if deleteErr := svc.DeleteDeck(ctx, targetSlug); deleteErr != nil {
				slog.Error("failed to roll back deck after thumbnail failure", "deck", targetSlug, "error", deleteErr)
			}
			http.Error(w, "save slides: generate thumbnails: "+err.Error(), http.StatusInternalServerError)
			return
		}
		newDeck, sourceSlides, err = svc.Deck(ctx, targetSlug)
		if err != nil {
			writeSlidesError(w, err)
			return
		}
		writeSlidesJSON(w, http.StatusOK, slidesDeckResponse{Deck: slimDeckFull(newDeck), Slides: sourceSlides})
	}
}

// ListSlidesDeckVersionsHandler handles GET /api/docs/slides/{deckSlug}/versions.
func ListSlidesDeckVersionsHandler(w http.ResponseWriter, r *http.Request) {
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	deckSlug := mux.Vars(r)["deckSlug"]
	versions, err := svc.Store.ListDeckVersions(r.Context(), deckSlug)
	if err != nil {
		writeSlidesError(w, err)
		return
	}
	if versions == nil {
		versions = []*store.DeckVersionSnapshot{}
	}
	writeSlidesJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

// RestoreSlidesDeckVersionHandler handles POST /api/docs/slides/{deckSlug}/versions/{version}/restore.
func RestoreSlidesDeckVersionHandler(w http.ResponseWriter, r *http.Request) {
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	deckSlug := mux.Vars(r)["deckSlug"]
	versionStr := mux.Vars(r)["version"]
	version, err := strconv.Atoi(versionStr)
	if err != nil || version < 1 {
		http.Error(w, "version must be a positive integer", http.StatusBadRequest)
		return
	}

	// Get the version snapshot to restore.
	snapshot, err := svc.Store.GetDeckVersion(ctx, deckSlug, version)
	if err != nil {
		writeSlidesError(w, err)
		return
	}

	// Load the current deck.
	deck, currentSlides, err := svc.Deck(ctx, deckSlug)
	if err != nil {
		writeSlidesError(w, err)
		return
	}

	// Archive the current state as a new version before restoring.
	currentSnapPayload, _ := json.Marshal(deckSnapshotPayload{
		Theme:         deck.Theme,
		Assets:        deck.Assets,
		Slides:        currentSlides,
		TemplateModel: deck.TemplateModel,
	})
	archiveSnapshot := &store.DeckVersionSnapshot{
		ID:       fmt.Sprintf("%s-v%d", deck.Slug, deck.Version),
		DeckSlug: deck.Slug,
		Version:  deck.Version,
		Title:    deck.Title,
		Snapshot: string(currentSnapPayload),
	}
	if err := svc.Store.SaveDeckVersion(ctx, archiveSnapshot); err != nil {
		slog.Warn("archive current version failed", "deck", deckSlug, "error", err)
	}

	// Parse the snapshot payload.
	var payload deckSnapshotPayload
	if err := json.Unmarshal([]byte(snapshot.Snapshot), &payload); err != nil {
		http.Error(w, "corrupt version snapshot", http.StatusInternalServerError)
		return
	}

	// Restore deck metadata.
	deck.Title = snapshot.Title
	deck.Theme = payload.Theme
	deck.Assets = payload.Assets
	deck.TemplateModel = payload.TemplateModel
	deck.Version++
	if err := svc.Store.UpdateDeck(ctx, deck); err != nil {
		writeSlidesError(w, err)
		return
	}

	// Replace slides: delete existing, write restored slides.
	for _, s := range currentSlides {
		_ = svc.Store.DeleteSlide(ctx, deck.ID, s.ID)
	}
	for _, s := range payload.Slides {
		slide := &store.SlideContent{
			ID: s.ID, DeckID: deck.ID, Position: s.Position,
			Title: s.Title, Content: s.Content, Notes: s.Notes,
			ThumbnailRef: s.ThumbnailRef, SchemaVersion: s.SchemaVersion,
		}
		_ = svc.Store.UpsertSlide(ctx, slide)
	}

	writeSlidesJSON(w, http.StatusOK, slidesDeckResponse{Deck: slimDeckFull(deck), Slides: payload.Slides})
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
	slog.Error("slides request failed", "error", err)
	http.Error(w, "slides request failed", http.StatusInternalServerError)
}
