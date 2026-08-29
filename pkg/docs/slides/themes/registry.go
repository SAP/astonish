package themes

import (
	"fmt"
	"sort"
	"strings"
)

type Theme struct {
	Schema int               `json:"schema"`
	Name   string            `json:"name"`
	Tokens map[string]string `json:"tokens"`
}

var builtin = map[string]Theme{
	"light-corporate": {Schema: 1, Name: "light-corporate", Tokens: map[string]string{"surface": "#FFFFFF", "ink": "#172033", "accent": "#1E40AF"}},
}

func Lookup(name string) (Theme, error) {
	theme, ok := builtin[name]
	if !ok {
		return Theme{}, fmt.Errorf("unknown slides theme %q", name)
	}
	return theme, nil
}

// Archetype is a reusable slide skeleton within a Template. Kind is one of
// title|section|content. Markup is a valid ASD v2 ast-slide fragment that uses
// the {{TITLE}} and {{BODY}} placeholders for author-supplied text.
//
// Tier and FillSlots are optional metadata that describe how faithfully the
// archetype must be reproduced. Tier is either "fixed" (the brand-chrome layout
// is reproduced verbatim; only the text inside FillSlots may be substituted) or
// "flexible" (a content layout the author may freely adapt). FillSlots lists the
// ast-text element ids that carry the {{TITLE}}/{{BODY}} placeholders and are
// therefore safe to replace with author-supplied text. Both fields are zero for
// built-in archetypes.
type Archetype struct {
	Kind      string   `json:"kind"`
	Title     string   `json:"title,omitempty"`
	Markup    string   `json:"markup"`
	Tier      string   `json:"tier,omitempty"`
	FillSlots []string `json:"fillSlots,omitempty"`
	// SlotHints describe each fill slot (id, role, short hint) so fill_slide
	// callers know what to put in ph-2 vs ph-3 without reading the markup.
	SlotHints []SlotHint `json:"slotHints,omitempty"`
	// ThumbnailRef names the deck Assets key (e.g. "thumb/title", "thumb/section-2")
	// holding a pre-baked PNG data URI rendered once at .pptx import time. It is
	// zero for built-ins and for imports made before the static-thumbnail pipeline
	// existed; those fall back to a live ast-deck render.
	ThumbnailRef string `json:"thumbnailRef,omitempty"`
}

// SlotHint is a compact description of one fill slot on an archetype.
type SlotHint struct {
	ID   string `json:"id"`
	Role string `json:"role,omitempty"` // title|body|image|heading|caption
	Hint string `json:"hint,omitempty"`
}

// Template is a named collection of design tokens plus slide archetypes. It is
// the schema-v2 evolution of Theme: Tokens carries the same palette while
// Archetypes describe ready-to-fill slide skeletons. Scope is set to "scope"
// for templates reconstructed from a scoped store (vs. built-ins).
type Template struct {
	Schema      int               `json:"schema"`
	Name        string            `json:"name"`
	Label       string            `json:"label,omitempty"`
	Description string            `json:"description,omitempty"`
	Tokens      map[string]string `json:"tokens"`
	Assets      map[string]string `json:"assets,omitempty"`
	Archetypes  []Archetype       `json:"archetypes"`
	Scope       string            `json:"scope,omitempty"`
	// Model is the lossless imported template IR. It is populated only for
	// high-fidelity imported templates (SchemaV3) and is nil for built-ins and
	// plain scoped templates. It is persisted verbatim so a future in-browser
	// editor / high-fidelity re-export has complete input; the Archetypes are
	// its rendered ASD projection.
	Model      *TemplateModel `json:"templateModel,omitempty"`
	StyleGuide *StyleGuide    `json:"styleGuide,omitempty"`
	// Skin selects the visual language for recipe layouts: "corporate" (logo,
	// legal, accent rule) or "product" (dark canvas, mono chrome, panels).
	// Empty means corporate.
	Skin string `json:"skin,omitempty"`
	// Palettes are named token overlays (surface/ink/accent/muted) for the
	// same template. Built-in classic (Light / Midnight / Aurora) and modern
	// ship colorways; imported brand templates typically have none — their
	// color is the brand.
	Palettes []Palette `json:"palettes,omitempty"`
}

// Palette is a named colorway on a Template. Skin, furniture, and fonts stay
// put; only the listed tokens change.
type Palette struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Tokens map[string]string `json:"tokens"`
}

// PaletteByID returns the named colorway, if the template defines it.
func (t Template) PaletteByID(id string) (Palette, bool) {
	want := strings.TrimSpace(id)
	if want == "" {
		return Palette{}, false
	}
	for _, p := range t.Palettes {
		if p.ID == want {
			return p, true
		}
	}
	return Palette{}, false
}

// ThemeTokens returns the design-token palette for the template.
func (t Template) ThemeTokens() map[string]string { return t.Tokens }

// archetypesFor builds the three standard archetypes (title, section, content)
// from a palette: bg is the surface color, ink is the primary text color, and
// accent is the section/heading color. titleMarkup, when non-empty, overrides
// the generated title archetype.
func archetypesFor(bg, ink, accent, titleMarkup string) []Archetype {
	title := titleMarkup
	if title == "" {
		title = `<ast-slide id="title">` +
			`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="` + bg + `" alt="" decorative="true"></ast-shape>` +
			`<ast-text id="title-heading" x="160" y="380" w="1600" h="220" color="` + ink + `" weight="bold" size="80" align="center">{{TITLE}}</ast-text>` +
			`<ast-text id="title-subtitle" x="160" y="620" w="1600" h="140" color="` + accent + `" size="40" align="center">{{BODY}}</ast-text>` +
			`</ast-slide>`
	}
	section := `<ast-slide id="section">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="` + bg + `" alt="" decorative="true"></ast-shape>` +
		`<ast-shape id="rule" kind="rect" x="160" y="560" w="480" h="12" fill="` + accent + `" alt="" decorative="true"></ast-shape>` +
		`<ast-text id="section-heading" x="160" y="380" w="1600" h="180" color="` + accent + `" weight="bold" size="64">{{TITLE}}</ast-text>` +
		`<ast-text id="section-body" x="160" y="600" w="1600" h="160" color="` + ink + `" size="36">{{BODY}}</ast-text>` +
		`</ast-slide>`
	content := `<ast-slide id="content">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="` + bg + `" alt="" decorative="true"></ast-shape>` +
		`<ast-text id="content-heading" x="160" y="120" w="1600" h="140" color="` + accent + `" weight="bold" size="56">{{TITLE}}</ast-text>` +
		`<ast-text id="content-body" x="160" y="320" w="1600" h="600" color="` + ink + `" size="36">{{BODY}}</ast-text>` +
		`</ast-slide>`
	return []Archetype{
		{Kind: "title", Title: "Title", Markup: title},
		{Kind: "section", Title: "Section", Markup: section},
		{Kind: "content", Title: "Content", Markup: content},
	}
}

var builtinTemplates = map[string]Template{
	"classic": {
		Schema: 2,
		Name:   "classic",
		Label:  "Classic",
		Description: "Corporate recipe language (logo, legal, accent rule) with Light, Midnight, " +
			"and Aurora colorways. Same jobs as Modern, different furniture.",
		Skin:       "corporate",
		Tokens:     map[string]string{"surface": "#FFFFFF", "ink": "#172033", "accent": "#1E40AF"},
		Archetypes: archetypesFor("#FFFFFF", "#172033", "#1E40AF", ""),
		Palettes:   classicPalettes(),
	},
	"modern": {
		Schema: 2,
		Name:   "modern",
		Label:  "Modern",
		Description: "Modern product language: one accent, monospace chrome, terminal furniture, and colorways " +
			"(dark and light). Same recipe jobs as other templates, different visual language.",
		Skin: "product",
		Tokens: map[string]string{
			"surface":     "#0B0D0F",
			"ink":         "#ECEDEE",
			"accent":      "#8B5CF6",
			"muted":       "#94A3B8",
			"displayFont": "Manrope",
			"bodyFont":    "Manrope",
			"monoFont":    "JetBrains Mono",
			// Declares which faces this template needs. Present/export load
			// exactly this list (files are filled from the bundled library).
			"embedded-fonts": modernEmbeddedFontsJSON,
		},
		Archetypes: archetypesFor("#0B0D0F", "#ECEDEE", "#8B5CF6", ""),
		Palettes:   productPalettes(),
	},
}

// modernEmbeddedFontsJSON is the Modern template's font declaration: family,
// CSS weight, and the Assets key present/export will resolve. Other templates
// omit this key and no extra faces are loaded.
const modernEmbeddedFontsJSON = `[{"family":"Manrope","variant":"400","assetKey":"font:Manrope:400"},{"family":"Manrope","variant":"500","assetKey":"font:Manrope:500"},{"family":"Manrope","variant":"600","assetKey":"font:Manrope:600"},{"family":"Manrope","variant":"700","assetKey":"font:Manrope:700"},{"family":"Manrope","variant":"800","assetKey":"font:Manrope:800"},{"family":"JetBrains Mono","variant":"400","assetKey":"font:JetBrains Mono:400"},{"family":"JetBrains Mono","variant":"600","assetKey":"font:JetBrains Mono:600"}]`

func classicPalettes() []Palette {
	return []Palette{
		{ID: "light", Label: "Light", Tokens: map[string]string{
			"surface": "#FFFFFF", "ink": "#172033", "accent": "#1E40AF",
		}},
		{ID: "midnight", Label: "Midnight", Tokens: map[string]string{
			"surface": "#0B1220", "ink": "#E2E8F0", "accent": "#F59E0B",
		}},
		{ID: "aurora", Label: "Aurora", Tokens: map[string]string{
			"surface": "#0F172A", "ink": "#F8FAFC", "accent": "#0EA5E9",
		}},
	}
}

func productPalettes() []Palette {
	dark := func(id, label, accent string) Palette {
		return Palette{ID: id, Label: label, Tokens: map[string]string{
			"surface": "#0B0D0F", "ink": "#ECEDEE", "accent": accent, "muted": "#94A3B8",
		}}
	}
	return []Palette{
		dark("violet", "Violet", "#8B5CF6"),
		dark("orange", "Orange", "#F97316"),
		dark("teal", "Teal", "#14B8A6"),
		dark("blue", "Blue", "#3B82F6"),
		dark("rose", "Rose", "#F43F5E"),
		{ID: "light-violet", Label: "Light violet", Tokens: map[string]string{
			"surface": "#F7F5FF", "ink": "#1A1228", "accent": "#7C3AED", "muted": "#64748B",
		}},
		{ID: "light-orange", Label: "Light orange", Tokens: map[string]string{
			"surface": "#FFF7ED", "ink": "#1C1917", "accent": "#EA580C", "muted": "#78716C",
		}},
		{ID: "editorial", Label: "Editorial", Tokens: map[string]string{
			"surface": "#FAFAF8", "ink": "#171717", "accent": "#171717", "muted": "#525252",
		}},
	}
}

// Former standalone built-ins, now colorways of classic. LookupTemplate and
// create_deck still accept these names so older decks and prompts keep working.
var builtinTemplateAliases = map[string]string{
	"aurora":          "classic",
	"midnight":        "classic",
	"light-corporate": "classic",
}

var builtinAliasPalettes = map[string]string{
	"aurora":          "aurora",
	"midnight":        "midnight",
	"light-corporate": "light",
}

// CanonicalTemplateName maps a built-in alias (aurora, midnight, light-corporate)
// to classic; other names are returned unchanged.
func CanonicalTemplateName(name string) string {
	name = strings.TrimSpace(name)
	if canon, ok := builtinTemplateAliases[name]; ok {
		return canon
	}
	return name
}

// AliasPaletteID is the classic colorway implied by an old template name
// (midnight → midnight). Empty when the name is not an alias.
func AliasPaletteID(name string) string {
	return builtinAliasPalettes[strings.TrimSpace(name)]
}

// ArchetypesFor builds the three standard archetypes (title, section, content)
// from a palette using the default (non-gradient) title layout. It is a thin
// exported wrapper over the internal archetypesFor so template-management code
// outside this package (e.g. the recolor handler) can regenerate archetypes
// from a new palette without duplicating the markup logic.
func ArchetypesFor(bg, ink, accent string) []Archetype {
	return archetypesFor(bg, ink, accent, "")
}

// LookupTemplate returns the built-in template with the given name. Former
// aurora / midnight / light-corporate names resolve to classic.
func LookupTemplate(name string) (Template, bool) {
	t, ok := builtinTemplates[CanonicalTemplateName(name)]
	return t, ok
}

// ListTemplates returns all built-in templates in deterministic (name) order.
func ListTemplates() []Template {
	out := make([]Template, 0, len(builtinTemplates))
	for _, t := range builtinTemplates {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
