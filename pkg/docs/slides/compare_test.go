package slides

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// makeSourceModel returns a TemplateModel with the given slides.
func makeSourceModel(slides ...themes.IRLayout) *themes.TemplateModel {
	return &themes.TemplateModel{
		Schema: themes.SchemaModelV3,
		Slides: slides,
	}
}

// makeSourceSlide returns a simple IRLayout with a background color, a text
// chrome object, and a placeholder text color.
func makeSourceSlide(id, bgColor, textColor, contentText string) themes.IRLayout {
	slide := themes.IRLayout{
		ID:   id,
		Name: id,
		Background: themes.IRBackground{
			Kind:  "solid",
			Color: bgColor,
		},
	}
	if textColor != "" {
		slide.Placeholders = []themes.IRPlaceholder{
			{
				Name:  "Title",
				Type:  "title",
				Style: themes.IRTextStyle{Color: textColor},
			},
		}
	}
	if contentText != "" {
		slide.Objects = []themes.IRChrome{
			{Kind: "text", Text: contentText, X: 100, Y: 100, W: 400, H: 60},
		}
	}
	return slide
}

// makeRecoScene builds a SceneGraph from pre-built Slide values.
func makeRecoScene(slides ...Slide) *SceneGraph {
	return &SceneGraph{
		SchemaVersion: SchemaV2,
		Title:         "Reconstruction",
		Slides:        slides,
	}
}

// makeRecoSlide returns a Slide whose first node is a decorative background
// shape with the given fill color, and an optional text node with the given
// text and color.
func makeRecoSlide(id, bgFill, textColor, nodeText string) Slide {
	bgNode := Node{
		ID:   "bg",
		Type: "shape",
		Geometry: Geometry{X: 0, Y: 0, W: 1920, H: 1080},
		Fill: bgFill,
		Props: map[string]any{
			"alt":        "",
			"decorative": "true",
		},
	}
	s := Slide{
		ID:    id,
		Nodes: []Node{bgNode},
	}
	if nodeText != "" || textColor != "" {
		textNode := Node{
			ID:   "ph-title",
			Type: "text",
			Geometry: Geometry{X: 160, Y: 120, W: 1600, H: 140},
			Text: nodeText,
			Props: map[string]any{
				"color": textColor,
			},
		}
		s.Nodes = append(s.Nodes, textNode)
	}
	return s
}

// ---------------------------------------------------------------------------
// Test 1: Exact match — score should be 1.0, Passed = true
// ---------------------------------------------------------------------------

func TestCompareExactMatch(t *testing.T) {
	bgColor := "#002A86"
	textColor := "#FFFFFF"
	contentText := "Hello World"

	src := makeSourceModel(
		makeSourceSlide("slide-1", bgColor, textColor, contentText),
	)
	reco := makeRecoScene(
		makeRecoSlide("slide-1", bgColor, textColor, contentText),
	)

	report := Compare(src, reco)

	if report.FidelityScore != 1.0 {
		t.Errorf("expected FidelityScore=1.0, got %.4f (struct=%.3f style=%.3f content=%.3f)",
			report.FidelityScore, report.StructureScore, report.StyleScore, report.ContentScore)
	}
	if !report.Passed {
		t.Error("expected Passed=true for exact match")
	}
	if len(report.SlideFindings) != 0 {
		t.Errorf("expected no slide findings, got %d: %+v", len(report.SlideFindings), report.SlideFindings)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Slide count mismatch — score < 1.0, GapSlideCountMismatch present
// ---------------------------------------------------------------------------

func TestCompareSlideCountMismatch(t *testing.T) {
	bgColor := "#002A86"
	src := makeSourceModel(
		makeSourceSlide("slide-1", bgColor, "", ""),
		makeSourceSlide("slide-2", bgColor, "", ""),
		makeSourceSlide("slide-3", bgColor, "", ""),
	)
	// Reconstruction only has 2 slides.
	reco := makeRecoScene(
		makeRecoSlide("slide-1", bgColor, "", ""),
		makeRecoSlide("slide-2", bgColor, "", ""),
	)

	report := Compare(src, reco)

	if report.FidelityScore >= 1.0 {
		t.Errorf("expected FidelityScore < 1.0 for count mismatch, got %.4f", report.FidelityScore)
	}

	found := false
	for _, sf := range report.SlideFindings {
		for _, gap := range sf.Gaps {
			if gap.Kind == GapSlideCountMismatch {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected GapSlideCountMismatch in findings, got: %+v", report.SlideFindings)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Wrong background color — GapWrongBackground present
// ---------------------------------------------------------------------------

func TestCompareWrongBackground(t *testing.T) {
	src := makeSourceModel(
		makeSourceSlide("slide-1", "#002A86", "", ""),
	)
	reco := makeRecoScene(
		makeRecoSlide("slide-1", "#FFFFFF", "", ""),
	)

	report := Compare(src, reco)

	found := false
	for _, sf := range report.SlideFindings {
		for _, gap := range sf.Gaps {
			if gap.Kind == GapWrongBackground {
				found = true
				if gap.Expected != "#002A86" {
					t.Errorf("expected Expected=#002A86, got %q", gap.Expected)
				}
				if gap.Actual != "#FFFFFF" {
					t.Errorf("expected Actual=#FFFFFF, got %q", gap.Actual)
				}
			}
		}
	}
	if !found {
		t.Errorf("expected GapWrongBackground in findings, got: %+v", report.SlideFindings)
	}
	if report.StyleScore >= 1.0 {
		t.Errorf("expected StyleScore < 1.0 for background mismatch, got %.4f", report.StyleScore)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Missing content — score < AcceptanceThreshold, Passed = false
// ---------------------------------------------------------------------------

func TestCompareMissingContent(t *testing.T) {
	// Source has text "Hello World" but reconstruction has empty markup.
	// Use a non-white background so the style check also penalises the empty slide.
	src := makeSourceModel(
		makeSourceSlide("slide-1", "#002A86", "#FFFFFF", "Hello World"),
	)
	// Empty reconstruction slide (no nodes → both empty-markup and content penalties apply).
	reco := makeRecoScene(
		Slide{ID: "slide-1", Nodes: nil},
	)

	report := Compare(src, reco)

	if report.Passed {
		t.Errorf("expected Passed=false when source content is missing from reconstruction, score=%.4f", report.FidelityScore)
	}
	if report.FidelityScore >= AcceptanceThreshold {
		t.Errorf("expected FidelityScore < %.2f, got %.4f", AcceptanceThreshold, report.FidelityScore)
	}

	foundMissing := false
	for _, sf := range report.SlideFindings {
		for _, gap := range sf.Gaps {
			if gap.Kind == GapMissingContent {
				foundMissing = true
			}
		}
	}
	if !foundMissing {
		t.Errorf("expected GapMissingContent in findings, got: %+v", report.SlideFindings)
	}
}

// ---------------------------------------------------------------------------
// Test 5: AcceptanceThreshold constant value and Passed logic
// ---------------------------------------------------------------------------

func TestCompareAcceptanceThreshold(t *testing.T) {
	if AcceptanceThreshold != 0.75 {
		t.Errorf("expected AcceptanceThreshold=0.75, got %v", AcceptanceThreshold)
	}

	// Build a scenario just above the threshold: matching background and
	// content but a text color mismatch (minor penalty).
	bgColor := "#002A86"
	textColor := "#FFFFFF"
	content := "Slide content that is long enough"

	src := makeSourceModel(
		makeSourceSlide("slide-1", bgColor, textColor, content),
	)
	recoAbove := makeRecoScene(
		makeRecoSlide("slide-1", bgColor, textColor, content),
	)
	above := Compare(src, recoAbove)
	if !above.Passed {
		t.Errorf("exact-match report should have Passed=true, score=%.4f", above.FidelityScore)
	}

	// Build a scenario well below the threshold: completely wrong background +
	// missing content (both penalised).
	srcBelow := makeSourceModel(
		makeSourceSlide("slide-1", "#002A86", "#FFFFFF", "Important text here"),
	)
	recoBelow := makeRecoScene(
		Slide{ID: "slide-1", Nodes: nil},
	)
	below := Compare(srcBelow, recoBelow)
	if below.Passed {
		t.Errorf("heavily-penalised report should have Passed=false, score=%.4f", below.FidelityScore)
	}
	if below.FidelityScore >= AcceptanceThreshold {
		t.Errorf("expected FidelityScore < %.2f for penalised report, got %.4f", AcceptanceThreshold, below.FidelityScore)
	}

	// Verify AcceptanceBar mirrors Passed.
	if AcceptanceBar(above) != above.Passed {
		t.Error("AcceptanceBar should mirror Passed field")
	}
	if AcceptanceBar(below) != below.Passed {
		t.Error("AcceptanceBar should mirror Passed field")
	}
}

// ---------------------------------------------------------------------------
// Test 6: roundRect card missing in reconstruction
// ---------------------------------------------------------------------------

// TestCompareMissingRoundRectCards checks that a source slide with roundRect
// card shapes triggers a GapMissingShapeFamily when the reconstruction has none.
func TestCompareMissingRoundRectCards(t *testing.T) {
	// Source slide has two roundRect card shapes with accent fills.
	src := makeSourceModel(themes.IRLayout{
		ID:   "slide-1",
		Name: "Card Layout",
		Background: themes.IRBackground{Kind: "solid", Color: "#FFFFFF"},
		Objects: []themes.IRChrome{
			{Kind: "rect", RectRadius: 8, X: 100, Y: 200, W: 400, H: 300,
				Fill: &themes.IRFill{Kind: "solid", Color: "#0A6ED1"}},
			{Kind: "rect", RectRadius: 8, X: 600, Y: 200, W: 400, H: 300,
				Fill: &themes.IRFill{Kind: "solid", Color: "#E9730C"}},
		},
	})

	// Reconstruction has only plain rect shapes, no roundRects.
	reco := makeRecoScene(Slide{
		ID: "slide-1",
		Nodes: []Node{
			{ID: "bg", Type: "shape", Geom: "rect", Geometry: Geometry{X: 0, Y: 0, W: 1920, H: 1080},
				Fill: "#FFFFFF", Props: map[string]any{"decorative": "true"}},
			{ID: "c-1", Type: "shape", Geom: "rect", Geometry: Geometry{X: 100, Y: 200, W: 400, H: 300},
				Fill: "#0A6ED1"},
		},
	})

	report := Compare(src, reco)

	found := false
	for _, sf := range report.SlideFindings {
		for _, gap := range sf.Gaps {
			if gap.Kind == GapMissingShapeFamily && gap.Expected == "roundRect shapes" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected GapMissingShapeFamily(roundRect) in findings, got: %+v", report.SlideFindings)
	}
	if report.StructureScore >= 1.0 {
		t.Errorf("expected StructureScore < 1.0 for missing roundRect cards, got %.4f", report.StructureScore)
	}
}

// ---------------------------------------------------------------------------
// Test 7: Missing accent fill color in reconstruction
// ---------------------------------------------------------------------------

// TestCompareMissingAccentColor checks that a source accent color not present
// in the reconstruction triggers a GapMissingFillColor.
func TestCompareMissingAccentColor(t *testing.T) {
	srcAccent := "#7357D9" // brand purple — a clearly non-grey accent

	src := makeSourceModel(themes.IRLayout{
		ID:   "slide-1",
		Name: "Accent Slide",
		Background: themes.IRBackground{Kind: "solid", Color: "#FFFFFF"},
		Objects: []themes.IRChrome{
			{Kind: "rect", X: 0, Y: 0, W: 1920, H: 12,
				Fill: &themes.IRFill{Kind: "solid", Color: srcAccent}},
		},
	})

	// Reconstruction has no purple shape.
	reco := makeRecoScene(Slide{
		ID: "slide-1",
		Nodes: []Node{
			{ID: "bg", Type: "shape", Geom: "rect", Geometry: Geometry{X: 0, Y: 0, W: 1920, H: 1080},
				Fill: "#FFFFFF", Props: map[string]any{"decorative": "true"}},
		},
	})

	report := Compare(src, reco)

	found := false
	srcAccentLower := strings.ToLower(srcAccent)
	for _, sf := range report.SlideFindings {
		for _, gap := range sf.Gaps {
			if gap.Kind == GapMissingFillColor && strings.ToLower(gap.Expected) == srcAccentLower {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected GapMissingFillColor(%s) in findings, got: %+v", srcAccent, report.SlideFindings)
	}
}

// ---------------------------------------------------------------------------
// Test 8: Shape count coverage — large deviation triggers GapWrongShapeCount
// ---------------------------------------------------------------------------

func TestCompareWrongShapeCount(t *testing.T) {
	// Source has 8 chrome shapes.
	objects := make([]themes.IRChrome, 8)
	for i := range objects {
		objects[i] = themes.IRChrome{
			Kind: "rect", X: i * 200, Y: 100, W: 180, H: 200,
			Fill: &themes.IRFill{Kind: "solid", Color: "#188918"},
		}
	}

	src := makeSourceModel(themes.IRLayout{
		ID:         "slide-1",
		Background: themes.IRBackground{Kind: "solid", Color: "#FFFFFF"},
		Objects:    objects,
	})

	// Reconstruction has only 2 chrome shapes (25% coverage → large deviation).
	reco := makeRecoScene(Slide{
		ID: "slide-1",
		Nodes: []Node{
			{ID: "bg", Type: "shape", Geom: "rect", Geometry: Geometry{X: 0, Y: 0, W: 1920, H: 1080},
				Fill: "#FFFFFF", Props: map[string]any{"decorative": "true"}},
			{ID: "c-1", Type: "shape", Geom: "rect", Geometry: Geometry{X: 0, Y: 100, W: 180, H: 200}, Fill: "#188918"},
			{ID: "c-2", Type: "shape", Geom: "rect", Geometry: Geometry{X: 200, Y: 100, W: 180, H: 200}, Fill: "#188918"},
		},
	})

	report := Compare(src, reco)

	found := false
	for _, sf := range report.SlideFindings {
		for _, gap := range sf.Gaps {
			if gap.Kind == GapWrongShapeCount {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected GapWrongShapeCount in findings, got: %+v", report.SlideFindings)
	}
}

// ---------------------------------------------------------------------------
// Test 9: extractSourceFingerprint — structural pattern detection
// ---------------------------------------------------------------------------

func TestExtractSourceFingerprint(t *testing.T) {
	layout := themes.IRLayout{
		ID:         "slide-1",
		Name:       "Card Grid",
		Background: themes.IRBackground{Kind: "solid", Color: "#002A86"},
		Objects: []themes.IRChrome{
			// A roundRect card with accent fill.
			{Kind: "rect", RectRadius: 8, X: 100, Y: 200, W: 400, H: 300,
				Fill: &themes.IRFill{Kind: "solid", Color: "#0A6ED1"}},
			// A narrow left stripe.
			{Kind: "rect", X: 100, Y: 200, W: 8, H: 300,
				Fill: &themes.IRFill{Kind: "solid", Color: "#E9730C"}},
			// A horizontal line.
			{Kind: "line", X: 80, Y: 180, W: 1760, H: 4,
				Fill: &themes.IRFill{Kind: "solid", Color: "#CCCCCC"}},
		},
	}

	fp := extractSourceFingerprint(0, layout)

	if !fp.HasRoundRects {
		t.Error("expected HasRoundRects=true")
	}
	if !fp.HasStripes {
		t.Error("expected HasStripes=true")
	}
	if fp.Background != "#002A86" {
		t.Errorf("expected Background=#002A86, got %q", fp.Background)
	}
	// The rect+rectRadius is a roundRect; the stripe rect is also a rect. Chrome count excludes text/image.
	if fp.ChromeShapeCount < 2 {
		t.Errorf("expected ChromeShapeCount >= 2, got %d", fp.ChromeShapeCount)
	}
}

// ---------------------------------------------------------------------------
// Test 10: isAccentColor — distinguishes accent colors from neutral ones
// ---------------------------------------------------------------------------

func TestIsAccentColor(t *testing.T) {
	cases := []struct {
		hex    string
		expect bool
	}{
		{"#0A6ED1", true},  // SAP blue
		{"#7357D9", true},  // brand purple
		{"#E9730C", true},  // amber
		{"#188918", true},  // green
		{"#FFFFFF", false}, // white
		{"#000000", false}, // black
		{"#CCCCCC", false}, // mid-grey
		{"#F5F5F5", false}, // near-white grey
		{"#1A1A1A", false}, // near-black
	}
	for _, tc := range cases {
		got := isAccentColor(tc.hex)
		if got != tc.expect {
			t.Errorf("isAccentColor(%q) = %v, want %v", tc.hex, got, tc.expect)
		}
	}
}
