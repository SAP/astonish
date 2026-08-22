package slides

import (
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
	for _, want := range []string{"<!doctype html>", "Content-Security-Policy", "sha256-", `<ast-deck schema="1" ratio="16:9">`, `<ast-slide id="intro"`, `<ast-text id="title" x="10" y="20" w="300" h="80" font-token="display">`, "&lt;script&gt;alert(1)&lt;/script&gt;", "window.runtimeReady=true"} {
		if !strings.Contains(doc, want) {
			t.Errorf("document missing %q", want)
		}
	}
	if strings.Contains(doc, "onclick=") {
		t.Fatal("event handler property reached export")
	}
	if strings.Contains(doc, `<script>alert(1)</script>`) {
		t.Fatal("text content was not escaped")
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
