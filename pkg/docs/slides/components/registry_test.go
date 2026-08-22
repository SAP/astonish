package components

import "testing"

func TestRegistryV1CoreComponentsAreNative(t *testing.T) {
	t.Parallel()
	for _, tag := range []string{"ast-text", "ast-shape", "ast-image", "ast-table", "ast-chart", "ast-group", "ast-notes"} {
		def, ok := LookupV1(tag)
		if !ok {
			t.Fatalf("missing %s", tag)
		}
		if def.PPTXFidelity != FidelityNative {
			t.Errorf("%s fidelity = %q", tag, def.PPTXFidelity)
		}
	}
}

func TestTagsV1Deterministic(t *testing.T) {
	t.Parallel()
	a, b := TagsV1(), TagsV1()
	if len(a) != len(b) {
		t.Fatal("registry changed between reads")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("tags are not deterministic: %v != %v", a, b)
		}
	}
}
