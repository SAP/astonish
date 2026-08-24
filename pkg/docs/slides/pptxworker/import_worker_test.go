package pptxworker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
			Kind   string `json:"kind"`
			Title  string `json:"title"`
			Markup string `json:"markup"`
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
	for _, want := range []string{"title", "section", "content"} {
		if !kinds[want] {
			t.Fatalf("missing archetype %q; got %+v", want, kinds)
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
	// Every archetype MUST carry a non-empty human label (its title = the real
	// PowerPoint layout cSld @name). Layout variants are surfaced to the user/AI
	// by this label. Example-* archetypes are NO LONGER generated from thin
	// authored slides (which carried no background and rendered white); the
	// colorful, on-brand starting points are the inheritance-corrected LAYOUT
	// archetypes. templateModel.slides may still be populated for the editor.
	for _, a := range tmpl.Archetypes {
		if strings.TrimSpace(a.Title) == "" {
			t.Fatalf("archetype %q has empty label (title); layout variants must be labeled", a.Kind)
		}
		if strings.HasPrefix(a.Kind, "example") {
			t.Fatalf("unexpected example-* archetype %q; examples must not be derived from authored slides", a.Kind)
		}
	}
}

func TestImportGarbageRejected(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	garbage := base64.StdEncoding.EncodeToString([]byte("not a zip at all"))
	_, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 15 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: garbage, Mode: "deck"})
	if err == nil {
		t.Fatal("expected error for garbage (non-zip) input")
	}
}
