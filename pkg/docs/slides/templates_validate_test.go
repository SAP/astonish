package slides

import (
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// TestBuiltinTemplateArchetypesAreValidASD guards that every archetype skeleton
// shipped in a built-in template parses as valid ASD v2 with zero error-level
// diagnostics. This is the contract that lets SaveTemplate persist archetype
// markup directly.
func TestBuiltinTemplateArchetypesAreValidASD(t *testing.T) {
	for _, tmpl := range themes.ListTemplates() {
		for _, arch := range tmpl.Archetypes {
			_, diags, err := ParseSlide(arch.Markup)
			if err != nil {
				t.Fatalf("template %q kind %q: parse error: %v\nmarkup: %s", tmpl.Name, arch.Kind, err, arch.Markup)
			}
			if HasErrors(diags) {
				t.Fatalf("template %q kind %q: validation errors: %#v\nmarkup: %s", tmpl.Name, arch.Kind, diags, arch.Markup)
			}
		}
	}
}
