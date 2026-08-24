package components

import "testing"

func TestSchemaV1(t *testing.T) {
	s, ok := SchemaV1("ast-image")
	if !ok || len(s.Required) == 0 {
		t.Fatalf("unexpected schema: %#v", s)
	}
	if _, ok := SchemaV1("iframe"); ok {
		t.Fatal("iframe must not be registered")
	}
}

func TestSchemaV2Superset(t *testing.T) {
	// v2 fidelity attributes must be present as optional on the relevant tags.
	shape, _ := SchemaV1("ast-shape")
	for _, want := range []string{"rot", "fill", "line", "line-dash", "head-end", "tail-end", "geom", "path", "opacity", "fill-token", "line-token", "flip-h", "flip-v"} {
		if !contains(shape.Optional, want) {
			t.Errorf("ast-shape missing optional attr %q", want)
		}
	}
	image, _ := SchemaV1("ast-image")
	for _, want := range []string{"fit", "rot", "opacity", "flip-h", "flip-v"} {
		if !contains(image.Optional, want) {
			t.Errorf("ast-image missing optional attr %q", want)
		}
	}
	text, _ := SchemaV1("ast-text")
	for _, want := range []string{"rot", "anchor", "wrap", "color", "font", "font-token", "color-token"} {
		if !contains(text.Optional, want) {
			t.Errorf("ast-text missing optional attr %q", want)
		}
	}
	if _, ok := SchemaV1("ast-run"); !ok {
		t.Fatal("ast-run must be registered")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
