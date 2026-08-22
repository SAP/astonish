package slides

import "testing"

func TestParseSlideRejectsExecutableContent(t *testing.T) {
	cases := []string{
		`<ast-slide id="x"><script>alert(1)</script></ast-slide>`,
		`<ast-slide id="x"><ast-text id="t" x="0" y="0" w="10" h="10" onclick="x">bad</ast-text></ast-slide>`,
		`<ast-slide id="x"><iframe src="https://example.com"></iframe></ast-slide>`,
	}
	for _, markup := range cases {
		if _, _, err := ParseSlide(markup); err == nil {
			t.Fatalf("expected rejection for %s", markup)
		}
	}
}
func TestParseSlideNormalizesAllowedMarkup(t *testing.T) {
	slide, diags, err := ParseSlide(`<ast-slide id="s" title="Hello"><ast-text id="t" x="10" y="20" w="300" h="40">Hi</ast-text><ast-notes>Speak</ast-notes></ast-slide>`)
	if err != nil {
		t.Fatal(err)
	}
	if HasErrors(diags) {
		t.Fatalf("unexpected diagnostics: %#v", diags)
	}
	if slide.ID != "s" || slide.Notes != "Speak" || len(slide.Nodes) != 1 {
		t.Fatalf("unexpected slide: %#v", slide)
	}
}
