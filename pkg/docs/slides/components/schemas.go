package components

// AttributeSchema describes the allowlisted attributes for an ASD element.
type AttributeSchema struct {
	Required []string
	Optional []string
}

// schemasV1 is the ASD v2 attribute allowlist. v2 is a strict superset of the
// original v1 shape: every v1 attribute still validates, and the new fidelity
// attributes (raw colors, gradients, rotation, geometry, rich text) are all
// OPTIONAL so pre-existing v1 markup continues to parse unchanged.
var schemasV1 = map[string]AttributeSchema{
	"ast-deck":  {Required: []string{"schema"}, Optional: []string{"ratio", "theme", "lang"}},
	"ast-slide": {Required: []string{"id"}, Optional: []string{"title", "lang"}},
	"ast-text": {Optional: []string{
		"id", "role", "x", "y", "w", "h", "inset", "font-token", "size", "weight", "color-token", "align", "alt", "decorative",
		// v2 fidelity attributes:
		"rot", "anchor", "wrap", "color", "font",
	}},
	"ast-shape": {Required: []string{"id", "kind", "x", "y", "w", "h"}, Optional: []string{
		"fill-token", "line-token", "line-width", "alt", "decorative",
		// v2 fidelity attributes:
		"rot", "fill", "line", "line-dash", "head-end", "tail-end", "geom", "path", "opacity",
	}},
	"ast-image": {Required: []string{"id", "asset-ref", "x", "y", "w", "h"}, Optional: []string{
		"fit", "alt", "decorative",
		// v2 fidelity attributes:
		"rot", "opacity",
	}},
	"ast-table": {Required: []string{"id", "data-ref", "x", "y", "w", "h"}, Optional: []string{"header", "style-token", "alt"}},
	"ast-chart": {Required: []string{"id", "kind", "data-ref", "x", "y", "w", "h"}, Optional: []string{"category-key", "value-keys", "style-token", "alt"}},
	"ast-group": {Required: []string{"id", "x", "y", "w", "h"}, Optional: []string{
		"alt", "decorative",
		// v2 fidelity attributes:
		"rot",
	}},
	"ast-run":   {Optional: []string{"b", "i", "u", "color", "font", "size", "weight"}},
	"ast-notes": {},
	"script":    {Required: []string{"type", "id"}},
}

func SchemaV1(tag string) (AttributeSchema, bool) {
	s, ok := schemasV1[tag]
	return s, ok
}
