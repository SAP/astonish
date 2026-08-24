package pptxworker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// repoRoot resolves the repository root relative to this test file (…/pkg/docs/slides/pptxworker).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
}

// nodeAvailable reports whether node and the required node_modules are present.
// The node-dependent tests skip cleanly when they are not.
func requireNodeEnv(t *testing.T) (workingDir, importScript, exportScript string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping node-dependent test")
	}
	repo := repoRoot(t)
	workingDir = filepath.Join(repo, "web")
	for _, mod := range []string{"jszip", "fast-xml-parser"} {
		if _, err := os.Stat(filepath.Join(workingDir, "node_modules", mod)); err != nil {
			t.Skipf("web/node_modules/%s missing; run `cd web && npm install jszip fast-xml-parser`", mod)
		}
	}
	if _, err := os.Stat(filepath.Join(workingDir, "node_modules", "pptxgenjs")); err != nil {
		t.Skip("web/node_modules/pptxgenjs missing; cannot generate fixture pptx")
	}
	importScript = filepath.Join(repo, "pkg/docs/slides/pptxworker/import_worker.mjs")
	exportScript = filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs")
	return workingDir, importScript, exportScript
}

// buildFixturePPTX exercises the existing export worker to produce a real .pptx
// from a small ASD v2 scene, returning base64.
func buildFixturePPTX(t *testing.T, workingDir, exportScript string, scene map[string]any) string {
	t.Helper()
	sceneJSON, err := json.Marshal(scene)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (Runner{WorkingDir: workingDir, ScriptPath: exportScript, Timeout: 30 * time.Second}).
		Run(context.Background(), Request{ProtocolVersion: ProtocolVersion, Scene: sceneJSON})
	if err != nil {
		t.Fatalf("export worker failed: %v", err)
	}
	if resp.PPTXBase64 == "" {
		t.Fatal("export worker returned empty pptx")
	}
	return resp.PPTXBase64
}

func TestImportRunnerRejectsProtocolMismatch(t *testing.T) {
	t.Parallel()
	_, err := (ImportRunner{}).Run(context.Background(), ImportRequest{ProtocolVersion: 99})
	if err == nil {
		t.Fatal("expected protocol error")
	}
}

func TestImportRunnerRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := filepath.Join(dir, "import.mjs")
	stub := `let s=""; for await (const c of process.stdin) s+=c; const r=JSON.parse(s); process.stdout.write(JSON.stringify({protocolVersion:r.protocolVersion,sceneOrTemplate:{schemaVersion:2,slides:[]},warnings:["ok"]}));`
	if err := os.WriteFile(script, []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := (ImportRunner{WorkingDir: dir, ScriptPath: script, Timeout: 5 * time.Second}).
		Run(context.Background(), ImportRequest{ProtocolVersion: ImportProtocolVersion, PPTXBase64: "UEs=", Mode: "deck"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0] != "ok" {
		t.Fatalf("unexpected warnings: %+v", resp.Warnings)
	}
	var scene map[string]any
	if err := json.Unmarshal(resp.SceneOrTemplate, &scene); err != nil {
		t.Fatalf("bad sceneOrTemplate: %v", err)
	}
	if scene["schemaVersion"].(float64) != 2 {
		t.Fatalf("expected schemaVersion 2, got %v", scene["schemaVersion"])
	}
}

func TestImportDeckRoundTrip(t *testing.T) {
	workingDir, importScript, exportScript := requireNodeEnv(t)

	// 1x1 transparent PNG data URL for the image node.
	pngData := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	scene := map[string]any{
		"schemaVersion": 2,
		"title":         "Import fixture",
		"theme":         map[string]any{"surface": "FFFFFF", "ink": "172033", "accent": "2563EB"},
		"slides": []any{
			map[string]any{
				"id": "s1",
				"nodes": []any{
					map[string]any{
						"id": "title", "type": "text",
						"geometry": map[string]any{"x": 160, "y": 80, "w": 1600, "h": 120},
						"runs":     []any{map[string]any{"text": "Hello world", "bold": true, "color": "#172033", "size": 40}},
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
	b64 := buildFixturePPTX(t, workingDir, exportScript, scene)

	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: b64, Mode: "deck"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}

	var out struct {
		SchemaVersion int               `json:"schemaVersion"`
		Theme         map[string]string `json:"theme"`
		Slides        []struct {
			Nodes []struct {
				Type     string                   `json:"type"`
				Geometry struct{ X, Y, W, H int } `json:"geometry"`
				Props    map[string]any           `json:"props"`
			} `json:"nodes"`
		} `json:"slides"`
		Assets map[string]string `json:"assets"`
	}
	if err := json.Unmarshal(resp.SceneOrTemplate, &out); err != nil {
		t.Fatalf("bad sceneOrTemplate: %v\n%s", err, string(resp.SceneOrTemplate))
	}
	if out.SchemaVersion != 2 {
		t.Fatalf("expected schemaVersion 2, got %d", out.SchemaVersion)
	}
	if out.Theme["surface"] == "" || out.Theme["ink"] == "" {
		t.Fatalf("theme missing surface/ink tokens: %+v", out.Theme)
	}
	if len(out.Slides) == 0 {
		t.Fatal("expected at least one slide")
	}
	inCanvas := false
	for _, s := range out.Slides {
		for _, n := range s.Nodes {
			g := n.Geometry
			if g.X >= 0 && g.Y >= 0 && g.X+g.W <= 1920 && g.Y+g.H <= 1080 && g.W > 0 && g.H > 0 {
				inCanvas = true
			}
		}
	}
	if !inCanvas {
		t.Fatal("expected at least one node with in-canvas geometry")
	}
	if len(out.Assets) == 0 {
		t.Fatalf("expected at least one extracted asset, got none")
	}
}

func TestImportTemplateMode(t *testing.T) {
	workingDir, importScript, exportScript := requireNodeEnv(t)

	scene := map[string]any{
		"schemaVersion": 2,
		"title":         "Template fixture",
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
	b64 := buildFixturePPTX(t, workingDir, exportScript, scene)

	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: b64, Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl struct {
		Schema     int               `json:"schema"`
		Tokens     map[string]string `json:"tokens"`
		Archetypes []struct {
			Kind      string   `json:"kind"`
			Title     string   `json:"title"`
			Markup    string   `json:"markup"`
			Tier      string   `json:"tier"`
			FillSlots []string `json:"fillSlots"`
		} `json:"archetypes"`
		TemplateModel struct {
			Schema  int `json:"schema"`
			Layouts []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Objects []struct {
					Kind     string `json:"kind"`
					MediaKey string `json:"mediaKey"`
				} `json:"objects"`
				Placeholders []struct {
					Type string `json:"type"`
				} `json:"placeholders"`
			} `json:"layouts"`
			Slides []struct {
				ID string `json:"id"`
			} `json:"slides"`
		} `json:"templateModel"`
	}
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v\n%s", err, string(resp.SceneOrTemplate))
	}
	if tmpl.Schema != 2 {
		t.Fatalf("expected schema 2, got %d", tmpl.Schema)
	}
	if len(tmpl.Archetypes) < 3 {
		t.Fatalf("expected >=3 archetypes, got %d", len(tmpl.Archetypes))
	}
	kinds := map[string]bool{}
	for _, a := range tmpl.Archetypes {
		kinds[a.Kind] = true
		if a.Markup == "" {
			t.Fatalf("archetype %q has empty markup", a.Kind)
		}
	}
	// The STABLE brand-chrome set is guaranteed on every imported template:
	// title (cover), section (divider), agenda, and closing (thank-you/end),
	// plus a flexible content role. Compare on the base kind (strip any -N
	// variant suffix) since a role may be present as title-2 etc.
	baseKinds := map[string]bool{}
	for k := range kinds {
		baseKinds[stripVariantSuffix(k)] = true
	}
	for _, want := range []string{"title", "section", "agenda", "closing", "content"} {
		if !baseKinds[want] {
			t.Fatalf("missing guaranteed archetype role %q; got %+v", want, baseKinds)
		}
	}
	// The lossless IR must be present and carry at least one layout. (The
	// synthetic fixture has few real chrome objects, so we assert the IR
	// structure exists rather than requiring a specific chrome object here; the
	// slides-package import_fidelity_test covers chrome preservation + ASD
	// validity on richer fixtures.)
	if tmpl.TemplateModel.Schema != 3 {
		t.Fatalf("expected templateModel.schema 3, got %d", tmpl.TemplateModel.Schema)
	}
	if len(tmpl.TemplateModel.Layouts) < 1 {
		t.Fatalf("expected >=1 IR layout, got %d", len(tmpl.TemplateModel.Layouts))
	}
	// The generic white slab that used to back a missing chrome role (a full
	// canvas #FFFFFF rect with only ph-title/ph-body text and no other chrome).
	// A chrome archetype must NEVER be this: it must either alias a real branded
	// layout (non-empty layout-name label + its chrome) or be synthesized in the
	// template's own style (master chrome + tokens). We assert each chrome
	// archetype carries brand chrome — a real layout name label AND markup that
	// references an asset (logo/photo) OR contains a non-white fill — rather than
	// being an empty white slate.
	for _, a := range tmpl.Archetypes {
		base := stripVariantSuffix(a.Kind)
		// (a) Every archetype has a non-empty human label (its layout name /
		// synthesized role name) so variants are selectable by label.
		if strings.TrimSpace(a.Title) == "" {
			t.Fatalf("archetype %q has empty label (title); variants must be labeled", a.Kind)
		}
		// (b) Example-* archetypes are never generated (they were white).
		if strings.HasPrefix(a.Kind, "example") {
			t.Fatalf("unexpected example-* archetype %q", a.Kind)
		}
		// (c) Tier is always fixed|flexible; chrome roles are fixed with slots.
		if a.Tier != "fixed" && a.Tier != "flexible" {
			t.Fatalf("archetype %q has invalid tier %q (want fixed|flexible)", a.Kind, a.Tier)
		}
		isChrome := base == "title" || base == "section" || base == "agenda" || base == "closing"
		if isChrome {
			if a.Tier != "fixed" {
				t.Fatalf("chrome archetype %q must be tier=fixed, got %q", a.Kind, a.Tier)
			}
			if len(a.FillSlots) == 0 {
				t.Fatalf("chrome archetype %q must carry fillSlots", a.Kind)
			}
			// (d) Not the old generic white slab: it must reference a brand asset
			// or carry a non-#FFFFFF fill (accent bar / colored bg / logo).
			hasBrandChrome := strings.Contains(a.Markup, "asset-ref=") ||
				hasNonWhiteFill(a.Markup)
			if !hasBrandChrome {
				t.Fatalf("chrome archetype %q (%s) has no brand chrome (no asset-ref and only white fills); looks like a generic white slab:\n%s", a.Kind, a.Title, a.Markup)
			}
			// (e) Its markup must actually contain each declared fill slot id.
			for _, id := range a.FillSlots {
				if !strings.Contains(a.Markup, `id="`+id+`"`) {
					t.Fatalf("chrome archetype %q declares fillSlot %q not present in markup:\n%s", a.Kind, id, a.Markup)
				}
			}
		}
	}
}

// TestImportScalesFooterFontSize pins the footer-font-scale fix: imported run
// font sizes are scaled to the ASD canvas by the same `scale` as geometry, so a
// small footer (e.g. sz="1000" = 10pt on a 1280x720 slide, scale 1.5) renders at
// ~15, not 10. The scaling lives in the importer's styleOf; we exercise it end to
// end against the real reference corporate template when it is available locally
// (it is not committed to the repo, so the test skips cleanly in CI).
func TestImportScalesFooterFontSize(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	ref := "/Users/I851355/Downloads/2026 GCO IPED PPT TEMPLATE.pptx"
	raw, err := os.ReadFile(ref)
	if err != nil {
		t.Skipf("reference template not present (%v); skipping footer-scale smoke", err)
	}
	b64 := base64.StdEncoding.EncodeToString(raw)
	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 60 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: b64, Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl struct {
		Archetypes []struct {
			Markup string `json:"markup"`
		} `json:"archetypes"`
	}
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v", err)
	}
	// Find the footer ast-text ("INTERNAL ...") and assert its size scaled up.
	// Go's RE2 has no lookahead, so match the opening ast-text tag then confirm
	// the INTERNAL text follows before the element closes.
	tagRe := regexp.MustCompile(`<ast-text[^>]*\bsize="(\d+)"[^>]*>`)
	found := false
	maxSize := 0
	for _, a := range tmpl.Archetypes {
		markup := a.Markup
		for {
			idx := strings.Index(markup, "INTERNAL")
			if idx < 0 {
				break
			}
			prefix := markup[:idx]
			open := strings.LastIndex(prefix, "<ast-text")
			markup = markup[idx+len("INTERNAL"):]
			if open < 0 {
				continue
			}
			tagEnd := strings.Index(prefix[open:], ">")
			if tagEnd < 0 {
				continue
			}
			tag := prefix[open : open+tagEnd+1]
			m := tagRe.FindStringSubmatch(tag)
			if m == nil {
				t.Fatalf("footer ast-text has no size attribute: %s", tag)
			}
			found = true
			size, _ := strconv.Atoi(m[1])
			if size > maxSize {
				maxSize = size
			}
			// The unscaled raw point values (10pt / 6pt) must never be emitted
			// verbatim — every footer size is scaled by the canvas `scale`.
			if size == 10 || size == 6 {
				t.Fatalf("footer size %d is the raw unscaled point value; canvas scale was not applied\n%s", size, tag)
			}
			if !strings.Contains(tag, `font="72 Brand`) {
				t.Fatalf("footer lost its brand font; expected font starting \"72 Brand\" in %s", tag)
			}
			if !strings.Contains(tag, "sans-serif") {
				t.Fatalf("footer font missing web-safe sans-serif fallback in %s", tag)
			}
		}
	}
	if !found {
		t.Fatal("did not find the INTERNAL footer ast-text in any archetype")
	}
	// The primary footer (10pt) must render at ~15 after the 1.5x canvas scale.
	if maxSize < 13 {
		t.Fatalf("primary footer size %d not scaled to canvas (want ~15)", maxSize)
	}
}

// stripVariantSuffix removes a trailing -N variant suffix (title-2 -> title).
func stripVariantSuffix(kind string) string {
	if i := strings.LastIndexByte(kind, '-'); i > 0 {
		if _, err := strconv.Atoi(kind[i+1:]); err == nil {
			return kind[:i]
		}
	}
	return kind
}

// hasNonWhiteFill reports whether the markup contains a fill= attribute whose
// color is not white (#FFFFFF / #FFF), i.e. real brand color chrome.
func hasNonWhiteFill(markup string) bool {
	for _, m := range fillAttrRe.FindAllStringSubmatch(markup, -1) {
		c := strings.ToUpper(strings.TrimSpace(m[1]))
		if c != "#FFFFFF" && c != "#FFF" && c != "#FFFFFFFF" {
			return true
		}
	}
	return false
}

var fillAttrRe = regexp.MustCompile(`fill="([^"]+)"`)

func TestImportGarbageRejected(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	garbage := base64.StdEncoding.EncodeToString([]byte("not a zip at all"))
	_, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 15 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: garbage, Mode: "deck"})
	if err == nil {
		t.Fatal("expected error for garbage (non-zip) input")
	}
}
