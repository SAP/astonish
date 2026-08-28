package slides

import (
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// Skin IDs are stored on themes.Template.Skin. Corporate is the original
// recipe look (logo, legal, accent rule). Product is the dark developer-tool
// language (mono rails, one accent, panels, terminals).
const (
	SkinCorporate = "corporate"
	SkinProduct   = "product"
)

// Skin is the visual language of a template: tokens, type, chrome grammar, and
// emphasis rules. Layout types (jobs) are shared; a skin decides how they paint.
type Skin struct {
	ID string

	Surface, Panel, Inset, Line       string
	Ink, InkMute, InkDim              string
	Accent, AccentSoft, AccentFill    string
	AccentEdge, AccentGlow            string
	DisplayFont, BodyFont, MonoFont   string
	MarginX, MarginY                  int
	HeroSize, H2Size, CardTitle, Stat int
	BodySize, ChromeSize, EyebrowSize int
	HeadlineAccentSpan                bool
}

func CorporateSkin(tokens map[string]string) Skin {
	s := Skin{
		ID:                 SkinCorporate,
		Surface:            "#FFFFFF",
		Ink:                "#172033",
		Accent:             "#1E40AF",
		InkMute:            "",
		DisplayFont:        "",
		BodyFont:           "",
		MonoFont:           "",
		MarginX:            120,
		MarginY:            72,
		HeroSize:           92,
		H2Size:             56,
		CardTitle:          32,
		Stat:               64,
		BodySize:           22,
		ChromeSize:         13,
		EyebrowSize:        15,
		HeadlineAccentSpan: true,
	}
	applyTokenOverrides(&s, tokens)
	if s.InkMute == "" {
		s.InkMute = mixHex(s.Ink, s.Surface, 0.22)
	}
	s.InkDim = mixHex(s.Ink, s.Surface, 0.45)
	s.Panel = mixHex(s.Surface, s.Ink, 0.08)
	s.Inset = mixHex(s.Surface, s.Ink, 0.12)
	s.Line = mixHex(s.Ink, s.Surface, 0.82)
	s.AccentFill = mixHex(s.Surface, s.Accent, 0.12)
	s.AccentEdge = s.Accent
	s.AccentSoft = mixHex(s.Accent, s.Surface, 0.25)
	s.AccentGlow = s.Accent
	if s.MonoFont == "" {
		s.MonoFont = s.BodyFont
	}
	return s
}

func ProductSkin(tokens map[string]string) Skin {
	s := Skin{
		ID:                 SkinProduct,
		Surface:            "#0B0D0F",
		Panel:              "#15181C",
		Inset:              "#0B0D0F",
		Line:               "#2A3038",
		Ink:                "#ECEDEE",
		InkMute:            "#94A3B8",
		InkDim:             "#5B6470",
		Accent:             "#8B5CF6",
		AccentSoft:         "#A78BFA",
		AccentFill:         "#1B1430",
		AccentEdge:         "#6D4AB8",
		AccentGlow:         "#8B5CF6",
		DisplayFont:        "Manrope",
		BodyFont:           "Manrope",
		MonoFont:           "JetBrains Mono",
		MarginX:            96,
		MarginY:            64,
		HeroSize:           120,
		H2Size:             64,
		CardTitle:          30,
		Stat:               72,
		BodySize:           18,
		ChromeSize:         14,
		EyebrowSize:        16,
		HeadlineAccentSpan: true,
	}
	applyTokenOverrides(&s, tokens)
	deriveProductFurniture(&s)
	return s
}

// deriveProductFurniture rebuilds panel/line/accent-fill from the current
// surface/ink/accent so a palette swap (orange, light, editorial) actually
// recolors the furniture instead of leaving the default violet fills.
func deriveProductFurniture(s *Skin) {
	s.Panel = mixHex(s.Surface, s.Ink, 0.08)
	s.Inset = mixHex(s.Surface, s.Ink, 0.03)
	s.Line = mixHex(s.Ink, s.Surface, 0.82)
	s.InkDim = mixHex(s.Ink, s.Surface, 0.45)
	if s.InkMute == "" {
		s.InkMute = mixHex(s.Ink, s.Surface, 0.28)
	}
	s.AccentFill = mixHex(s.Surface, s.Accent, 0.22)
	s.AccentEdge = mixHex(s.Accent, s.Surface, 0.18)
	s.AccentSoft = mixHex(s.Accent, s.Surface, 0.25)
	s.AccentGlow = s.Accent
}

func applyTokenOverrides(s *Skin, tokens map[string]string) {
	if tokens == nil {
		return
	}
	if v := strings.TrimSpace(tokens["surface"]); v != "" {
		s.Surface = v
	}
	if v := strings.TrimSpace(tokens["ink"]); v != "" {
		s.Ink = v
	}
	if v := strings.TrimSpace(tokens["accent"]); v != "" {
		s.Accent = v
	}
	if v := strings.TrimSpace(tokens["muted"]); v != "" {
		s.InkMute = v
	}
	if v := strings.TrimSpace(tokens["displayFont"]); v != "" {
		s.DisplayFont = v
	}
	if v := strings.TrimSpace(tokens["bodyFont"]); v != "" {
		s.BodyFont = v
	}
	if v := strings.TrimSpace(tokens["monoFont"]); v != "" {
		s.MonoFont = v
	}
}

// SkinFor picks the visual language for a template. Built-in "product" (and
// explicit Skin=product) get the dark developer-tool language; everything else
// — including imported corporate decks — uses the original corporate recipes.
func SkinFor(tmpl themes.Template) Skin {
	id := strings.ToLower(strings.TrimSpace(tmpl.Skin))
	if id == "" {
		id = strings.ToLower(strings.TrimSpace(tmpl.Name))
	}
	switch id {
	case SkinProduct, "devtool", "product-dark":
		return ProductSkin(tmpl.Tokens)
	default:
		return CorporateSkin(tmpl.Tokens)
	}
}

func (s Skin) IsProduct() bool { return s.ID == SkinProduct }
