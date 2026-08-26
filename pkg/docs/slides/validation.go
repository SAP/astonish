package slides

import (
	"fmt"
	"regexp"
	"strings"
)

func ValidateMarkup(markup string) []Diagnostic {
	_, diagnostics, err := ParseSlide(markup)
	if err != nil {
		return []Diagnostic{{Severity: "error", Code: "invalid_markup", Message: err.Error()}}
	}
	return diagnostics
}

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
			validateFidelity(&out, slide.ID, n)
			walk(n.Children)
		}
	}
	walk(slide.Nodes)
	if usesPercentageScale(slide.Nodes) {
		out = append(out, Diagnostic{Severity: "error", Code: "invalid_geometry_scale", Message: "top-level geometry uses a 0-100 scale; x, y, w, and h must be logical pixels on the 1920x1080 canvas", SlideID: slide.ID})
	}
	for _, node := range slide.Nodes {
		if node.Type == "text" && containsMarkdownEmphasis(node.Text) {
			out = append(out, Diagnostic{Severity: "warning", Code: "literal_markdown", Message: "ast-text renders plain text; use weight/style attributes instead of Markdown emphasis markers", SlideID: slide.ID, ElementID: node.ID})
		}
	}
	return out
}

func containsMarkdownEmphasis(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "**") && strings.HasSuffix(trimmed, "**") ||
		strings.HasPrefix(trimmed, "*") && strings.HasSuffix(trimmed, "*")
}

func usesPercentageScale(nodes []Node) bool {
	if len(nodes) < 2 {
		return false
	}
	maxRight, maxBottom := 0, 0
	for _, node := range nodes {
		maxRight = max(maxRight, node.Geometry.X+node.Geometry.W)
		maxBottom = max(maxBottom, node.Geometry.Y+node.Geometry.H)
	}
	return maxRight <= 100 && maxBottom <= 100
}

func HasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

// safeColorPattern matches either a hex color (#RRGGBB or #RRGGBBAA) or an
// rgb()/rgba() function containing only digits, commas, spaces, dots, and
// percent signs. Anything else (letters, semicolons, angle brackets) is
// rejected to guard against injection into SVG/CSS output.
var (
	safeColorPattern = regexp.MustCompile(`^(#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?|rgba?\([0-9.,%\s]+\))$`)
	safePathPattern  = regexp.MustCompile(`^[MmLlCcQqZzHhVvAa0-9\s,.+-]*$`)
)

// allowedGeomPresets is the set of built-in geometry presets a shape may use.
var allowedGeomPresets = map[string]bool{
	"rect": true, "roundRect": true, "ellipse": true, "triangle": true,
	"rtTriangle": true, "diamond": true, "parallelogram": true, "trapezoid": true,
	"hexagon": true, "octagon": true, "star5": true, "rightArrow": true,
	"leftArrow": true, "chevron": true, "cloud": true, "can": true, "cube": true,
	"line": true, "bracketPair": true,
}

// safeColor reports whether s is a safe, well-formed color literal.
func safeColor(s string) bool {
	return safeColorPattern.MatchString(s)
}

// safePath reports whether s contains only SVG-path-safe characters.
func safePath(s string) bool {
	return safePathPattern.MatchString(s)
}

// safeFont reports whether s is free of characters that could break out of an
// SVG/CSS context.
func safeFont(s string) bool {
	return !strings.ContainsAny(s, ";<>")
}

// validateFidelity checks the v2 fidelity attributes (rotation, opacity, fill,
// line, gradient, geometry preset, custom path) plus rich-run styling on a
// single node, appending diagnostics to out.
func validateFidelity(out *[]Diagnostic, slideID string, n Node) {
	if n.Rot < -360 || n.Rot > 360 {
		*out = append(*out, Diagnostic{Severity: "error", Code: "invalid_rotation", Message: fmt.Sprintf("rotation %d is outside the -360..360 range", n.Rot), SlideID: slideID, ElementID: n.ID})
	}
	// Opacity==0 is treated as unset (json omitempty); only reject out-of-range values.
	if n.Opacity < 0 || n.Opacity > 1 {
		*out = append(*out, Diagnostic{Severity: "error", Code: "invalid_opacity", Message: fmt.Sprintf("opacity %g must be within [0,1]", n.Opacity), SlideID: slideID, ElementID: n.ID})
	}
	if n.Fill != "" && !safeColor(n.Fill) {
		*out = append(*out, Diagnostic{Severity: "error", Code: "invalid_color", Message: fmt.Sprintf("fill color %q is not a safe #RRGGBB(AA) or rgb()/rgba() value", n.Fill), SlideID: slideID, ElementID: n.ID})
	}
	if n.Line != "" && !safeColor(n.Line) {
		*out = append(*out, Diagnostic{Severity: "error", Code: "invalid_color", Message: fmt.Sprintf("line color %q is not a safe #RRGGBB(AA) or rgb()/rgba() value", n.Line), SlideID: slideID, ElementID: n.ID})
	}
	if n.Gradient != nil {
		validateGradient(out, slideID, n)
	}
	if n.Geom != "" && !allowedGeomPresets[n.Geom] {
		*out = append(*out, Diagnostic{Severity: "error", Code: "invalid_geometry_preset", Message: fmt.Sprintf("geometry preset %q is not an allowed shape", n.Geom), SlideID: slideID, ElementID: n.ID})
	}
	if n.Path != "" && !safePath(n.Path) {
		*out = append(*out, Diagnostic{Severity: "error", Code: "invalid_path", Message: fmt.Sprintf("custom path contains unsafe characters: %q", n.Path), SlideID: slideID, ElementID: n.ID})
	}
	for _, run := range n.Runs {
		if run.Color != "" && !safeColor(run.Color) {
			*out = append(*out, Diagnostic{Severity: "error", Code: "invalid_color", Message: fmt.Sprintf("run color %q is not a safe #RRGGBB(AA) or rgb()/rgba() value", run.Color), SlideID: slideID, ElementID: n.ID})
		}
		if run.Font != "" && !safeFont(run.Font) {
			*out = append(*out, Diagnostic{Severity: "error", Code: "invalid_font", Message: fmt.Sprintf("run font %q contains unsafe characters", run.Font), SlideID: slideID, ElementID: n.ID})
		}
	}
}

// validateGradient checks a non-nil gradient's kind, stop count, stop ordering,
// stop positions, and stop colors.
func validateGradient(out *[]Diagnostic, slideID string, n Node) {
	g := n.Gradient
	bad := func(msg string) {
		*out = append(*out, Diagnostic{Severity: "error", Code: "invalid_gradient", Message: msg, SlideID: slideID, ElementID: n.ID})
	}
	if g.Kind != "linear" && g.Kind != "radial" {
		bad(fmt.Sprintf("gradient kind %q must be linear or radial", g.Kind))
	}
	if len(g.Stops) < 2 {
		bad("gradient requires at least 2 stops")
	}
	prev := -1
	for _, stop := range g.Stops {
		if stop.Pos < 0 || stop.Pos > 100 {
			bad(fmt.Sprintf("gradient stop position %d must be within [0,100]", stop.Pos))
		}
		if stop.Pos < prev {
			bad("gradient stop positions must be non-decreasing")
		}
		prev = stop.Pos
		if stop.Color != "" && !safeColor(stop.Color) {
			bad(fmt.Sprintf("gradient stop color %q is not a safe value", stop.Color))
		}
	}
}
