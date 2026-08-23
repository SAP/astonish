package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// maxImportPPTXBytes bounds the uploaded .pptx payload accepted by the import
// endpoint (25 MiB). Larger uploads are rejected with 413.
const maxImportPPTXBytes = 25 << 20

// pptxMIME is the OOXML PowerPoint content type used to validate uploads whose
// filename does not end in .pptx.
const pptxMIME = "application/vnd.openxmlformats-officedocument.presentationml.presentation"

// ListSlidesTemplatesHandler returns the merged set of built-in templates plus
// any templates persisted in the requested scope (?scope=personal|team). The
// built-ins are always returned even when no scoped service is available.
// Deduplication is by Name, preferring the built-in over a scoped template.
func ListSlidesTemplatesHandler(w http.ResponseWriter, r *http.Request) {
	merged := make([]themes.Template, 0)
	seen := map[string]bool{}
	for _, t := range themes.ListTemplates() {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		merged = append(merged, t)
	}

	// Scoped templates are best-effort: if the request has no usable docs
	// service we still return the built-ins with 200.
	if svc, err := docsService(r); err == nil {
		if scoped, err := svc.ListTemplates(r.Context()); err == nil {
			for _, t := range scoped {
				if seen[t.Name] {
					continue
				}
				seen[t.Name] = true
				merged = append(merged, t)
			}
		}
	}

	writeSlidesJSON(w, http.StatusOK, map[string]any{"templates": merged})
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
		http.Error(w, "upload exceeds 25MB limit", http.StatusRequestEntityTooLarge)
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
		http.Error(w, "import worker returned invalid template", http.StatusInternalServerError)
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
