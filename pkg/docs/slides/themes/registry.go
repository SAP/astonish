package themes

import (
	"fmt"
	"sort"
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
	Model *TemplateModel `json:"templateModel,omitempty"`
}

// ThemeTokens returns the design-token palette for the template.
func (t Template) ThemeTokens() map[string]string { return t.Tokens }

// archetypesFor builds the three standard archetypes (title, section, content)
// from a palette: bg is the surface color, ink is the primary text color, and
// accent is the section/heading color. titleMarkup, when non-empty, overrides
// the generated title archetype (used by templates with a gradient background).
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

// auroraTitle is a gradient-backed title archetype for the colorful "aurora"
// template. The gradient fill is expressed as a JSON script child of the
// full-canvas background shape, per the ASD v2 gradient schema.
const auroraTitle = `<ast-slide id="title">` +
	`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" alt="" decorative="true">` +
	`<script type="application/json" id="bg-gradient">{"kind":"linear","angle":90,"stops":[{"pos":0,"color":"#6D28D9"},{"pos":100,"color":"#0EA5E9"}]}</script>` +
	`</ast-shape>` +
	`<ast-text id="title-heading" x="160" y="380" w="1600" h="220" color="#F8FAFC" weight="bold" size="80" align="center">{{TITLE}}</ast-text>` +
	`<ast-text id="title-subtitle" x="160" y="620" w="1600" h="140" color="#E0F2FE" size="40" align="center">{{BODY}}</ast-text>` +
	`</ast-slide>`

var builtinTemplates = map[string]Template{
	"light-corporate": {
		Schema:      2,
		Name:        "light-corporate",
		Label:       "Light Corporate",
		Description: "Clean, high-contrast light theme for business decks.",
		Tokens:      map[string]string{"surface": "#FFFFFF", "ink": "#172033", "accent": "#1E40AF"},
		Archetypes:  archetypesFor("#FFFFFF", "#172033", "#1E40AF", ""),
	},
	"midnight": {
		Schema:      2,
		Name:        "midnight",
		Label:       "Midnight",
		Description: "Dark theme with warm amber accents for low-light rooms.",
		Tokens:      map[string]string{"surface": "#0B1220", "ink": "#E2E8F0", "accent": "#F59E0B"},
		Archetypes:  archetypesFor("#0B1220", "#E2E8F0", "#F59E0B", ""),
	},
	"aurora": {
		Schema:      2,
		Name:        "aurora",
		Label:       "Aurora",
		Description: "Colorful gradient theme for vibrant, modern presentations.",
		Tokens:      map[string]string{"surface": "#0F172A", "ink": "#F8FAFC", "accent": "#0EA5E9"},
		Archetypes:  archetypesFor("#0F172A", "#F8FAFC", "#0EA5E9", auroraTitle),
	},
}

// ArchetypesFor builds the three standard archetypes (title, section, content)
// from a palette using the default (non-gradient) title layout. It is a thin
// exported wrapper over the internal archetypesFor so template-management code
// outside this package (e.g. the recolor handler) can regenerate archetypes
// from a new palette without duplicating the markup logic.
func ArchetypesFor(bg, ink, accent string) []Archetype {
	return archetypesFor(bg, ink, accent, "")
}

// LookupTemplate returns the built-in template with the given name.
func LookupTemplate(name string) (Template, bool) {
	t, ok := builtinTemplates[name]
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
