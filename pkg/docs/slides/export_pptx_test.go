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
			{ID: "title", Type: "text", Geometry: Geometry{X: 96, Y: 60, W: 1728, H: 100}, Text: "Editable title", Props: map[string]any{"size": "96", "weight": "700", "align": "center", "anchor": "ctr", "color-token": "ink"}},
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
	// Text fidelity: the centered, bold title must produce centered paragraph
	// alignment, a bold run, and a centered vertical text-body anchor. These are
	// the DrawingML tokens pptxgenjs emits for align:'center', bold:true and
	// valign:'middle'.
	for _, marker := range []string{`algn="ctr"`, `b="1"`, `anchor="ctr"`} {
		if !strings.Contains(slideXML, marker) {
			t.Errorf("slide XML missing text-fidelity marker %q", marker)
		}
	}
	if strings.Contains(slideXML, "<p:pic>") {
		t.Error("default editable spike unexpectedly contains a picture")
	}
	if _, ok := parts["ppt/notesSlides/notesSlide1.xml"]; !ok {
		t.Error("speaker notes part missing")
	}
}

func TestPPTXTextHonorsAlignmentAndSize(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	scene := SceneGraph{SchemaVersion: SchemaV2, Title: "Alignment", Slides: []Slide{{
		ID: "align", Nodes: []Node{
			{ID: "sub", Type: "text", Geometry: Geometry{X: 96, Y: 800, W: 1728, H: 120}, Text: "Subtitle", Props: map[string]any{"size": "48", "weight": "400", "align": "right", "anchor": "b", "color": "#F59E0B"}},
		},
	}}}
	result, err := exporter.Export(context.Background(), scene, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Capabilities.Native != 1 {
		t.Fatalf("unexpected capabilities: %+v", result.Capabilities)
	}
	zr, err := zip.NewReader(bytes.NewReader(result.Bytes), int64(len(result.Bytes)))
	if err != nil {
		t.Fatalf("invalid pptx zip: %v", err)
	}
	var slideXML string
	for _, f := range zr.File {
		if f.Name == "ppt/slides/slide1.xml" {
			r, openErr := f.Open()
			if openErr != nil {
				t.Fatal(openErr)
			}
			b, readErr := io.ReadAll(r)
			_ = r.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			slideXML = string(b)
		}
	}
	// align:'right' -> algn="r"; anchor:'b' -> vertical anchor="b"; the raw
	// amber color is emitted as a solid fill srgbClr val="F59E0B".
	for _, marker := range []string{`algn="r"`, `anchor="b"`, "F59E0B"} {
		if !strings.Contains(slideXML, marker) {
			t.Errorf("slide XML missing marker %q", marker)
		}
	}
}

func TestPPTXExportV2NodeProps(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	scene := SceneGraph{SchemaVersion: SchemaV2, Title: "V2 fidelity", Slides: []Slide{{
		ID: "v2", Notes: "Speaker note", Nodes: []Node{
			{
				ID: "grad", Type: "shape", Geometry: Geometry{X: 96, Y: 200, W: 400, H: 180},
				Rot: 15, Geom: "roundRect", Dash: "dash", Opacity: 0.5,
				Gradient: &Gradient{Kind: "linear", Angle: 45, Stops: []GradientStop{
					{Pos: 0, Color: "#DBEAFE"}, {Pos: 100, Color: "#2563EB"},
				}},
			},
			{
				ID: "runs", Type: "text", Geometry: Geometry{X: 96, Y: 60, W: 1728, H: 100}, Rot: 5,
				Runs: []TextRun{
					{Text: "Bold ", Bold: true, Color: "#111827", Font: "Aptos", Size: 32},
					{Text: "italic ", Italic: true, Color: "#2563EB"},
					{Text: "underline", Underline: true},
				},
			},
		},
	}}}
	result, err := exporter.Export(context.Background(), scene, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Capabilities.Native != 2 || result.Capabilities.Raster != 0 || result.Capabilities.Unsupported != 0 {
		t.Fatalf("unexpected capabilities: %+v", result.Capabilities)
	}
	if _, err := zip.NewReader(bytes.NewReader(result.Bytes), int64(len(result.Bytes))); err != nil {
		t.Fatalf("invalid pptx zip: %v", err)
	}
	// Gradient is approximated as a solid fill; expect a diagnostic for it.
	foundGradientWarning := false
	for _, d := range result.Diagnostics {
		if strings.Contains(strings.ToLower(d.Message), "gradient") {
			foundGradientWarning = true
		}
	}
	if !foundGradientWarning {
		t.Errorf("expected gradient approximation warning, got diagnostics: %+v", result.Diagnostics)
	}
}
