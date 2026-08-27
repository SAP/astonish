package themes

// StyleGuide is the LLM-consumable design-system documentation derived from an
// imported template. It provides the typography scale, spacing rhythm, color
// usage rules, layout patterns, and avoid-list that the LLM needs to generate
// on-brand content slides.
type StyleGuide struct {
	// TypographyScale defines the heading/body/caption hierarchy derived from
	// analyzing all placeholder font sizes across layouts.
	TypographyScale []TypeLevel `json:"typographyScale,omitempty"`
	// ColorRoles maps semantic roles (primary, accent, muted, surface, ink) to
	// hex colors with usage guidance.
	ColorRoles []ColorRole `json:"colorRoles,omitempty"`
	// SpacingSystem captures the derived spacing rhythm (base unit, margins, gaps).
	SpacingSystem *SpacingSystem `json:"spacingSystem,omitempty"`
	// LayoutPatterns describes the layout archetypes with guidance on when to use each.
	LayoutPatterns []LayoutPattern `json:"layoutPatterns,omitempty"`
	// AvoidList explicitly bans common AI defaults that conflict with this template's style.
	AvoidList []string `json:"avoidList,omitempty"`
	// FontPairing documents the display+body font combination with usage rules.
	FontPairing *FontPairing `json:"fontPairing,omitempty"`
	// ComponentPatterns describes how to fill each flexible content archetype.
	// The LLM uses this to select the right layout and fill only the designated slots.
	ComponentPatterns []ComponentPattern `json:"componentPatterns,omitempty"`
	// Markdown is the full human/LLM-readable style guide as a markdown document
	// (the primary artifact the LLM sees in context).
	Markdown string `json:"markdown,omitempty"`
}

// TypeLevel represents one level in the typography hierarchy.
type TypeLevel struct {
	Role     string `json:"role"`           // h1, h2, h3, body, caption, label, eyebrow
	FontSize int    `json:"fontSize"`       // logical px on 1920x1080 canvas
	Weight   string `json:"weight"`         // normal, 500, 600, 700, 800
	Font     string `json:"font"`           // font family (display or body)
	Color    string `json:"color,omitempty"` // typical color for this level
	Usage    string `json:"usage,omitempty"` // when to use this level
}

// ColorRole maps a semantic role to a hex color with usage guidance.
type ColorRole struct {
	Name  string `json:"name"`            // primary, accent, accent2, surface, ink, muted
	Color string `json:"color"`           // #RRGGBB
	Usage string `json:"usage"`           // description of where to use
	Limit string `json:"limit,omitempty"` // usage constraint (e.g. "max once per slide")
}

// SpacingSystem captures the derived spacing rhythm.
type SpacingSystem struct {
	PageMarginX   int `json:"pageMarginX"`   // left/right margin in px
	PageMarginY   int `json:"pageMarginY"`   // top/bottom margin in px
	SectionGap    int `json:"sectionGap"`    // gap between major sections
	ElementGap    int `json:"elementGap"`    // gap between related elements
	TitleBodyGap  int `json:"titleBodyGap"`  // gap between title and body content
	ContentStartY int `json:"contentStartY"` // typical Y where body content begins
}

// LayoutPattern describes a layout archetype with guidance on when to use it.
type LayoutPattern struct {
	Kind        string `json:"kind"`              // from archetype kind
	Name        string `json:"name"`              // human label
	Description string `json:"description"`       // when to use this layout
	Columns     int    `json:"columns,omitempty"` // detected column count
}

// FontPairing documents the display+body font combination with usage rules.
type FontPairing struct {
	DisplayFont  string `json:"displayFont"`        // headline font
	BodyFont     string `json:"bodyFont"`           // body text font
	MonoFont     string `json:"monoFont,omitempty"` // code/data font if detected
	DisplayUsage string `json:"displayUsage"`       // when to use display font
	BodyUsage    string `json:"bodyUsage"`          // when to use body font
}

// ComponentPattern describes one flexible content archetype's visual structure
// and how to fill it. This gives the LLM concrete per-layout authoring guidance.
type ComponentPattern struct {
	ArchetypeLabel string   `json:"archetypeLabel"`       // The archetype title/label (real PowerPoint layout name)
	Kind           string   `json:"kind"`                 // archetype kind (content, content-2, etc.)
	VisualSummary  string   `json:"visualSummary"`        // 1-2 sentence description of what the layout LOOKS like
	FillSlots      []string `json:"fillSlots"`            // element IDs the LLM may change
	UsageRule      string   `json:"usageRule"`            // When to pick this layout (content signal)
	ChromeNote     string   `json:"chromeNote,omitempty"` // What chrome elements exist and must be preserved
}
