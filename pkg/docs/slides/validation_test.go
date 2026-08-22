package slides

import "testing"

func TestValidateSlideGeometryAndAccessibility(t *testing.T) {
	d := ValidateSlide(Slide{ID: "s", Nodes: []Node{{ID: "i", Type: "image", Geometry: Geometry{X: 1900, Y: 0, W: 100, H: 100}}}})
	if len(d) != 2 {
		t.Fatalf("expected geometry and alt diagnostics, got %#v", d)
	}
}

func TestValidateSlideRejectsPercentageScaleGeometry(t *testing.T) {
	diagnostics := ValidateSlide(Slide{ID: "percentage-layout", Nodes: []Node{
		{ID: "title", Type: "text", Geometry: Geometry{X: 10, Y: 5, W: 80, H: 10}},
		{ID: "body", Type: "text", Geometry: Geometry{X: 10, Y: 20, W: 80, H: 70}},
	}})
	if len(diagnostics) != 1 || diagnostics[0].Code != "invalid_geometry_scale" || diagnostics[0].Severity != "error" {
		t.Fatalf("expected percentage-scale geometry error, got %#v", diagnostics)
	}
}

func TestValidateSlideAllowsLogicalPixelGeometry(t *testing.T) {
	diagnostics := ValidateSlide(Slide{ID: "pixel-layout", Nodes: []Node{
		{ID: "title", Type: "text", Geometry: Geometry{X: 120, Y: 60, W: 1680, H: 120}},
		{ID: "body", Type: "text", Geometry: Geometry{X: 120, Y: 220, W: 1680, H: 700}},
	}})
	if len(diagnostics) != 0 {
		t.Fatalf("expected valid logical-pixel geometry, got %#v", diagnostics)
	}
}

func TestValidateSlideWarnsAboutLiteralMarkdown(t *testing.T) {
	diagnostics := ValidateSlide(Slide{ID: "markdown", Nodes: []Node{{ID: "title", Type: "text", Geometry: Geometry{W: 800, H: 100}, Text: "**Literal title**"}}})
	if len(diagnostics) != 1 || diagnostics[0].Code != "literal_markdown" || diagnostics[0].Severity != "warning" {
		t.Fatalf("expected literal Markdown warning, got %#v", diagnostics)
	}
}
