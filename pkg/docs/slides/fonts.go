package slides

import (
	"bytes"
	"encoding/json"
	"strings"
)

// embeddedFontsThemeKey is the deck-theme map key for the font declaration:
// which faces this deck needs. Value is JSON []EmbeddedFontRef. Built-in
// templates set it on their tokens; the PPTX importer records recovered
// brand faces the same way (see import_worker.mjs collectEmbeddedFonts).
// It is never a --ast-* CSS variable; writeFontFaces emits @font-face from it.
const embeddedFontsThemeKey = "embedded-fonts"

// EmbeddedFontRef names one face the deck declared it needs: a family
// (e.g. "Manrope" or "72 Brand"), a variant (CSS weight like "400", or
// regular|bold|italic|boldItalic from pptx import), and the Assets-map key
// (e.g. "font:Manrope:400") whose value is a data:font/… payload.
type EmbeddedFontRef struct {
	Family   string `json:"family"`
	Variant  string `json:"variant"`
	AssetKey string `json:"assetKey"`
}

// parseEmbeddedFonts decodes the deck's font declaration. Nil when the theme
// has no embedded-fonts key or the value is not valid JSON — decks that name
// a family without declaring faces load nothing extra. Entries missing a
// family or asset key are dropped so the exporter never emits a malformed
// @font-face.
func parseEmbeddedFonts(theme map[string]string) []EmbeddedFontRef {
	if len(theme) == 0 {
		return nil
	}
	raw, ok := theme[embeddedFontsThemeKey]
	if !ok || raw == "" {
		return nil
	}
	var refs []EmbeddedFontRef
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return nil
	}
	out := refs[:0]
	for _, r := range refs {
		if r.Family == "" || r.AssetKey == "" {
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fontVariantCSS maps an embedded-font style variant to its CSS font-weight and
// font-style. Unknown variants are treated as regular (400/normal).
func fontVariantCSS(variant string) (weight, style string) {
	v := strings.TrimSpace(variant)
	switch v {
	case "bold":
		return "700", "normal"
	case "italic":
		return "400", "italic"
	case "boldItalic":
		return "700", "italic"
	case "regular", "":
		return "400", "normal"
	}
	if v != "" && isNumericWeight(v) {
		return v, "normal"
	}
	return "400", "normal"
}

func isNumericWeight(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// fontFormatFor maps a data: font MIME to the CSS @font-face format() hint.
// Returns "" for an unrecognized/unsafe URI so the caller skips it.
func fontFormatFor(dataURI string) string {
	switch {
	case strings.HasPrefix(dataURI, "data:font/ttf"):
		return "truetype"
	case strings.HasPrefix(dataURI, "data:font/otf"):
		return "opentype"
	case strings.HasPrefix(dataURI, "data:font/woff2"):
		return "woff2"
	case strings.HasPrefix(dataURI, "data:font/woff"):
		return "woff"
	default:
		return ""
	}
}

// safeFontFamily reports whether a family name is safe to place inside a
// double-quoted CSS string in a @font-face rule. It rejects double quotes,
// backslashes, and control characters (which could break out of the string or
// the <style> element). Family names from PPTX are plain text (e.g. "72 Brand").
func safeFontFamily(family string) bool {
	if family == "" {
		return false
	}
	for _, r := range family {
		if r == '"' || r == '\\' || r == '<' || r == '>' || r < 0x20 {
			return false
		}
	}
	return true
}

// writeFontFaces emits an @font-face rule for every embedded font FACE the theme
// declares (via the embedded-fonts manifest) that has a matching data:font/ asset.
// This lets the browser resolve the concrete brand family names the theme sets on
// --ast-display / --ast-body-font instead of falling back to the default serif.
// It writes nothing when the theme has no embedded fonts, keeping existing decks
// and no-embed imports byte-identical. The data: src URIs are written directly
// (not through the theme-value sanitizer), so the family name and the data: prefix
// are validated defensively here.
func writeFontFaces(out *bytes.Buffer, theme map[string]string, assets map[string]string) {
	refs := parseEmbeddedFonts(theme)
	if len(refs) == 0 || len(assets) == 0 {
		return
	}
	for _, ref := range refs {
		if !safeFontFamily(ref.Family) {
			continue
		}
		dataURI := assets[ref.AssetKey]
		if dataURI == "" || !strings.HasPrefix(dataURI, "data:font/") {
			continue
		}
		format := fontFormatFor(dataURI)
		if format == "" {
			continue
		}
		weight, style := fontVariantCSS(ref.Variant)
		out.WriteString(`@font-face{font-family:"`)
		out.WriteString(ref.Family)
		out.WriteString(`";src:url(`)
		out.WriteString(dataURI)
		out.WriteString(`) format("`)
		out.WriteString(format)
		out.WriteString(`");font-weight:`)
		out.WriteString(weight)
		out.WriteString(`;font-style:`)
		out.WriteString(style)
		out.WriteString(`;font-display:swap}`)
	}
}
