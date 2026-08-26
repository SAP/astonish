package slides

import (
	"strings"
	"testing"
)

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

func TestParseSlideV2Attributes(t *testing.T) {
	markup := `<ast-slide id="s">` +
		`<ast-shape id="sh" kind="rect" x="100" y="100" w="600" h="400" rot="45" fill="#ff0000" line="#00ff00" line-dash="dash" head-end="arrow" geom="roundRect" opacity="0.5">` +
		`<script type="application/json" id="g">{"kind":"linear","angle":90,"stops":[{"pos":0,"color":"#000000"},{"pos":100,"color":"#ffffff"}]}</script>` +
		`</ast-shape>` +
		`<ast-text id="t" x="100" y="600" w="600" h="120" rot="10" anchor="ctr" color="#123456" font="Arial">` +
		`<ast-run b u color="#ff0000" font="Georgia" size="24">Bold</ast-run>` +
		`<ast-run i>Ital</ast-run>` +
		`</ast-text>` +
		`</ast-slide>`
	slide, diags, err := ParseSlide(markup)
	if err != nil {
		t.Fatal(err)
	}
	if HasErrors(diags) {
		t.Fatalf("unexpected diagnostics: %#v", diags)
	}
	if len(slide.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(slide.Nodes))
	}
	shape := slide.Nodes[0]
	if shape.Rot != 45 || shape.Fill != "#ff0000" || shape.Line != "#00ff00" || shape.Dash != "dash" || shape.Geom != "roundRect" || shape.Opacity != 0.5 {
		t.Fatalf("shape v2 attrs not parsed: %#v", shape)
	}
	if shape.Gradient == nil || shape.Gradient.Kind != "linear" || shape.Gradient.Angle != 90 || len(shape.Gradient.Stops) != 2 {
		t.Fatalf("gradient not parsed: %#v", shape.Gradient)
	}
	if shape.Gradient.Stops[1].Pos != 100 || shape.Gradient.Stops[1].Color != "#ffffff" {
		t.Fatalf("gradient stops wrong: %#v", shape.Gradient.Stops)
	}
	txt := slide.Nodes[1]
	if txt.Rot != 10 || txt.Props["anchor"] != "ctr" {
		t.Fatalf("text v2 attrs not parsed: %#v", txt)
	}
	if len(txt.Runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(txt.Runs))
	}
	if !txt.Runs[0].Bold || !txt.Runs[0].Underline || txt.Runs[0].Color != "#ff0000" || txt.Runs[0].Font != "Georgia" || txt.Runs[0].Size != 24 || txt.Runs[0].Text != "Bold" {
		t.Fatalf("run 0 wrong: %#v", txt.Runs[0])
	}
	if !txt.Runs[1].Italic || txt.Runs[1].Text != "Ital" {
		t.Fatalf("run 1 wrong: %#v", txt.Runs[1])
	}
}

func TestParseSlideV1CompatUnchanged(t *testing.T) {
	// A pure v1 slide must still parse with zero error diagnostics.
	markup := `<ast-slide id="s" title="T">` +
		`<ast-shape id="sh" kind="rect" x="100" y="100" w="800" h="400" fill-token="accent1" line-token="ink"><ast-text id="c" x="120" y="120" w="700" h="120" font-token="body" color-token="ink" size="18">Caption</ast-text></ast-shape>` +
		`<ast-image id="im" asset-ref="a1" x="1000" y="200" w="500" h="500" fit="contain" alt="pic"/>` +
		`</ast-slide>`
	slide, diags, err := ParseSlide(markup)
	if err != nil {
		t.Fatal(err)
	}
	if HasErrors(diags) {
		t.Fatalf("v1 slide produced errors: %#v", diags)
	}
	if len(slide.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(slide.Nodes))
	}
	if slide.Nodes[0].Props["fill-token"] != "accent1" {
		t.Fatalf("v1 token prop lost: %#v", slide.Nodes[0].Props)
	}
}

func TestRunFromHTML_PreservesSeparatorNewlines(t *testing.T) {
	// The model emits deliberate whitespace-only separator runs between bullet
	// items. The parser must preserve that text verbatim (no TrimSpace) so the
	// line-break signal survives to the renderers.
	markup := `<ast-slide id="s">` +
		`<ast-text id="t" x="100" y="100" w="600" h="400">` +
		`<ast-run b>Label:</ast-run>` +
		`<ast-run> text</ast-run>` +
		`<ast-run>` + "\n\n" + `</ast-run>` +
		`<ast-run b>Next:</ast-run>` +
		`</ast-text>` +
		`</ast-slide>`
	slide, diags, err := ParseSlide(markup)
	if err != nil {
		t.Fatal(err)
	}
	if HasErrors(diags) {
		t.Fatalf("unexpected diagnostics: %#v", diags)
	}
	if len(slide.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(slide.Nodes))
	}
	runs := slide.Nodes[0].Runs
	if len(runs) != 4 {
		t.Fatalf("expected 4 runs, got %d: %#v", len(runs), runs)
	}
	// Bold runs still parse with their content.
	if !runs[0].Bold || runs[0].Text != "Label:" {
		t.Fatalf("run 0 wrong: %#v", runs[0])
	}
	if !runs[3].Bold || runs[3].Text != "Next:" {
		t.Fatalf("run 3 wrong: %#v", runs[3])
	}
	// The separator run's newlines survived (not emptied by TrimSpace).
	foundSeparator := false
	for _, r := range runs {
		if strings.Contains(r.Text, "\n\n") {
			foundSeparator = true
		}
	}
	if !foundSeparator {
		t.Fatalf("separator newlines were stripped from runs: %#v", runs)
	}
}

func TestParseSlideRejectsBadV2Values(t *testing.T) {
	cases := []string{
		`<ast-shape id="s" kind="rect" x="0" y="0" w="1" h="1" rot="abc"/>`,
		`<ast-shape id="s" kind="rect" x="0" y="0" w="1" h="1" opacity="oops"/>`,
	}
	for _, inner := range cases {
		markup := `<ast-slide id="x">` + inner + `</ast-slide>`
		if _, _, err := ParseSlide(markup); err == nil {
			t.Fatalf("expected parse error for %s", inner)
		}
	}
}
