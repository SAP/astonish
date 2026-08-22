package slides

import "fmt"

func ValidateSlide(slide Slide) []Diagnostic {
	var out []Diagnostic
	if slide.ID == "" {
		out = append(out, Diagnostic{Severity: "error", Code: "missing_id", Message: "slide requires a stable id"})
	}
	seen := map[string]bool{}
	var walk func([]Node)
	walk = func(nodes []Node) {
		for _, n := range nodes {
			if n.ID == "" && n.Type != "text" {
				out = append(out, Diagnostic{Severity: "error", Code: "missing_id", Message: "element requires a stable id", SlideID: slide.ID})
			}
			if n.ID != "" {
				if seen[n.ID] {
					out = append(out, Diagnostic{Severity: "error", Code: "duplicate_id", Message: fmt.Sprintf("duplicate element id %q", n.ID), SlideID: slide.ID, ElementID: n.ID})
				}
				seen[n.ID] = true
			}
			if n.Geometry.W < 0 || n.Geometry.H < 0 || n.Geometry.X < 0 || n.Geometry.Y < 0 || n.Geometry.X+n.Geometry.W > CanvasWidth || n.Geometry.Y+n.Geometry.H > CanvasHeight {
				out = append(out, Diagnostic{Severity: "error", Code: "invalid_geometry", Message: "element geometry is outside the slide canvas", SlideID: slide.ID, ElementID: n.ID})
			}
			if n.Type == "image" {
				alt, _ := n.Props["alt"].(string)
				decorative, _ := n.Props["decorative"].(string)
				if alt == "" && decorative != "true" {
					out = append(out, Diagnostic{Severity: "warning", Code: "missing_alt", Message: "image requires alt text or decorative=true", SlideID: slide.ID, ElementID: n.ID})
				}
			}
			walk(n.Children)
		}
	}
	walk(slide.Nodes)
	return out
}

func HasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}
