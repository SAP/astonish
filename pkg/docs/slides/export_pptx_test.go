package slides

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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

func TestPPTXOmitsUnfilledShapesInsteadOfWhiteTiles(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	scene := SceneGraph{SchemaVersion: SchemaV2, Title: "No white ghosts", Slides: []Slide{{
		ID: "s", Nodes: []Node{
			{ID: "bg", Type: "shape", Geometry: Geometry{X: 0, Y: 0, W: 1920, H: 1080}, Fill: "#0B1220", Geom: "rect"},
			{ID: "ghost", Type: "shape", Geometry: Geometry{X: 100, Y: 100, W: 142, H: 142}, Geom: "rect"},
			{ID: "accent", Type: "shape", Geometry: Geometry{X: 0, Y: 0, W: 12, H: 1080}, Fill: "#E76500", Geom: "rect"},
		},
	}}}
	result, err := exporter.Export(context.Background(), scene, false)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(result.Bytes), int64(len(result.Bytes)))
	if err != nil {
		t.Fatal(err)
	}
	var slideXML string
	for _, f := range zr.File {
		if f.Name == "ppt/slides/slide1.xml" {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			b, e := io.ReadAll(r)
			_ = r.Close()
			if e != nil {
				t.Fatal(e)
			}
			slideXML = string(b)
		}
	}
	if strings.Count(slideXML, "<p:sp>") != 2 {
		t.Fatalf("expected 2 shapes (bg + accent), ghost unfilled tile must be omitted; got %d in\n%s", strings.Count(slideXML, "<p:sp>"), slideXML)
	}
	if !strings.Contains(slideXML, "0B1220") || !strings.Contains(slideXML, "E76500") {
		t.Fatalf("expected authored fills; got\n%s", slideXML)
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

// TestPPTXTextHonorsShortFormAlignment guards the fix for the real ASD authoring
// forms. Decks (and the slides skill) author alignment as the short tokens
// `l | ctr | r` and vertical anchor `t | ctr | b`, which the parser passes
// through to node props verbatim. The PPTX worker's alignMap previously only
// knew the full CSS words, so align="ctr" fell through to left and centered
// titles rendered left-anchored in PowerPoint while HTML/PDF looked centered.
// This asserts a center-authored title produces algn="ctr" (not the left
// default) and anchor="ctr".
func TestPPTXTextHonorsShortFormAlignment(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	scene := SceneGraph{SchemaVersion: SchemaV2, Title: "Short align", Slides: []Slide{{
		ID: "shortalign", Nodes: []Node{
			{ID: "title", Type: "text", Geometry: Geometry{X: 160, Y: 380, W: 1600, H: 160}, Text: "The Life of Bill Gates", Props: map[string]any{"size": "84", "weight": "700", "align": "ctr", "anchor": "ctr"}},
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
	// align:"ctr" must map to centered paragraph alignment (algn="ctr"), NOT
	// the left default; anchor:"ctr" must map to a centered vertical anchor.
	if !strings.Contains(slideXML, `algn="ctr"`) {
		t.Errorf(`center-authored title missing algn="ctr" (regressed to left?): %s`, slideXML)
	}
	if strings.Contains(slideXML, `algn="l"`) {
		t.Errorf(`center-authored title must not be left-aligned: %s`, slideXML)
	}
	if !strings.Contains(slideXML, `anchor="ctr"`) {
		t.Errorf(`center-authored title missing vertical anchor="ctr": %s`, slideXML)
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

// TestPPTXShapeBorderMatchesAuthoring asserts the PPTX export only draws a
// shape outline when the deck authored one. A fill-only shape (e.g. a
// full-slide background/frame panel) must NOT gain a spurious visible
// rectangle: its outline is emitted as a fully transparent line (alpha=0). A
// shape authored with a line color keeps that visible border. This guards the
// worker.mjs `shape` case, which previously forced a 1pt 172033 border on every
// shape and produced a faint frame around the slide content.
func TestPPTXShapeBorderMatchesAuthoring(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	scene := SceneGraph{SchemaVersion: SchemaV2, Title: "Shape borders", Slides: []Slide{{
		ID: "borders", Nodes: []Node{
			// Fill-only frame: no authored line -> must have no visible border.
			{ID: "frame", Type: "shape", Geometry: Geometry{X: 40, Y: 40, W: 1840, H: 1000}, Geom: "rect", Fill: "0B1220"},
			// Authored red line -> must keep a visible border.
			{ID: "bordered", Type: "shape", Geometry: Geometry{X: 100, Y: 100, W: 200, H: 80}, Geom: "rect", Fill: "FFFFFF", Line: "FF0000"},
		},
	}}}
	result, err := exporter.Export(context.Background(), scene, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Capabilities.Native != 2 {
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
	if slideXML == "" {
		t.Fatal("slide1.xml missing from exported pptx")
	}
	// Split the two shape blocks so each assertion targets the right shape.
	shapes := strings.Split(slideXML, "<p:sp>")
	var frameSP, borderedSP string
	for _, s := range shapes {
		switch {
		case strings.Contains(s, `val="0B1220"`):
			frameSP = s
		case strings.Contains(s, `val="FF0000"`):
			borderedSP = s
		}
	}
	if frameSP == "" || borderedSP == "" {
		t.Fatalf("could not locate both shapes in slide XML: %s", slideXML)
	}
	// The fill-only frame must have a transparent (alpha=0) outline and no
	// visible line color such as the old 172033 default.
	if !strings.Contains(frameSP, `<a:alpha val="0"/>`) {
		t.Errorf("fill-only shape must have a transparent outline (alpha=0): %s", frameSP)
	}
	if strings.Contains(frameSP, `val="172033"`) {
		t.Errorf("fill-only shape must not carry the old default 172033 border: %s", frameSP)
	}
	// The authored-line shape must keep its visible red border with no alpha.
	if !strings.Contains(borderedSP, `<a:ln`) || !strings.Contains(borderedSP, `val="FF0000"`) {
		t.Errorf("authored-line shape must keep its visible border: %s", borderedSP)
	}
	// Isolate the bordered shape's <a:ln> and ensure it is not made transparent.
	if lnIdx := strings.Index(borderedSP, "<a:ln"); lnIdx >= 0 {
		if strings.Contains(borderedSP[lnIdx:], `<a:alpha val="0"/>`) {
			t.Errorf("authored-line shape border must not be transparent: %s", borderedSP)
		}
	}
}

// TestPPTXLayoutMatchesCanvasAndCenters guards the slide-layout dimensions. The
// scene canvas is 1920x1080 logical units at 160 units/inch (== 12in x 6.75in).
// The worker must define a custom layout of exactly that size instead of
// pptxgenjs LAYOUT_WIDE (13.333in x 7.5in); otherwise the 12in-wide content is
// placed on a wider slide, leaving the slack on the right/bottom and shifting a
// slide-symmetric, center-aligned title ~5% left of the true slide center. This
// asserts the presentation slide size equals the canvas AND that a symmetric
// box (x=160,w=1600 -> spans 1in..11in, center 6in) lands exactly at the slide
// horizontal center in the emitted DrawingML.
func TestPPTXLayoutMatchesCanvasAndCenters(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	scene := SceneGraph{SchemaVersion: SchemaV2, Title: "Centering", Slides: []Slide{{
		ID: "center", Nodes: []Node{
			{ID: "title", Type: "text", Geometry: Geometry{X: 160, Y: 380, W: 1600, H: 160}, Text: "Centered Title", Props: map[string]any{"size": "84", "align": "ctr", "anchor": "ctr"}},
		},
	}}}
	result, err := exporter.Export(context.Background(), scene, false)
	if err != nil {
		t.Fatal(err)
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
	// Slide size must equal the canvas: 1920/160=12in, 1080/160=6.75in in EMU
	// (1in = 914400 EMU). 12in = 10972800, 6.75in = 6172200.
	pres := parts["ppt/presentation.xml"]
	wantSldSz := `<p:sldSz cx="10972800" cy="6172200"`
	if !strings.Contains(pres, wantSldSz) {
		t.Errorf("presentation slide size must match the 12in x 6.75in canvas (%q); got: %s", wantSldSz, pres)
	}
	// The centered title box (x=160 -> 1in=914400 EMU, w=1600 -> 10in=9144000
	// EMU) must be horizontally centered on the 12in slide: box center =
	// 914400 + 9144000/2 = 5486400 == slide center (10972800/2 = 5486400).
	slideXML := parts["ppt/slides/slide1.xml"]
	off := regexp.MustCompile(`<a:off x="(\d+)" y="\d+"/><a:ext cx="(\d+)" cy="\d+"/>`)
	var found bool
	for _, m := range off.FindAllStringSubmatch(slideXML, -1) {
		x, _ := strconv.Atoi(m[1])
		cx, _ := strconv.Atoi(m[2])
		if cx == 0 { // skip the empty group-shape xfrm
			continue
		}
		found = true
		boxCenter := x + cx/2
		slideCenter := 10972800 / 2
		if boxCenter != slideCenter {
			t.Errorf("centered title box center = %d EMU, want slide center %d EMU (off by %d)", boxCenter, slideCenter, boxCenter-slideCenter)
		}
	}
	if !found {
		t.Fatalf("no text box xfrm found in slide XML: %s", slideXML)
	}
	// And the paragraph alignment must be centered.
	if !strings.Contains(slideXML, `algn="ctr"`) {
		t.Errorf(`centered title missing algn="ctr": %s`, slideXML)
	}
}

// TestPPTXImageHonorsFlip guards that a horizontally-flipped image node round-
// trips to a native picture with flipH="1" in its <a:xfrm>, so a borrowed hero
// photo re-exports mirrored the same way the source PowerPoint authored it
// (never re-mirrored). node.flipH/flipV reach the worker via the Node struct's
// flipH/flipV json tags and are passed to pptxgenjs addImage as flip options.
func TestPPTXImageHonorsFlip(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	// Minimal valid 1x1 transparent PNG as a data URL (the worker requires
	// node.props.data on an image node).
	const pngData = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
	scene := SceneGraph{SchemaVersion: SchemaV2, Title: "Flip image", Slides: []Slide{{
		ID: "flip", Nodes: []Node{
			{
				ID: "hero", Type: "image",
				Geometry: Geometry{X: 100, Y: 100, W: 800, H: 600},
				FlipH:    true,
				Props:    map[string]any{"data": pngData, "decorative": "true"},
			},
		},
	}}}
	result, err := exporter.Export(context.Background(), scene, false)
	if err != nil {
		t.Fatal(err)
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
	if slideXML == "" {
		t.Fatal("slide1.xml missing from exported pptx")
	}
	if !strings.Contains(slideXML, "<p:pic>") {
		t.Fatalf("expected a native picture in slide XML: %s", slideXML)
	}
	if !strings.Contains(slideXML, `flipH="1"`) {
		t.Errorf(`flipped image must serialize flipH="1" in its xfrm: %s`, slideXML)
	}
}

// TestPPTXImageResolvesAssetRef guards the imported-template export path. Deck
// slides built from an imported .pptx template author images as
// <ast-image asset-ref="sha256-…"> whose bytes live in the deck asset map
// (SceneGraph.Assets), NOT inline on the node. The PPTX worker must resolve the
// asset-ref against scene.assets into the picture's data (mirroring the HTML
// exporter's resolveImageSrc) instead of throwing "image … has no validated
// data" — the regression that broke PPTX export of every template-based deck.
func TestPPTXImageResolvesAssetRef(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	const pngData = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
	const ref = "sha256-deadbeef"
	scene := SceneGraph{
		SchemaVersion: SchemaV2,
		Title:         "Asset ref image",
		// The bytes live in the deck asset map, keyed by the asset-ref.
		Assets: map[string]string{ref: pngData},
		Slides: []Slide{{
			ID: "cover", Nodes: []Node{
				{
					ID: "logo", Type: "image",
					Geometry: Geometry{X: 100, Y: 100, W: 400, H: 200},
					// No inline data — only the asset-ref, as imported templates author it.
					Props: map[string]any{"asset-ref": ref},
				},
			},
		}},
	}
	result, err := exporter.Export(context.Background(), scene, false)
	if err != nil {
		t.Fatalf("export must resolve asset-ref, not fail: %v", err)
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
	if !strings.Contains(slideXML, "<p:pic>") {
		t.Fatalf("asset-ref image must serialize as a native picture: %s", slideXML)
	}
}

// TestPPTXImageMissingAssetDegrades guards that an image whose asset-ref cannot
// be resolved (e.g. an imported template that dropped an unsupported EMF vector)
// degrades to a skipped image with a warning rather than failing the whole
// export. Losing one decorative image must never make the deck un-exportable.
func TestPPTXImageMissingAssetDegrades(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	scene := SceneGraph{
		SchemaVersion: SchemaV2,
		Title:         "Missing asset",
		Slides: []Slide{{
			ID: "cover", Nodes: []Node{
				{ID: "title", Type: "text", Geometry: Geometry{X: 160, Y: 380, W: 1600, H: 160}, Text: "Still exports", Props: map[string]any{"size": "72"}},
				{ID: "c-3", Type: "image", Geometry: Geometry{X: 100, Y: 100, W: 400, H: 200}, Props: map[string]any{"asset-ref": "sha256-missing"}},
			},
		}},
	}
	result, err := exporter.Export(context.Background(), scene, false)
	if err != nil {
		t.Fatalf("a missing image asset must not fail the whole export: %v", err)
	}
	if len(result.Bytes) == 0 {
		t.Fatal("export produced no pptx bytes")
	}
	// The unresolved image is reported as a fallback diagnostic, not a hard error.
	foundWarn := false
	for _, d := range result.Diagnostics {
		if strings.Contains(d.Message, "c-3") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("expected a fallback warning naming the skipped image, got %#v", result.Diagnostics)
	}
}

// TestPPTXFontStackProducesWellFormedXML guards the fix for imported templates
// whose theme fonts are full CSS font-family stacks with embedded quotes, e.g.
// `"72 Brand", Aptos, Arial, sans-serif`. pptxgenjs writes fontFace verbatim
// into <a:latin typeface="...">, so an embedded quote produced malformed OOXML
// (typeface=""72 Brand", ...") and PowerPoint recovered the file, blanking the
// affected slides. The worker must reduce any CSS stack to a single, quote-free
// family so every slide part is well-formed XML.
func TestPPTXFontStackProducesWellFormedXML(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	exporter := PPTXExporter{Runner: pptxworker.Runner{
		WorkingDir: filepath.Join(repo, "web"),
		ScriptPath: filepath.Join(repo, "pkg/docs/slides/pptxworker/worker.mjs"),
		Timeout:    30 * time.Second,
	}}
	scene := SceneGraph{
		SchemaVersion: SchemaV2,
		Title:         "Quoted font stack",
		Theme: map[string]string{
			"displayFont": `"72 Brand Medium", Aptos, Arial, sans-serif`,
			"bodyFont":    `"72 Brand", Aptos, Arial, sans-serif`,
		},
		Slides: []Slide{{
			ID: "cover", Nodes: []Node{
				{ID: "title", Type: "text", Geometry: Geometry{X: 160, Y: 380, W: 1600, H: 160}, Text: "Cover title", Props: map[string]any{"size": "96", "font-token": "display"}},
				{ID: "body", Type: "text", Geometry: Geometry{X: 160, Y: 560, W: 1600, H: 120}, Text: "Body text", Props: map[string]any{"size": "40", "font-token": "body-font"}},
			},
		}},
	}
	result, err := exporter.Export(context.Background(), scene, false)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(result.Bytes), int64(len(result.Bytes)))
	if err != nil {
		t.Fatalf("invalid pptx zip: %v", err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		r, openErr := f.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		b, readErr := io.ReadAll(r)
		_ = r.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		xmlText := string(b)
		// The real corruption is malformed OOXML: an embedded quote in the
		// typeface attribute value breaks XML parsing (PowerPoint then recovers
		// the file and blanks the slide). Parsing the part is the reliable gate.
		// (Empty typeface="" for east-asian/complex-script fonts is valid and
		// intentional PptxGenJS output, so we do not string-match on it.)
		if err := xml.Unmarshal(b, new(struct {
			XMLName xml.Name
		})); err != nil {
			t.Fatalf("%s is not well-formed XML: %v (near %s)", f.Name, err, firstTypeface(xmlText))
		}
	}
}

func firstTypeface(xmlText string) string {
	idx := strings.Index(xmlText, `typeface="`)
	if idx < 0 {
		return ""
	}
	snippet := xmlText[idx:]
	if len(snippet) > 80 {
		snippet = snippet[:80]
	}
	return snippet
}
