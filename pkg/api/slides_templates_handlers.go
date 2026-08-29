package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
	webassets "github.com/SAP/astonish/web"
	"github.com/gorilla/mux"
)

// maxImportPPTXBytes bounds the uploaded .pptx payload accepted by the import
// endpoint (75 MiB). Larger uploads are rejected with 413. Corporate templates
// with embedded fonts, master imagery, and media routinely exceed 25 MiB, and
// the original bytes are persisted (base64, ~33% inflation) for the fidelity
// export path, so the cap must accommodate real-world decks.
const maxImportPPTXBytes = 75 << 20

// pptxMIME is the OOXML PowerPoint content type used to validate uploads whose
// filename does not end in .pptx.
const pptxMIME = "application/vnd.openxmlformats-officedocument.presentationml.presentation"

// slidesTemplateListItem is the lightweight DTO returned by
// ListSlidesTemplatesHandler. It intentionally OMITS the Assets map and per-
// archetype markup — a Templates UI listing many templates must not ship
// megabytes of asset bytes. Cover carries ONE representative slide (baked PNG
// ref, or live markup when no thumbnail was baked) so the library card can
// match the chat template picker. Scope is "builtin" | "personal" | "team".
type slidesTemplateListItem struct {
	Name           string                   `json:"name"`
	Label          string                   `json:"label,omitempty"`
	Description    string                   `json:"description,omitempty"`
	Scope          string                   `json:"scope"`
	Tokens         map[string]string        `json:"tokens,omitempty"`
	ArchetypeKinds []string                 `json:"archetypeKinds,omitempty"`
	Archetypes     []slidesArchetypeVariant `json:"archetypes,omitempty"`
	Cover          *slidesTemplateCover     `json:"cover,omitempty"`
}

// slidesTemplateCover is the representative slide shown on a Templates library
// card (same pick as the chat slidesTemplatePicker: first title* archetype,
// else the first archetype). ThumbnailRef is a baked PNG asset key served by
// GetSlidesTemplateThumbnailHandler. Markup is the live-render fallback and is
// omitted when ThumbnailRef is set so imported catalogs stay small.
type slidesTemplateCover struct {
	Kind         string `json:"kind"`
	ThumbnailRef string `json:"thumbnailRef,omitempty"`
	Markup       string `json:"markup,omitempty"`
}

// slidesArchetypeVariant is one fillable slide skeleton: its role Kind plus a
// human-readable Label. A template may carry multiple variants per role.
type slidesArchetypeVariant struct {
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
	// ThumbnailRef is the deck Assets key of a pre-baked PNG thumbnail for this
	// archetype (empty when none was baked). Only the ref string is shipped; the
	// asset bytes are served separately by GetSlidesTemplateThumbnailHandler.
	ThumbnailRef string `json:"thumbnailRef,omitempty"`
}

// hexColorRe matches a #RRGGBB or #RRGGBBAA color used by recolor tokens.
var hexColorRe = regexp.MustCompile(`^#([0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// recolorTokenKeys is the closed set of palette tokens the recolor endpoint
// accepts. Anything else is rejected (400) so a client cannot smuggle
// arbitrary keys into a template's theme.
var recolorTokenKeys = map[string]bool{"surface": true, "ink": true, "accent": true}

// archetypeKinds projects a template's archetypes to their kinds only.
func archetypeKinds(t themes.Template) []string {
	kinds := make([]string, 0, len(t.Archetypes))
	for _, a := range t.Archetypes {
		kinds = append(kinds, a.Kind)
	}
	return kinds
}

// archetypeVariants projects a template's archetypes to {kind,label} variants.
// Markup is omitted; the Templates library card uses Cover instead of listing
// every layout.
func archetypeVariants(t themes.Template) []slidesArchetypeVariant {
	out := make([]slidesArchetypeVariant, 0, len(t.Archetypes))
	for _, a := range t.Archetypes {
		out = append(out, slidesArchetypeVariant{Kind: a.Kind, Label: a.Title, ThumbnailRef: a.ThumbnailRef})
	}
	return out
}

func slidesTemplateListDTO(t themes.Template, scope string) slidesTemplateListItem {
	return slidesTemplateListItem{
		Name:           t.Name,
		Label:          t.Label,
		Description:    t.Description,
		Scope:          scope,
		Tokens:         t.Tokens,
		ArchetypeKinds: archetypeKinds(t),
		Archetypes:     archetypeVariants(t),
		Cover:          templateCoverDTO(t),
	}
}

// templateCoverDTO picks the cover slide the chat template picker uses: first
// title* role, else the first archetype. Prefers a baked PNG ref; ships markup
// only when there is no thumbnail (built-ins and older imports).
func templateCoverDTO(t themes.Template) *slidesTemplateCover {
	arch := templateCoverArchetype(t)
	if arch == nil {
		return nil
	}
	cover := &slidesTemplateCover{Kind: arch.Kind}
	if ref := strings.TrimSpace(arch.ThumbnailRef); ref != "" {
		cover.ThumbnailRef = ref
		return cover
	}
	if strings.TrimSpace(arch.Markup) != "" {
		cover.Markup = arch.Markup
	}
	return cover
}

func templateCoverArchetype(t themes.Template) *themes.Archetype {
	for i := range t.Archetypes {
		if templateBaseKind(t.Archetypes[i].Kind) == "title" {
			return &t.Archetypes[i]
		}
	}
	if len(t.Archetypes) > 0 {
		return &t.Archetypes[0]
	}
	return nil
}

// templateBaseKind collapses a numbered variant suffix (title-2 → title).
func templateBaseKind(kind string) string {
	if i := strings.LastIndexByte(kind, '-'); i > 0 {
		if _, err := strconv.Atoi(kind[i+1:]); err == nil {
			return kind[:i]
		}
	}
	return kind
}

// scopeQuery returns the requested scope label for list DTOs: "team" when
// ?scope=team, else "personal".
func scopeQuery(r *http.Request) string {
	if r.URL.Query().Get("scope") == "team" {
		return "team"
	}
	return "personal"
}

// ListSlidesTemplatesHandler returns the merged set of built-in templates plus
// any templates persisted in the requested scope (?scope=personal|team) as a
// LIGHTWEIGHT DTO (no assets map; per-archetype markup omitted). Cover may
// include one live-render markup fragment when no baked thumbnail exists. The
// built-ins are always returned even when no scoped service is available.
// Deduplication is by Name, preferring the built-in over a scoped template.
func ListSlidesTemplatesHandler(w http.ResponseWriter, r *http.Request) {
	merged := make([]slidesTemplateListItem, 0)
	seen := map[string]bool{}
	for _, t := range themes.ListTemplates() {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		merged = append(merged, slidesTemplateListDTO(t, "builtin"))
	}

	// Scoped templates are best-effort: if the request has no usable docs
	// service we still return the built-ins with 200.
	if svc, err := docsService(r); err == nil {
		if scoped, err := svc.ListTemplates(r.Context()); err == nil {
			scope := scopeQuery(r)
			for _, t := range scoped {
				if seen[t.Name] {
					continue
				}
				seen[t.Name] = true
				merged = append(merged, slidesTemplateListDTO(t, scope))
			}
		}
	}

	writeSlidesJSON(w, http.StatusOK, map[string]any{"templates": merged})
}

// DeleteSlidesTemplateHandler deletes a SCOPED template (its hidden tmpl/<name>
// deck). Built-in templates are read-only and cannot be deleted (403). Because
// the underlying DeleteDeck is silent on a missing deck, deleting an absent
// template is idempotent and still returns 204.
func DeleteSlidesTemplateHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if strings.TrimSpace(name) == "" {
		http.Error(w, "template name is required", http.StatusBadRequest)
		return
	}
	if _, ok := themes.LookupTemplate(name); ok {
		http.Error(w, "cannot delete a built-in template", http.StatusForbidden)
		return
	}
	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteDeck(r.Context(), svc.TemplateSlug(name)); err != nil {
		writeSlidesError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// duplicateTemplateRequest is the optional JSON body for a duplicate request.
type duplicateTemplateRequest struct {
	NewName  string `json:"newName"`
	NewLabel string `json:"newLabel"`
}

// DuplicateSlidesTemplateHandler clones a template (built-in OR scoped) into a
// NEW scoped template the user can then edit.
func DuplicateSlidesTemplateHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if strings.TrimSpace(name) == "" {
		http.Error(w, "template name is required", http.StatusBadRequest)
		return
	}

	var body duplicateTemplateRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		// A missing/empty body is fine; only reject malformed JSON.
		if err := dec.Decode(&body); err != nil && err != io.EOF {
			http.Error(w, "invalid duplicate request body", http.StatusBadRequest)
			return
		}
	}

	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}

	// Resolve the source: built-in first, then a scoped template.
	src, found := themes.LookupTemplate(name)
	if !found {
		scoped, ok, err := svc.Template(r.Context(), name)
		if err != nil {
			writeSlidesError(w, err)
			return
		}
		if !ok {
			http.Error(w, "template not found", http.StatusNotFound)
			return
		}
		src = scoped
	}

	// Target name: explicit newName, else "<name>-copy"; slugified and made
	// unique by suffixing -2, -3, ... when a tmpl/<n> deck already exists.
	target := slugifyTemplateName(body.NewName)
	if target == "" {
		target = slugifyTemplateName(name + "-copy")
	}
	if target == "" {
		target = "template-copy"
	}
	target = uniqueTemplateName(r, target)

	label := strings.TrimSpace(body.NewLabel)
	if label == "" {
		if src.Label != "" {
			label = src.Label + " (copy)"
		} else {
			label = target
		}
	}

	dup := themes.Template{
		Schema:      src.Schema,
		Name:        target,
		Label:       label,
		Description: src.Description,
		Tokens:      cloneStringMap(src.Tokens),
		Assets:      cloneStringMap(src.Assets),
		Archetypes:  append([]themes.Archetype(nil), src.Archetypes...),
		Scope:       "",
	}
	if err := svc.SaveTemplate(r.Context(), dup); err != nil {
		writeSlidesError(w, err)
		return
	}

	writeSlidesJSON(w, http.StatusOK, map[string]any{
		"template": map[string]any{"name": dup.Name, "label": dup.Label},
	})
}

// recolorTemplateRequest is the JSON body for a recolor request.
type recolorTemplateRequest struct {
	Tokens map[string]string `json:"tokens"`
}

// RecolorSlidesTemplateHandler updates the surface/ink/accent theme tokens of a
// SCOPED template. Built-ins are read-only (403). Unknown keys or malformed hex
// values are rejected (400).
//
// v1 limitation: only the Tokens palette is updated; the persisted archetype
// markup keeps its originally-embedded colors. Regenerating archetypes from the
// new palette (via themes.ArchetypesFor) is a deliberate follow-up so this
// endpoint stays small and predictable.
func RecolorSlidesTemplateHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if strings.TrimSpace(name) == "" {
		http.Error(w, "template name is required", http.StatusBadRequest)
		return
	}
	if _, ok := themes.LookupTemplate(name); ok {
		http.Error(w, "cannot recolor a built-in template", http.StatusForbidden)
		return
	}

	var body recolorTemplateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "invalid recolor request body", http.StatusBadRequest)
		return
	}
	if len(body.Tokens) == 0 {
		http.Error(w, "tokens are required", http.StatusBadRequest)
		return
	}
	for k, v := range body.Tokens {
		if !recolorTokenKeys[k] {
			http.Error(w, "unknown token key: "+k, http.StatusBadRequest)
			return
		}
		if !hexColorRe.MatchString(v) {
			http.Error(w, "invalid hex color for "+k+": "+v, http.StatusBadRequest)
			return
		}
	}

	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	tmpl, found, err := svc.Template(r.Context(), name)
	if err != nil {
		writeSlidesError(w, err)
		return
	}
	if !found {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}

	// Overlay the provided tokens onto the existing palette.
	if tmpl.Tokens == nil {
		tmpl.Tokens = map[string]string{}
	}
	for k, v := range body.Tokens {
		tmpl.Tokens[k] = v
	}
	if err := svc.SaveTemplate(r.Context(), tmpl); err != nil {
		writeSlidesError(w, err)
		return
	}

	writeSlidesJSON(w, http.StatusOK, slidesTemplateListDTO(tmpl, scopeQuery(r)))
}

// thumbnailPNGPrefix is the data-URI prefix stripped before base64-decoding a
// baked archetype thumbnail asset.
const thumbnailPNGPrefix = "data:image/png;base64,"

func resolveSlidesTemplateFromRequest(r *http.Request, name string) (themes.Template, bool) {
	tmpl, found := themes.LookupTemplate(name)
	if found {
		return slides.HydrateTemplateFonts(tmpl), true
	}
	svc, err := docsService(r)
	if err != nil {
		return themes.Template{}, false
	}
	scoped, err := svc.ListTemplates(r.Context())
	if err != nil {
		return themes.Template{}, false
	}
	for _, t := range scoped {
		if t.Name == name {
			return t, true
		}
	}
	return themes.Template{}, false
}

// GetSlidesTemplateThumbnailHandler serves the pre-baked PNG thumbnail for a
// single archetype of a template. It resolves the template (built-in first,
// then the scoped docs service), finds the archetype by exact kind (falling
// back to a variant-suffix-insensitive match), decodes the base64 PNG data URI
// stored in the template's Assets under the archetype's ThumbnailRef, and
// streams it with long-lived immutable cache headers. Any missing template,
// archetype, thumbnail ref, asset, or decode failure returns 404 so a client
// can transparently fall back to a live render.
func GetSlidesTemplateThumbnailHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := strings.TrimSpace(vars["name"])
	kind := strings.TrimSpace(vars["kind"])
	if name == "" || kind == "" {
		http.Error(w, "template name and kind are required", http.StatusBadRequest)
		return
	}

	tmpl, found := resolveSlidesTemplateFromRequest(r, name)
	if !found {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}

	// Exact kind only — title-2 is a different cover from title. Falling back
	// to the first title* served the wrong thumbnail (white cover vs blue anvil).
	arch, ok := findArchetypeForThumbnail(tmpl, kind)
	if !ok || arch.ThumbnailRef == "" {
		http.Error(w, "thumbnail not found", http.StatusNotFound)
		return
	}

	asset, ok := tmpl.Assets[arch.ThumbnailRef]
	if !ok {
		http.Error(w, "thumbnail not found", http.StatusNotFound)
		return
	}
	payload := strings.TrimPrefix(asset, thumbnailPNGPrefix)
	png, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(png) == 0 {
		http.Error(w, "thumbnail not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+arch.ThumbnailRef+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

// GetSlidesTemplateMediaHandler serves one template asset by ref (sha256-…
// images or font:… faces) so pickers and thumbs can load media without
// embedding data: bytes in chat. Missing refs 404.
func GetSlidesTemplateMediaHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := strings.TrimSpace(vars["name"])
	ref := strings.TrimSpace(vars["ref"])
	if name == "" || ref == "" {
		http.Error(w, "template name and ref are required", http.StatusBadRequest)
		return
	}
	tmpl, found := resolveSlidesTemplateFromRequest(r, name)
	if !found {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}
	asset, ok := tmpl.Assets[ref]
	if !ok {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	ctype, body, ok := decodeTemplateMediaURI(asset)
	if !ok {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+ref+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func decodeTemplateMediaURI(s string) (contentType string, body []byte, ok bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "data:") {
		return "", nil, false
	}
	rest := s[len("data:"):]
	const marker = ";base64,"
	i := strings.Index(rest, marker)
	if i < 0 {
		return "", nil, false
	}
	mime := rest[:i]
	if mime == "image/svg+xml" {
		return "", nil, false
	}
	if !strings.HasPrefix(mime, "image/") && !strings.HasPrefix(mime, "font/") {
		return "", nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(rest[i+len(marker):])
	if err != nil || len(raw) == 0 {
		return "", nil, false
	}
	return mime, raw, true
}

// findArchetypeForThumbnail returns the archetype matching kind exactly.
// Variant suffixes are significant: title-2 is a different cover from title.
func findArchetypeForThumbnail(tmpl themes.Template, kind string) (themes.Archetype, bool) {
	for _, a := range tmpl.Archetypes {
		if a.Kind == kind {
			return a, true
		}
	}
	return themes.Archetype{}, false
}

// uniqueTemplateName returns base, or base-2/base-3/... if a scoped template
// deck already exists under that slug.
func uniqueTemplateName(r *http.Request, base string) string {
	dsvc, err := docsService(r)
	if err != nil {
		return base
	}
	candidate := base
	for i := 2; ; i++ {
		if _, _, dErr := dsvc.Deck(r.Context(), dsvc.TemplateSlug(candidate)); dErr != nil {
			return candidate // not found (or store error) -> treat as free
		}
		candidate = base + "-" + strconv.Itoa(i)
		if i > 100 { // pathological guard
			return candidate
		}
	}
}

// cloneStringMap returns a shallow copy of m (nil-safe).
func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ImportSlidesTemplateHandler accepts a multipart .pptx upload, converts it to
// an ASD v2 Template via the pinned Node import worker, persists it in the
// requested scope, and returns the stored template identity plus any warnings.
func ImportSlidesTemplateHandler(w http.ResponseWriter, r *http.Request) {
	// Bound the total request body before parsing so a malicious client cannot
	// exhaust memory regardless of the reported Content-Length. Allow a small
	// slack over the payload cap for multipart headers/boundaries; the exact
	// .pptx size is re-checked below against maxImportPPTXBytes.
	r.Body = http.MaxBytesReader(w, r.Body, maxImportPPTXBytes+(1<<20))
	// #nosec G120 -- r.Body is bounded by http.MaxBytesReader above, so form
	// parsing cannot exhaust memory; the exact .pptx size is re-checked below.
	if err := r.ParseMultipartForm(maxImportPPTXBytes); err != nil {
		// Distinguish an oversized upload (body exceeded the MaxBytesReader cap
		// during parsing) from a genuinely malformed multipart body, so the
		// client gets an actionable 413 instead of a confusing 400.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || strings.Contains(err.Error(), "request body too large") {
			http.Error(w, "upload exceeds 75MB limit", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid multipart upload", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file upload", http.StatusBadRequest)
		return
	}
	defer file.Close()

	filename := ""
	if header != nil {
		filename = header.Filename
	}
	if !isPPTXUpload(filename, header) {
		http.Error(w, "upload must be a .pptx file", http.StatusBadRequest)
		return
	}

	// Read at most maxImportPPTXBytes+1 so we can detect oversized uploads
	// deterministically regardless of the reported Content-Length.
	limited := io.LimitReader(file, maxImportPPTXBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		http.Error(w, "read upload", http.StatusBadRequest)
		return
	}
	if len(data) == 0 {
		http.Error(w, "empty upload", http.StatusBadRequest)
		return
	}
	if len(data) > maxImportPPTXBytes {
		http.Error(w, "upload exceeds 75MB limit", http.StatusRequestEntityTooLarge)
		return
	}

	b64 := base64.StdEncoding.EncodeToString(data)

	_, currentFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
	runner := pptxworker.ImportRunner{
		WorkingDir: filepath.Join(repoRoot, "web"),
		ScriptPath: filepath.Join(repoRoot, "pkg/docs/slides/pptxworker/import_worker.mjs"),
	}

	resp, err := runner.Run(r.Context(), pptxworker.ImportRequest{PPTXBase64: b64, Mode: "template"})
	if err != nil {
		http.Error(w, "import pptx template", importErrorStatus(err))
		return
	}

	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		slog.Error("import slides template: worker response did not decode into Template",
			"error", err, "body_prefix", truncateForLog(resp.SceneOrTemplate, 2000))
		http.Error(w, fmt.Sprintf("import worker returned invalid template: %v", err), http.StatusInternalServerError)
		return
	}

	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = slugifyTemplateName(base)
	} else {
		name = slugifyTemplateName(name)
	}
	if name == "" {
		name = "imported-template"
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" {
		if base != "" {
			label = base
		} else {
			label = name
		}
	}
	tmpl.Name = name
	tmpl.Label = label
	tmpl.Scope = ""

	// Generate rich style guidance for LLM content authoring. Best-effort:
	// a nil or incomplete guide never fails the import.
	if tmpl.Model != nil {
		tmpl.StyleGuide = themes.GenerateStyleGuide(tmpl.Model, tmpl.Tokens, tmpl.Archetypes)
		// Also store on the model so it persists through TemplateModel JSON serialization.
		tmpl.Model.StyleGuide = tmpl.StyleGuide
	}

	// Pre-bake static PNG thumbnails for each archetype using the shared headless
	// Chrome browser. This is BEST-EFFORT: any browser-launch or per-archetype
	// failure is logged and the import proceeds without thumbnails (the picker
	// falls back to a live render). Never fail the import over thumbnails.
	generateTemplateThumbnails(r.Context(), &tmpl)

	svc, ok := requireDocsService(w, r)
	if !ok {
		return
	}
	if err := svc.SaveTemplate(r.Context(), tmpl); err != nil {
		writeSlidesError(w, err)
		return
	}

	writeSlidesJSON(w, http.StatusOK, map[string]any{
		"template": map[string]any{
			"name":  tmpl.Name,
			"label": tmpl.Label,
			"scope": tmpl.Scope,
		},
		"warnings": resp.Warnings,
	})
}

// generateTemplateThumbnails pre-bakes static PNG thumbnails for each archetype
// in tmpl using the shared local headless-Chrome browser. It is BEST-EFFORT: a
// nil browser manager, a browser-launch panic, or any per-archetype error must
// not fail the import — the picker falls back to a live render for archetypes
// without a baked thumbnail. The recover guard protects against an unexpected
// panic from the browser layer.
func generateTemplateThumbnails(ctx context.Context, tmpl *themes.Template) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("slides thumbnail generation panicked; importing without thumbnails",
				"template", tmpl.Name, "panic", rec)
		}
	}()

	mgr := GetLocalPDFBrowserManager()
	if mgr == nil {
		slog.Warn("slides thumbnail generation skipped: no local PDF browser manager",
			"template", tmpl.Name)
		return
	}
	runtimeJS := webassets.GetSlidesRuntime()
	slides.GenerateArchetypeThumbnails(ctx, tmpl, runtimeJS, mgr)
}

// isPPTXUpload reports whether the upload looks like a PowerPoint file, by
// filename extension (case-insensitive) or declared content type.
func isPPTXUpload(filename string, header *multipart.FileHeader) bool {
	if strings.HasSuffix(strings.ToLower(filename), ".pptx") {
		return true
	}
	if header != nil && strings.Contains(header.Header.Get("Content-Type"), pptxMIME) {
		return true
	}
	return false
}

// slugifyTemplateName lowercases the input and replaces every run of
// non-alphanumeric characters with a single dash, trimming leading/trailing
// dashes, yielding a URL-safe template name.
func slugifyTemplateName(in string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(in) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// importErrorStatus maps an import worker error to an HTTP status. Timeouts and
// process-execution failures are 5xx; everything else (bad/corrupt input the
// worker rejected) is treated as a 400.
func importErrorStatus(err error) int {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"):
		return http.StatusGatewayTimeout
	case strings.Contains(msg, "executable file not found"),
		strings.Contains(msg, "no such file"),
		strings.Contains(msg, "unsupported pptx import worker protocol"),
		strings.Contains(msg, "returned protocol"),
		strings.Contains(msg, "decode pptx import worker response"):
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

// truncateForLog returns a bounded string view of raw JSON for diagnostic logs,
// so a large worker payload does not flood the log while still surfacing the
// shape that failed to decode.
func truncateForLog(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…(truncated)"
}
