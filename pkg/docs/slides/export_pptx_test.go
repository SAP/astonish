package slides

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
)

func TestPPTXSpikeProducesNativeObjects(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	scene := SceneGraph{SchemaVersion: SchemaV1, Title: "Native spike", Slides: []Slide{{
		ID: "spike", Notes: "Speaker note", Nodes: []Node{
			{ID: "title", Type: "text", Geometry: Geometry{X: 96, Y: 60, W: 1728, H: 100}, Text: "Editable title", Props: map[string]any{"fontSize": 32, "bold": true}},
			{ID: "box", Type: "shape", Geometry: Geometry{X: 96, Y: 200, W: 400, H: 180}, Props: map[string]any{"kind": "roundRect", "fill": "DBEAFE"}},
			{ID: "table", Type: "table", Geometry: Geometry{X: 96, Y: 440, W: 760, H: 400}, Table: &TableData{Rows: [][]string{{"Service", "Risk"}, {"Checkout", "High"}}}},
			{ID: "chart", Type: "chart", Geometry: Geometry{X: 1000, Y: 220, W: 720, H: 560}, Props: map[string]any{"kind": "bar"}, Series: []ChartSeries{{Name: "Risk", Labels: []string{"Checkout", "Identity"}, Values: []float64{95, 70}}}},
		},
	}}}
	result, err := exporter.Export(context.Background(), scene, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Capabilities.Native != 4 || result.Capabilities.Raster != 0 || result.Capabilities.Unsupported != 0 {
		t.Fatalf("unexpected capabilities: %+v", result.Capabilities)
	}
	zr, err := zip.NewReader(bytes.NewReader(result.Bytes), int64(len(result.Bytes)))
	if err != nil {
		t.Fatalf("invalid pptx zip: %v", err)
	}
	parts := map[string]string{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, ".xml") {
			r, openErr := f.Open()
			if openErr != nil {
				t.Fatal(openErr)
			}
			b, readErr := io.ReadAll(r)
			_ = r.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			parts[f.Name] = string(b)
		}
	}
	slideXML := parts["ppt/slides/slide1.xml"]
	for _, marker := range []string{"Editable title", "<a:tbl>", "<c:chart"} {
		if !strings.Contains(slideXML, marker) {
			t.Errorf("slide XML missing native marker %q", marker)
		}
	}
	if strings.Contains(slideXML, "<p:pic>") {
		t.Error("default editable spike unexpectedly contains a picture")
	}
	if _, ok := parts["ppt/notesSlides/notesSlide1.xml"]; !ok {
		t.Error("speaker notes part missing")
	}
}
