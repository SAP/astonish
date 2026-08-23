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

func TestRegistryV2ChildrenAndRun(t *testing.T) {
	t.Parallel()
	run, ok := LookupV1("ast-run")
	if !ok {
		t.Fatal("missing ast-run")
	}
	if run.PPTXFidelity != FidelityNative {
		t.Errorf("ast-run fidelity = %q", run.PPTXFidelity)
	}
	text, _ := LookupV1("ast-text")
	if !childAllowed(text, "ast-run") {
		t.Error("ast-text must allow ast-run child")
	}
	shape, _ := LookupV1("ast-shape")
	if !childAllowed(shape, "script") {
		t.Error("ast-shape must allow script child for gradients")
	}
}

func childAllowed(def Definition, child string) bool {
	for _, c := range def.AllowedChildren {
		if c == child {
			return true
		}
	}
	return false
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
