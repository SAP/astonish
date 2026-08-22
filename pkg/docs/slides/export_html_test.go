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
		t.Fatal("expected schema error")
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
