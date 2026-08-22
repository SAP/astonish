package tools

import "testing"

func TestFlowToolDeclarationsIncludeSlides(t *testing.T) {
	want := map[string]bool{
		"create_deck":   false,
		"write_slide":   false,
		"get_deck":      false,
		"list_decks":    false,
		"validate_deck": false,
	}
	for _, declaration := range GetAllFlowToolDeclarations() {
		if _, ok := want[declaration.Name]; ok {
			if declaration.Category != "slides" {
				t.Fatalf("%s category = %q, want slides", declaration.Name, declaration.Category)
			}
			want[declaration.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing %s declaration", name)
		}
	}
}
