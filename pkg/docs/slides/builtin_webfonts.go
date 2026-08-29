package slides

import (
	"embed"
	"encoding/base64"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// Bundled latin webfonts used to *satisfy* a deck's font declaration
// (theme["embedded-fonts"]). They are not loaded unless the deck/template
// lists that family. Imported pptx fonts still come from the template assets.
//
//go:embed webfonts/*.woff2
var bundledWebFonts embed.FS

type bundledFace struct {
	file, family, variant string
}

var bundledFaces = []bundledFace{
	{"webfonts/manrope-400.woff2", "Manrope", "400"},
	{"webfonts/manrope-500.woff2", "Manrope", "500"},
	{"webfonts/manrope-600.woff2", "Manrope", "600"},
	{"webfonts/manrope-700.woff2", "Manrope", "700"},
	{"webfonts/manrope-800.woff2", "Manrope", "800"},
	{"webfonts/jetbrains-mono-400.woff2", "JetBrains Mono", "400"},
	{"webfonts/jetbrains-mono-600.woff2", "JetBrains Mono", "600"},
}

func bundledFontDataURI(family, variant string) string {
	for _, f := range bundledFaces {
		if !strings.EqualFold(f.family, family) || f.variant != strings.TrimSpace(variant) {
			continue
		}
		raw, err := bundledWebFonts.ReadFile(f.file)
		if err != nil || len(raw) == 0 {
			return ""
		}
		return "data:font/woff2;base64," + base64.StdEncoding.EncodeToString(raw)
	}
	return ""
}

// fillDeclaredFontAssets writes data: URIs into assets for every face the
// theme *declares* (embedded-fonts) that we have a bundled file for. Existing
// assets win (imported pptx bytes). Unknown declared families are left empty
// for writeFontFaces to skip.
func fillDeclaredFontAssets(theme, assets map[string]string) {
	if assets == nil {
		return
	}
	for _, ref := range parseEmbeddedFonts(theme) {
		if strings.TrimSpace(assets[ref.AssetKey]) != "" {
			continue
		}
		if uri := bundledFontDataURI(ref.Family, ref.Variant); uri != "" {
			assets[ref.AssetKey] = uri
		}
	}
}

// HydrateTemplateFonts copies tokens/assets and fills declared faces so
// create_deck can seed font: assets and present can emit @font-face.
func HydrateTemplateFonts(t themes.Template) themes.Template {
	t.Tokens = cloneStringMap(t.Tokens)
	t.Assets = cloneStringMap(t.Assets)
	if t.Assets == nil {
		t.Assets = map[string]string{}
	}
	fillDeclaredFontAssets(t.Tokens, t.Assets)
	if len(t.Assets) == 0 {
		t.Assets = nil
	}
	return t
}

// inheritDeclaredFontsFromTemplate copies embedded-fonts from the built-in
// named by theme["template-name"] when the deck itself has no declaration.
// Older session decks stamped displayFont but not the manifest still load
// the template's faces. A deck that already lists faces keeps that list —
// we never sniff displayFont or treat a family as globally required.
func inheritDeclaredFontsFromTemplate(theme map[string]string) {
	if theme == nil {
		return
	}
	if len(parseEmbeddedFonts(theme)) > 0 {
		return
	}
	name := strings.TrimSpace(theme[themeKeyTemplateName])
	if name == "" {
		return
	}
	tmpl, ok := themes.LookupTemplate(name)
	if !ok {
		return
	}
	raw := strings.TrimSpace(tmpl.Tokens[embeddedFontsThemeKey])
	if raw == "" {
		return
	}
	theme[embeddedFontsThemeKey] = raw
}
