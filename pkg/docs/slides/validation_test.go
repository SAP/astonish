package slides

import "testing"

func TestValidateSlideGeometryAndAccessibility(t *testing.T) {
	d := ValidateSlide(Slide{ID: "s", Nodes: []Node{{ID: "i", Type: "image", Geometry: Geometry{X: 1900, Y: 0, W: 100, H: 100}}}})
	if len(d) != 2 {
		t.Fatalf("expected geometry and alt diagnostics, got %#v", d)
	}
}
