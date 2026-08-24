package slides

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func exportTestScene() SceneGraph {
	return SceneGraph{SchemaVersion: SchemaV1, Title: `Roadmap <Q4>`, Slides: []Slide{{
		ID: "intro", Title: `Intro "safe"`, Notes: "speaker <notes>",
		Nodes: []Node{{ID: "title", Type: "text", Geometry: Geometry{X: 10, Y: 20, W: 300, H: 80}, Text: `<script>alert(1)</script>`, Props: map[string]any{"font-token": "display", "onclick": "bad"}}},
	}}}
}

func TestHTMLExporterProducesSelfContainedEscapedDocument(t *testing.T) {
	result, err := (HTMLExporter{RuntimeJS: []byte(`window.runtimeReady=true`)}).Export(exportTestScene())
	if err != nil {
		t.Fatal(err)
	}
	doc := string(result.Bytes)
	for _, want := range []string{"<!doctype html>", "Content-Security-Policy", "sha256-", "html,body{width:100%;height:100%;margin:0;overflow:hidden}", "ast-deck{display:block;position:relative;width:1920px;height:1080px", "ast-slide[active]{display:block}", `<ast-deck schema="1" ratio="16:9">`, `<ast-slide id="intro"`, `<ast-text id="title" x="10" y="20" w="300" h="80" font-token="display">`, "&lt;script&gt;alert(1)&lt;/script&gt;", "window.runtimeReady=true"} {
		if !strings.Contains(doc, want) {
			t.Errorf("document missing %q", want)
		}
	}
	if strings.Contains(doc, "red;}body") {
		t.Fatal("unsafe theme value reached export")
	}
	if strings.Contains(doc, "onclick=") {
		t.Fatal("event handler property reached export")
	}
	if strings.Contains(doc, `<script>alert(1)</script>`) {
		t.Fatal("text content was not escaped")
	}
}

func TestHTMLExporterRendersV2FidelityAttributes(t *testing.T) {
	scene := SceneGraph{SchemaVersion: SchemaV2, Title: "V2 Deck", Slides: []Slide{{
		ID: "cover",
		Nodes: []Node{
			{
				ID: "grad-box", Type: "shape",
				Geometry: Geometry{X: 100, Y: 100, W: 400, H: 300},
				Geom:     "rect",
				Rot:      15,
				Opacity:  0.5,
				Gradient: &Gradient{Kind: "linear", Angle: 90, Stops: []GradientStop{
					{Pos: 0, Color: "#ff0000"},
					{Pos: 100, Color: "#0000ff"},
				}},
			},
			{
				ID: "custom", Type: "shape",
				Geometry: Geometry{X: 600, Y: 100, W: 200, H: 200},
				Path:     "M 0 0 L 100 0 L 50 100 Z",
				Fill:     "#00ff00",
			},
			{
				ID: "rich", Type: "text",
				Geometry: Geometry{X: 100, Y: 500, W: 800, H: 120},
				Runs: []TextRun{
					{Text: "Hello ", Bold: true, Color: "#ff0000"},
					{Text: "world", Italic: true, Underline: true, Size: 48},
				},
			},
		},
	}}}
	result, err := (HTMLExporter{RuntimeJS: []byte(`window.runtimeReady=true`)}).Export(scene)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(result.Bytes)
	for _, want := range []string{
		"<linearGradient",
		`<stop offset="0%"`,
		`stop-color="#ff0000"`,
		"rotate(15deg)",
		`d="M 0 0 L 100 0 L 50 100 Z"`,
		"<span",
		"font-weight:700",
		"color:#ff0000",
		"font-style:italic",
		"text-decoration:underline",
		"font-size:48px",
		"opacity:0.5",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("v2 document missing %q", want)
		}
	}
	if strings.Count(doc, "<span") < 2 {
		t.Errorf("expected at least two run spans, got %d", strings.Count(doc, "<span"))
	}
	if !strings.Contains(doc, "#ff0000") {
		t.Fatal("raw color did not reach export")
	}
}

func TestExportHTML_TextWhitespacePreWrap(t *testing.T) {
	// A run whose text contains a newline must survive into the emitted span,
	// and the ast-text element must carry white-space:pre-wrap so the browser
	// renders that newline as a line break instead of collapsing it.
	scene := SceneGraph{SchemaVersion: SchemaV2, Title: "Whitespace Deck", Slides: []Slide{{
		ID: "s",
		Nodes: []Node{
			{
				ID: "rich", Type: "text",
				Geometry: Geometry{X: 100, Y: 100, W: 800, H: 400},
				Runs: []TextRun{
					{Text: "Line one", Bold: true},
					{Text: "\n\n"},
					{Text: "Line two", Bold: true},
				},
			},
		},
	}}}
	result, err := (HTMLExporter{RuntimeJS: []byte(`window.runtimeReady=true`)}).Export(scene)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(result.Bytes)
	if !strings.Contains(doc, "white-space:pre-wrap") {
		t.Fatal("ast-text white-space:pre-wrap CSS rule missing from export")
	}
	if !strings.Contains(doc, "ast-text{white-space:pre-wrap") {
		t.Fatal("ast-text CSS rule not applied to the ast-text element")
	}
	// The separator run's newline must be present in the emitted markup.
	if !strings.Contains(doc, "\n\n") {
		t.Fatal("run newline was not preserved in exported HTML")
	}
}

func TestHTMLExporterPrintModePaginatesOneSlidePerPage(t *testing.T) {
	// A two-slide deck. In print mode the export must emit page-break CSS so
	// Chrome produces one page per slide (not one overlapping page), and an
	// @page box matching the 1920x1080 canvas so nothing is scaled or cropped.
	scene := exportTestScene()
	scene.Slides = append(scene.Slides, Slide{
		ID:    "second",
		Nodes: []Node{{ID: "t2", Type: "text", Geometry: Geometry{X: 0, Y: 0, W: 100, H: 40}, Text: "two"}},
	})

	printDoc := mustExport(t, HTMLExporter{RuntimeJS: []byte("window.runtimeReady=true"), Print: true}, scene)
	for _, want := range []string{
		"@page{size:20in 11.25in;margin:0}",
		"ast-slide{display:block;position:relative",
		"width:20in;height:11.25in",
		"break-inside:avoid",
		"break-after:page",
		"page-break-after:always",
		"ast-slide:last-of-type{break-after:auto",
	} {
		if !strings.Contains(printDoc, want) {
			t.Errorf("print document missing %q", want)
		}
	}

	// The screen (non-print) document must NOT carry the print pagination CSS;
	// on screen slides overlap and are toggled by the runtime.
	screenDoc := mustExport(t, HTMLExporter{RuntimeJS: []byte("window.runtimeReady=true")}, scene)
	for _, unwanted := range []string{"@page{size:20in 11.25in", "break-after:page"} {
		if strings.Contains(screenDoc, unwanted) {
			t.Errorf("screen document unexpectedly contains print CSS %q", unwanted)
		}
	}
}

func mustExport(t *testing.T, e HTMLExporter, scene SceneGraph) string {
	t.Helper()
	result, err := e.Export(scene)
	if err != nil {
		t.Fatal(err)
	}
	return string(result.Bytes)
}

func TestHTMLExporterCSPHashesExactEmbeddedRuntime(t *testing.T) {
	runtimeJS := []byte(`window.template="</script>";window.runtimeReady=true`)
	result, err := (HTMLExporter{RuntimeJS: runtimeJS}).Export(exportTestScene())
	if err != nil {
		t.Fatal(err)
	}

	doc := string(result.Bytes)
	const scriptStart = `<script>`
	const scriptEnd = `</script></body>`
	start := strings.LastIndex(doc, scriptStart)
	end := strings.LastIndex(doc, scriptEnd)
	if start < 0 || end <= start {
		t.Fatal("exported runtime script not found")
	}
	embeddedRuntime := []byte(doc[start+len(scriptStart) : end])
	if strings.Contains(string(embeddedRuntime), `</script>`) {
		t.Fatal("embedded runtime contains an unescaped closing script tag")
	}

	hash := sha256.Sum256(embeddedRuntime)
	want := "'sha256-" + base64.StdEncoding.EncodeToString(hash[:]) + "'"
	if !strings.Contains(doc, want) {
		t.Fatalf("CSP does not authorize the exact embedded runtime; want %s", want)
	}
}

func TestHTMLExporterRejectsInvalidInput(t *testing.T) {
	if _, err := (HTMLExporter{RuntimeJS: []byte("runtime")}).Export(SceneGraph{}); err == nil {
	}
	if _, err := (HTMLExporter{}).Export(exportTestScene()); err == nil {
		t.Fatal("expected runtime error")
	}
	scene := exportTestScene()
	scene.Slides[0].ID = ""
	if _, err := (HTMLExporter{RuntimeJS: []byte("runtime")}).Export(scene); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestHTMLExporterRendersFullCanvasImageBackground(t *testing.T) {
	scene := SceneGraph{SchemaVersion: SchemaV2, Title: "Image BG Deck", Slides: []Slide{{
		ID: "cover",
		Nodes: []Node{
			{
				ID:       "bg",
				Type:     "image",
				Geometry: Geometry{X: 0, Y: 0, W: 1920, H: 1080},
				Props: map[string]any{
					"asset-ref":  "sha256-deadbeef",
					"decorative": "true",
				},
			},
		},
	}}}
	doc := mustExport(t, HTMLExporter{RuntimeJS: []byte(`window.runtimeReady=true`)}, scene)
	if !strings.Contains(doc, "<ast-image") {
		t.Fatalf("expected an ast-image element in export, got:\n%s", doc)
	}
	// The full-canvas background must carry the asset reference and cover the
	// entire 1920x1080 canvas.
	for _, want := range []string{
		`asset-ref="sha256-deadbeef"`,
		`x="0"`,
		`y="0"`,
		`w="1920"`,
		`h="1080"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("full-canvas image export missing %q", want)
		}
	}
}

// TestHTMLExporterResolvesAssetRefToDataURL verifies that an ast-image whose
// asset-ref is present in the deck asset map is rendered with a concrete data:
// URL `src` (the Lit runtime only reads `src`), while the original asset-ref is
// preserved. An unresolvable ref must NOT gain a src. This is the fix for
// imported-template logos rendering as broken images.
func TestHTMLExporterResolvesAssetRefToDataURL(t *testing.T) {
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	scene := SceneGraph{
		SchemaVersion: SchemaV3,
		Title:         "Imported",
		Assets:        map[string]string{"sha256-logo": png},
		Slides: []Slide{{
			ID: "cover",
			Nodes: []Node{
				{ID: "logo", Type: "image", Geometry: Geometry{X: 40, Y: 40, W: 200, H: 100}, Props: map[string]any{"asset-ref": "sha256-logo"}},
				{ID: "missing", Type: "image", Geometry: Geometry{X: 40, Y: 200, W: 200, H: 100}, Props: map[string]any{"asset-ref": "sha256-absent"}},
			},
		}},
	}
	doc := mustExport(t, HTMLExporter{RuntimeJS: []byte(`window.runtimeReady=true`)}, scene)
	if !strings.Contains(doc, `src="`+png+`"`) {
		t.Errorf("resolved asset-ref did not produce a data: src\n%s", doc)
	}
	if !strings.Contains(doc, `asset-ref="sha256-logo"`) {
		t.Errorf("asset-ref should be preserved alongside src")
	}
	// The unresolvable ref keeps its asset-ref but must not gain a src pointing
	// at the wrong/other asset. There should be exactly one src in the doc.
	if strings.Count(doc, "src=") != 1 {
		t.Errorf("expected exactly one resolved src, got %d\n%s", strings.Count(doc, "src="), doc)
	}
}
