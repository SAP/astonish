package slides

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// requireImportNodeEnv resolves the node working dir + worker scripts, skipping
// cleanly when node or the required node_modules are unavailable.
func requireImportNodeEnv(t *testing.T) (workingDir, importScript, exportScript string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping node-dependent test")
	}
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	workingDir = filepath.Join(repo, "web")
	for _, mod := range []string{"jszip", "fast-xml-parser", "pptxgenjs"} {
		if _, err := os.Stat(filepath.Join(workingDir, "node_modules", mod)); err != nil {
			t.Skipf("web/node_modules/%s missing; run `cd web && npm install`", mod)
		}
	}
	importScript = filepath.Join(repo, "pkg/docs/slides/pptxworker/import_worker.mjs")
	exportScript = filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs")
	return workingDir, importScript, exportScript
}

// TestImportFidelityArchetypesAreValidASD imports a real .pptx (produced by the
// export worker) in template mode and asserts every synthesized archetype
// markup parses and validates as ASD with zero errors — i.e. the IR->ASD
// serializer never emits invalid markup.
func TestImportFidelityArchetypesAreValidASD(t *testing.T) {
	workingDir, importScript, exportScript := requireImportNodeEnv(t)

	// A small but non-trivial fixture: title text + a filled rounded-rect shape
	// + an image, exercising the shape/image chrome extraction paths.
	pngData := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	scene := map[string]any{
		"schemaVersion": 2,
		"title":         "Fidelity fixture",
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
					map[string]any{
						"id": "box", "type": "shape", "geom": "roundRect",
						"geometry": map[string]any{"x": 160, "y": 260, "w": 400, "h": 200},
						"fill":     "#DBEAFE",
					},
					map[string]any{
						"id": "pic", "type": "image",
						"geometry": map[string]any{"x": 700, "y": 260, "w": 200, "h": 200},
						"props":    map[string]any{"data": pngData},
					},
				},
			},
		},
	}
	sceneJSON, err := json.Marshal(scene)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := (pptxworker.Runner{WorkingDir: workingDir, ScriptPath: exportScript, Timeout: 30 * time.Second}).
		Run(context.Background(), pptxworker.Request{ProtocolVersion: pptxworker.ProtocolVersion, Scene: sceneJSON})
	if err != nil {
		t.Fatalf("export worker failed: %v", err)
	}
	if exp.PPTXBase64 == "" {
		t.Fatal("export worker returned empty pptx")
	}

	resp, err := (pptxworker.ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), pptxworker.ImportRequest{PPTXBase64: exp.PPTXBase64, Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}

	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v\n%s", err, string(resp.SceneOrTemplate))
	}
	if len(tmpl.Archetypes) == 0 {
		t.Fatal("expected at least one archetype")
	}
	hasAssetRef := false
	for _, a := range tmpl.Archetypes {
		if a.Markup == "" {
			t.Fatalf("archetype %q has empty markup", a.Kind)
		}
		// Every archetype MUST carry a human label (the real PowerPoint layout
		// name, stored in Title); layout variants are surfaced by this label.
		if strings.TrimSpace(a.Title) == "" {
			t.Fatalf("archetype %q has empty label (title); layout variants must be labeled", a.Kind)
		}
		_, diags, err := ParseSlide(a.Markup)
		if err != nil {
			t.Fatalf("archetype %q markup failed to parse: %v\n%s", a.Kind, err, a.Markup)
		}
		if HasErrors(diags) {
			t.Fatalf("archetype %q markup has ASD errors: %+v\n%s", a.Kind, diags, a.Markup)
		}
		if strings.Contains(a.Markup, "asset-ref") {
			hasAssetRef = true
		}
	}
	// The lossless IR must round-trip through the template envelope.
	if tmpl.Model == nil {
		t.Fatal("expected templateModel (Model) on imported template")
	}
	if len(tmpl.Model.Layouts) == 0 {
		t.Fatal("expected at least one IR layout")
	}
	// Example archetypes are NO LONGER derived from thin authored slides (which
	// carry no background and rendered white). Colorful chrome lives in the
	// layouts/master and is captured via master->layout inheritance. When a
	// LAYOUT itself carries image chrome (a real corporate cover/divider), at
	// least one archetype must reference an asset, proving inherited chrome is
	// carried through (not white). The synthetic export-worker fixture puts its
	// image on an authored SLIDE (not a layout), so guard on layout image
	// chrome — not on the raw asset count — to avoid a false positive.
	layoutHasImageChrome := false
	for _, l := range tmpl.Model.Layouts {
		if l.Background.Kind == "image" || l.Background.MediaKey != "" {
			layoutHasImageChrome = true
		}
		for _, o := range l.Objects {
			if o.MediaKey != "" || o.Kind == "image" {
				layoutHasImageChrome = true
			}
		}
	}
	if layoutHasImageChrome && !hasAssetRef {
		t.Fatalf("a layout carries image chrome but no archetype markup references an asset; inherited chrome not captured")
	}
}
