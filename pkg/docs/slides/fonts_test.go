package slides

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseEmbeddedFonts(t *testing.T) {
	refs := []EmbeddedFontRef{
		{Family: "72 Brand", Variant: "regular", AssetKey: "font:72 Brand:regular"},
		{Family: "72 Brand", Variant: "bold", AssetKey: "font:72 Brand:bold"},
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	theme := map[string]string{
		"display":             "72 Brand Medium",
		"body-font":           "72 Brand",
		embeddedFontsThemeKey: string(raw),
	}
	got := parseEmbeddedFonts(theme)
	if len(got) != 2 {
		t.Fatalf("want 2 refs, got %d: %+v", len(got), got)
	}
	if got[0].Family != "72 Brand" || got[0].Variant != "regular" || got[0].AssetKey != "font:72 Brand:regular" {
		t.Errorf("unexpected first ref: %+v", got[0])
	}
	if got[1].Variant != "bold" {
		t.Errorf("unexpected second ref: %+v", got[1])
	}
}

func TestParseEmbeddedFontsAbsentOrInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"nil theme":              nil,
		"no key":                 {"display": "72 Brand"},
		"empty value":            {embeddedFontsThemeKey: ""},
		"invalid json":           {embeddedFontsThemeKey: "not-json"},
		"empty array":            {embeddedFontsThemeKey: "[]"},
		"entries missing fields": {embeddedFontsThemeKey: `[{"family":"","variant":"regular","assetKey":""}]`},
	}
	for name, theme := range cases {
		if got := parseEmbeddedFonts(theme); got != nil {
			t.Errorf("%s: want nil, got %+v", name, got)
		}
	}
}

func TestWriteFontFacesEmitsRule(t *testing.T) {
	refs := []EmbeddedFontRef{
		{Family: "72 Brand", Variant: "regular", AssetKey: "font:72 Brand:regular"},
		{Family: "72 Brand", Variant: "boldItalic", AssetKey: "font:72 Brand:boldItalic"},
	}
	raw, _ := json.Marshal(refs)
	theme := map[string]string{embeddedFontsThemeKey: string(raw)}
	assets := map[string]string{
		"font:72 Brand:regular":    "data:font/ttf;base64,AAEAAAA",
		"font:72 Brand:boldItalic": "data:font/otf;base64,T1RUTw",
	}
	var buf bytes.Buffer
	writeFontFaces(&buf, theme, assets)
	out := buf.String()

	if !strings.Contains(out, `@font-face{font-family:"72 Brand";src:url(data:font/ttf;base64,AAEAAAA) format("truetype");font-weight:400;font-style:normal;font-display:swap}`) {
		t.Errorf("regular @font-face missing/wrong; got:\n%s", out)
	}
	if !strings.Contains(out, `format("opentype")`) || !strings.Contains(out, `font-weight:700;font-style:italic`) {
		t.Errorf("boldItalic @font-face missing/wrong; got:\n%s", out)
	}
}

func TestWriteFontFacesGuards(t *testing.T) {
	mkTheme := func(refs []EmbeddedFontRef) map[string]string {
		raw, _ := json.Marshal(refs)
		return map[string]string{embeddedFontsThemeKey: string(raw)}
	}

	t.Run("no embedded-fonts key emits nothing", func(t *testing.T) {
		var buf bytes.Buffer
		writeFontFaces(&buf, map[string]string{"display": "72 Brand"}, map[string]string{"sha256-x": "data:image/png;base64,AAAA"})
		if buf.Len() != 0 {
			t.Errorf("want empty, got %q", buf.String())
		}
	})

	t.Run("missing asset skipped", func(t *testing.T) {
		var buf bytes.Buffer
		theme := mkTheme([]EmbeddedFontRef{{Family: "72 Brand", Variant: "regular", AssetKey: "font:72 Brand:regular"}})
		writeFontFaces(&buf, theme, map[string]string{}) // asset not present
		if buf.Len() != 0 {
			t.Errorf("want empty when asset absent, got %q", buf.String())
		}
	})

	t.Run("non-font data URI skipped", func(t *testing.T) {
		var buf bytes.Buffer
		theme := mkTheme([]EmbeddedFontRef{{Family: "X", Variant: "regular", AssetKey: "k"}})
		writeFontFaces(&buf, theme, map[string]string{"k": "data:image/png;base64,AAAA"})
		if buf.Len() != 0 {
			t.Errorf("want empty for non-font URI, got %q", buf.String())
		}
	})

	t.Run("numeric weight", func(t *testing.T) {
		var buf bytes.Buffer
		theme := mkTheme([]EmbeddedFontRef{{Family: "Manrope", Variant: "600", AssetKey: "k"}})
		writeFontFaces(&buf, theme, map[string]string{"k": "data:font/woff2;base64,AAAA"})
		if !strings.Contains(buf.String(), "font-weight:600") {
			t.Errorf("want weight 600, got %q", buf.String())
		}
	})

	t.Run("unsafe family name skipped", func(t *testing.T) {
		var buf bytes.Buffer
		theme := mkTheme([]EmbeddedFontRef{{Family: `bad"</style>`, Variant: "regular", AssetKey: "k"}})
		writeFontFaces(&buf, theme, map[string]string{"k": "data:font/ttf;base64,AAAA"})
		if buf.Len() != 0 {
			t.Errorf("want empty for unsafe family, got %q", buf.String())
		}
	})
}
