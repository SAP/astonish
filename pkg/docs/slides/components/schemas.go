package components

// AttributeSchema describes the allowlisted attributes for an ASD element.
type AttributeSchema struct {
	Required []string
	Optional []string
}

var schemasV1 = map[string]AttributeSchema{
	"ast-deck":  {Required: []string{"schema"}, Optional: []string{"ratio", "theme", "lang"}},
	"ast-slide": {Required: []string{"id"}, Optional: []string{"title", "lang"}},
	"ast-text":  {Optional: []string{"id", "role", "x", "y", "w", "h", "inset", "font-token", "size", "weight", "color-token", "align", "alt", "decorative"}},
	"ast-shape": {Required: []string{"id", "kind", "x", "y", "w", "h"}, Optional: []string{"fill-token", "line-token", "alt", "decorative"}},
	"ast-image": {Required: []string{"id", "asset-ref", "x", "y", "w", "h"}, Optional: []string{"fit", "alt", "decorative"}},
	"ast-table": {Required: []string{"id", "data-ref", "x", "y", "w", "h"}, Optional: []string{"header", "style-token", "alt"}},
	"ast-chart": {Required: []string{"id", "kind", "data-ref", "x", "y", "w", "h"}, Optional: []string{"category-key", "value-keys", "style-token", "alt"}},
	"ast-group": {Required: []string{"id", "x", "y", "w", "h"}, Optional: []string{"alt", "decorative"}},
	"ast-notes": {},
	"script":    {Required: []string{"type", "id"}},
}

func SchemaV1(tag string) (AttributeSchema, bool) {
	s, ok := schemasV1[tag]
	return s, ok
}
