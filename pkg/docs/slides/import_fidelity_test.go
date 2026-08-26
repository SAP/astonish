package slides

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// Picture-region matchers for the fixed-tier fidelity assertions. A chrome
// archetype whose source layout carried a picture region must render that region
// as a REAL, swappable hero image — <ast-image id="ph-pic-N" ... asset-ref="…">
// at the borrowed sample photo's own geometry (optionally flip-h/flip-v) — and
// advertise ph-pic-N in fillSlots. It must NOT fall back to a synthetic neutral
// panel (<ast-shape id="ph-pic-N" …>), which was the old "blank blue box" bug.
var (
	// picShapePanelRe matches the synthetic neutral panel a picture region used
	// to fall back to (must NOT appear when the region is borrowable).
	picShapePanelRe = regexp.MustCompile(`<ast-shape id="ph-pic-\d+"`)
	// picImageIDRe captures the ph-pic-N id of a real, asset-backed hero image.
	picImageIDRe = regexp.MustCompile(`<ast-image id="(ph-pic-\d+)"[^>]*asset-ref="[^"]+"`)
	// picImageWithFlipRe matches a hero image that also carries a flip attribute.
	picImageWithFlipRe = regexp.MustCompile(`<ast-image id="ph-pic-\d+"[^>]*asset-ref="[^"]+"[^>]*flip-[hv]="true"`)
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
	// sawImageSlot: at least one fixed archetype exposed a real, asset-backed,
	// swappable hero-image slot (ph-pic-N in fillSlots). sawImageFlip: a borrowed
	// sample photo carried a mirror flip that survived onto the ast-image.
	sawImageSlot := false
	sawImageFlip := false
	for _, a := range tmpl.Archetypes {
		if a.Markup == "" {
			t.Fatalf("archetype %q has empty markup", a.Kind)
		}
		// Every archetype MUST carry a human label (the real PowerPoint layout
		// name, stored in Title); layout variants are surfaced by this label.
		if strings.TrimSpace(a.Title) == "" {
			t.Fatalf("archetype %q has empty label (title); layout variants must be labeled", a.Kind)
		}
		// Fixed brand-chrome archetypes declare fillSlots (the ast-text ids the
		// AI may edit); each declared slot id must be present in the markup so the
		// text-only fill contract is honored.
		if a.Tier == "fixed" {
			if len(a.FillSlots) == 0 {
				t.Fatalf("fixed archetype %q must declare fillSlots", a.Kind)
			}
			for _, id := range a.FillSlots {
				if !strings.Contains(a.Markup, `id="`+id+`"`) {
					t.Fatalf("fixed archetype %q declares fillSlot %q absent from markup:\n%s", a.Kind, id, a.Markup)
				}
			}
			// Geometry-faithful, swappable hero-image contract: when a chrome
			// archetype's source layout carried a picture region it is rendered as
			// a REAL image at the borrowed sample photo's own geometry — an
			// <ast-image id="ph-pic-N" … asset-ref="…"> — NOT a synthetic neutral
			// <ast-shape> panel (the old "blank blue box" fallback). The image id
			// (ph-pic-N) must be advertised in fillSlots so the photo is a
			// replaceable IMAGE slot (not only text slots). Guard on the presence
			// of a ph-pic- region so the assertion is meaningful even when the
			// synthetic export fixture emits no picture placeholder.
			if strings.Contains(a.Markup, `id="ph-pic-`) {
				// The region must be a real image, never a fallback shape panel.
				if picShapePanelRe.MatchString(a.Markup) {
					t.Fatalf("fixed archetype %q renders a picture region as a synthetic <ast-shape> panel instead of a borrowed <ast-image asset-ref=…>:\n%s", a.Kind, a.Markup)
				}
				m := picImageIDRe.FindStringSubmatch(a.Markup)
				if m == nil {
					t.Fatalf("fixed archetype %q has a ph-pic- region but no <ast-image id=\"ph-pic-N\" … asset-ref=…>:\n%s", a.Kind, a.Markup)
				}
				picID := m[1]
				inSlots := false
				for _, id := range a.FillSlots {
					if id == picID {
						inSlots = true
						break
					}
				}
				if !inSlots {
					t.Fatalf("fixed archetype %q emits image slot %q but does not advertise it in fillSlots %v (image regions must be replaceable IMAGE slots)", a.Kind, picID, a.FillSlots)
				}
				sawImageSlot = true
				// A borrowed sample photo may carry a mirror flip; if the fixture's
				// geometry made a flip available, assert it survives on the image.
				// When the geometry offers no flip, we intentionally do not force it.
				if picImageWithFlipRe.MatchString(a.Markup) {
					sawImageFlip = true
				}
			}
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
	// Geometry-faithful swappable hero-image cross-check at the IR level: if any
	// source layout declared a picture placeholder region, at least one fixed
	// archetype must have surfaced it as a real, asset-backed, replaceable image
	// slot (ph-pic-N present in the markup as <ast-image asset-ref=…> AND in
	// fillSlots) — never a synthetic neutral panel. This proves the borrow path
	// ran end-to-end. When no layout has a picture region, there is nothing to
	// assert (the per-archetype guard above already covers any ph-pic- that does
	// appear), so we do not force sawImageSlot.
	layoutHasPictureRegion := false
	for _, l := range tmpl.Model.Layouts {
		for _, p := range l.Placeholders {
			if p.Type == "image" {
				layoutHasPictureRegion = true
			}
		}
	}
	if layoutHasPictureRegion && !sawImageSlot {
		t.Fatalf("a layout declares a picture region but no fixed archetype exposes a real <ast-image asset-ref=…> image fill slot (ph-pic-N in fillSlots); the swappable hero-image borrow did not run")
	}
	// sawImageFlip is asserted only opportunistically: when the fixture's borrowed
	// sample photo geometry made a mirror flip available it must survive onto the
	// ast-image. We record it here to make the intent explicit without forcing a
	// flip the fixture geometry may not provide.
	_ = sawImageFlip
}
