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

func TestValidateSlideFidelityAttributes(t *testing.T) {
	geom := Geometry{X: 100, Y: 100, W: 600, H: 400}
	cases := []struct {
		name     string
		node     Node
		wantCode string // "" means no diagnostics expected
	}{
		{name: "valid rotation", node: Node{ID: "n", Type: "shape", Geometry: geom, Rot: 45}},
		{name: "rotation too high", node: Node{ID: "n", Type: "shape", Geometry: geom, Rot: 720}, wantCode: "invalid_rotation"},
		{name: "rotation too low", node: Node{ID: "n", Type: "shape", Geometry: geom, Rot: -400}, wantCode: "invalid_rotation"},

		{name: "valid opacity", node: Node{ID: "n", Type: "shape", Geometry: geom, Opacity: 0.5}},
		{name: "opacity zero unset", node: Node{ID: "n", Type: "shape", Geometry: geom, Opacity: 0}},
		{name: "opacity negative", node: Node{ID: "n", Type: "shape", Geometry: geom, Opacity: -0.1}, wantCode: "invalid_opacity"},
		{name: "opacity above one", node: Node{ID: "n", Type: "shape", Geometry: geom, Opacity: 1.5}, wantCode: "invalid_opacity"},

		{name: "valid hex fill", node: Node{ID: "n", Type: "shape", Geometry: geom, Fill: "#AABBCC"}},
		{name: "valid hex alpha fill", node: Node{ID: "n", Type: "shape", Geometry: geom, Fill: "#AABBCCDD"}},
		{name: "valid rgb fill", node: Node{ID: "n", Type: "shape", Geometry: geom, Fill: "rgb(10, 20, 30)"}},
		{name: "valid rgba fill", node: Node{ID: "n", Type: "shape", Geometry: geom, Fill: "rgba(10, 20, 30, 0.5)"}},
		{name: "invalid fill name", node: Node{ID: "n", Type: "shape", Geometry: geom, Fill: "red"}, wantCode: "invalid_color"},
		{name: "invalid fill injection", node: Node{ID: "n", Type: "shape", Geometry: geom, Fill: "#fff;</style>"}, wantCode: "invalid_color"},
		{name: "valid line color", node: Node{ID: "n", Type: "shape", Geometry: geom, Line: "#123456"}},
		{name: "invalid line color", node: Node{ID: "n", Type: "shape", Geometry: geom, Line: "javascript:alert(1)"}, wantCode: "invalid_color"},

		{name: "valid gradient", node: Node{ID: "n", Type: "shape", Geometry: geom, Gradient: &Gradient{Kind: "linear", Stops: []GradientStop{{Pos: 0, Color: "#000000"}, {Pos: 100, Color: "#FFFFFF"}}}}},
		{name: "valid radial gradient equal stops", node: Node{ID: "n", Type: "shape", Geometry: geom, Gradient: &Gradient{Kind: "radial", Stops: []GradientStop{{Pos: 50, Color: "#000000"}, {Pos: 50, Color: "#FFFFFF"}}}}},
		{name: "gradient bad kind", node: Node{ID: "n", Type: "shape", Geometry: geom, Gradient: &Gradient{Kind: "sweep", Stops: []GradientStop{{Pos: 0, Color: "#000000"}, {Pos: 100, Color: "#FFFFFF"}}}}, wantCode: "invalid_gradient"},
		{name: "gradient too few stops", node: Node{ID: "n", Type: "shape", Geometry: geom, Gradient: &Gradient{Kind: "linear", Stops: []GradientStop{{Pos: 0, Color: "#000000"}}}}, wantCode: "invalid_gradient"},
		{name: "gradient stop out of range", node: Node{ID: "n", Type: "shape", Geometry: geom, Gradient: &Gradient{Kind: "linear", Stops: []GradientStop{{Pos: 0, Color: "#000000"}, {Pos: 120, Color: "#FFFFFF"}}}}, wantCode: "invalid_gradient"},
		{name: "gradient stops descending", node: Node{ID: "n", Type: "shape", Geometry: geom, Gradient: &Gradient{Kind: "linear", Stops: []GradientStop{{Pos: 80, Color: "#000000"}, {Pos: 20, Color: "#FFFFFF"}}}}, wantCode: "invalid_gradient"},
		{name: "gradient bad stop color", node: Node{ID: "n", Type: "shape", Geometry: geom, Gradient: &Gradient{Kind: "linear", Stops: []GradientStop{{Pos: 0, Color: "blue"}, {Pos: 100, Color: "#FFFFFF"}}}}, wantCode: "invalid_gradient"},

		{name: "valid geom preset", node: Node{ID: "n", Type: "shape", Geometry: geom, Geom: "roundRect"}},
		{name: "invalid geom preset", node: Node{ID: "n", Type: "shape", Geometry: geom, Geom: "pentagon"}, wantCode: "invalid_geometry_preset"},

		{name: "valid path", node: Node{ID: "n", Type: "shape", Geometry: geom, Path: "M0 0 L10 10 C 20,20 30,30 40,40 Z"}},
		{name: "invalid path letters", node: Node{ID: "n", Type: "shape", Geometry: geom, Path: "M0 0 evil(x) Z"}, wantCode: "invalid_path"},

		{name: "valid run color", node: Node{ID: "n", Type: "text", Geometry: geom, Runs: []TextRun{{Text: "hi", Color: "#00FF00"}}}},
		{name: "invalid run color", node: Node{ID: "n", Type: "text", Geometry: geom, Runs: []TextRun{{Text: "hi", Color: "green"}}}, wantCode: "invalid_color"},
		{name: "valid run font", node: Node{ID: "n", Type: "text", Geometry: geom, Runs: []TextRun{{Text: "hi", Font: "Arial"}}}},
		{name: "invalid run font", node: Node{ID: "n", Type: "text", Geometry: geom, Runs: []TextRun{{Text: "hi", Font: "Arial;<script>"}}}, wantCode: "invalid_font"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := ValidateSlide(Slide{ID: "s", Nodes: []Node{tc.node}})
			if tc.wantCode == "" {
				if len(diagnostics) != 0 {
					t.Fatalf("expected no diagnostics, got %#v", diagnostics)
				}
				return
			}
			found := false
			for _, d := range diagnostics {
				if d.Code == tc.wantCode {
					if d.Severity != "error" {
						t.Fatalf("expected error severity for %s, got %q", tc.wantCode, d.Severity)
					}
					if d.SlideID != "s" || d.ElementID != tc.node.ID {
						t.Fatalf("expected slide/element ids set, got %#v", d)
					}
					found = true
				}
			}
			if !found {
				t.Fatalf("expected diagnostic code %q, got %#v", tc.wantCode, diagnostics)
			}
		})
	}
}

func TestSafePathAllowsArcCommand(t *testing.T) {
	geom := Geometry{X: 100, Y: 100, W: 600, H: 400}
	cases := []struct {
		name     string
		path     string
		wantCode string // "" means no invalid_path diagnostic expected
	}{
		// (a) The SVG arc command (A/a) now validates — this is the widening.
		{name: "arc command uppercase", path: "M10 10 A20 20 0 0 1 40 40 Z"},
		{name: "arc command lowercase", path: "m10 10 a20 20 0 0 1 40 40 z"},
		// (c) Superset: a pre-existing v2 path using only M/L/C/Q/Z still validates.
		{name: "legacy curve path still valid", path: "M0 0 L10 10 C 20,20 30,30 40,40 Q 5,5 6,6 Z"},
		// (b) Unsafe paths are still rejected.
		{name: "script injection rejected", path: "M0 0 L10 10 <script>", wantCode: "invalid_path"},
		{name: "unsafe letter rejected", path: "M0 0 Z X", wantCode: "invalid_path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := Node{ID: "n", Type: "shape", Geometry: geom, Path: tc.path}
			diagnostics := ValidateSlide(Slide{ID: "s", Nodes: []Node{node}})
			hasInvalidPath := false
			for _, d := range diagnostics {
				if d.Code == "invalid_path" {
					hasInvalidPath = true
				}
			}
			if tc.wantCode == "invalid_path" {
				if !hasInvalidPath {
					t.Fatalf("expected invalid_path diagnostic, got %#v", diagnostics)
				}
				if !HasErrors(diagnostics) {
					t.Fatalf("expected errors for unsafe path, got %#v", diagnostics)
				}
				return
			}
			if hasInvalidPath || HasErrors(diagnostics) {
				t.Fatalf("expected no invalid_path error for path %q, got %#v", tc.path, diagnostics)
			}
		})
	}
}

func TestValidateSlideRecursesIntoChildren(t *testing.T) {
	diagnostics := ValidateSlide(Slide{ID: "s", Nodes: []Node{{
		ID: "group", Type: "group", Geometry: Geometry{X: 100, Y: 100, W: 600, H: 400},
		Children: []Node{{ID: "child", Type: "shape", Geometry: Geometry{X: 120, Y: 120, W: 100, H: 100}, Fill: "notacolor"}},
	}}})
	if len(diagnostics) != 1 || diagnostics[0].Code != "invalid_color" || diagnostics[0].ElementID != "child" {
		t.Fatalf("expected invalid_color on nested child, got %#v", diagnostics)
	}
}
