package slides

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// Recipe kinds are layout-type skeletons themed from the active template's
// style guide. They are the default body (and cover/closer) catalog — imported
// pattern-* entries remain as an optional match when geometry equals the job.
const (
	RecipeCover          = "recipe-cover"
	RecipeSplitNarrative = "recipe-split-narrative"
	RecipeQuoteSplit     = "recipe-quote-split"
	RecipeTwoUp          = "recipe-two-up"
	RecipeThreeUp        = "recipe-three-up"
	RecipeStatRow        = "recipe-stat-row"
	RecipeNumberedGrid   = "recipe-numbered-grid"
	RecipeCalloutRail    = "recipe-callout-rail"
	RecipeYearHero       = "recipe-year-hero"
	RecipeCloser         = "recipe-closer"
)

// Chrome is brand furniture copied from the imported template onto every recipe.
// Empty fields are omitted. Page and DeckTitle are filled at fill_slides time.
type Chrome struct {
	LogoRef      string
	LogoW        int
	LogoH        int
	Legal        string
	Confidential string
	DeckTitle    string
	Page         int
}

type recipeMeta struct {
	Kind    string
	Title   string
	Summary string
	Slots   []themes.SlotHint // Role "optional" is not required
}

func allRecipeMeta() []recipeMeta {
	return []recipeMeta{
		{
			Kind:    RecipeCover,
			Title:   "Cover lockup",
			Summary: "Cover lockup — eyebrow, split display title, dek, 4 meta cells. Slide 0.",
			Slots: []themes.SlotHint{
				{ID: "eyebrow", Role: "label", Hint: "2–5 word kicker (uppercase)"},
				{ID: "headline", Role: "title", Hint: "Display title line 1"},
				{ID: "headline_2", Role: "optional", Hint: "Display title line 2 (optional)"},
				{ID: "dek", Role: "body", Hint: "One-sentence thesis, 12–22 words"},
				{ID: "meta_1_label", Role: "label", Hint: "Meta cell 1 label"},
				{ID: "meta_1_value", Role: "body", Hint: "Meta cell 1 value"},
				{ID: "meta_2_label", Role: "label", Hint: "Meta cell 2 label"},
				{ID: "meta_2_value", Role: "body", Hint: "Meta cell 2 value"},
				{ID: "meta_3_label", Role: "optional", Hint: "Meta cell 3 label (optional)"},
				{ID: "meta_3_value", Role: "optional", Hint: "Meta cell 3 value (optional)"},
				{ID: "meta_4_label", Role: "optional", Hint: "Meta cell 4 label (optional)"},
				{ID: "meta_4_value", Role: "optional", Hint: "Meta cell 4 value (optional)"},
			},
		},
		{
			Kind:    RecipeSplitNarrative,
			Title:   "Split narrative",
			Summary: "Chapter: eyebrow + date, split headline, left story, 3 stacked claims.",
			Slots:   headerSlotsPlus(append(bodySlots(2, true), itemSlots(3, false)...)),
		},
		{
			Kind:    RecipeQuoteSplit,
			Title:   "Quote split",
			Summary: "Turning point: left story, right pull-quote + attribution.",
			Slots: headerSlotsPlus(append(bodySlots(2, true),
				themes.SlotHint{ID: "quote", Role: "body", Hint: "Pull-quote, one or two sentences"},
				themes.SlotHint{ID: "attribution", Role: "caption", Hint: "Who said it, and when"},
			)),
		},
		{
			Kind:    RecipeTwoUp,
			Title:   "Two-up contrast",
			Summary: "Two equal columns — use for contrast, before/after, or a pair.",
			Slots:   headerSlotsPlus(columnSlots(2)),
		},
		{
			Kind:    RecipeThreeUp,
			Title:   "Three-up cards",
			Summary: "Three cards (kicker + title + paragraph). Use for a triad or short timeline.",
			Slots:   headerSlotsPlus(cardSlots(3)),
		},
		{
			Kind:    RecipeStatRow,
			Title:   "Stat row",
			Summary: "Takeaway headline plus 3–4 metric tiles (label + giant number + caption).",
			Slots: headerSlotsPlus([]themes.SlotHint{
				{ID: "stat_1_label", Role: "label", Hint: "Metric 1 label"},
				{ID: "stat_1_number", Role: "title", Hint: "Metric 1 number (short)"},
				{ID: "stat_1_caption", Role: "body", Hint: "Metric 1 caption, one sentence"},
				{ID: "stat_2_label", Role: "label", Hint: "Metric 2 label"},
				{ID: "stat_2_number", Role: "title", Hint: "Metric 2 number"},
				{ID: "stat_2_caption", Role: "body", Hint: "Metric 2 caption"},
				{ID: "stat_3_label", Role: "label", Hint: "Metric 3 label"},
				{ID: "stat_3_number", Role: "title", Hint: "Metric 3 number"},
				{ID: "stat_3_caption", Role: "body", Hint: "Metric 3 caption"},
				{ID: "stat_4_label", Role: "optional", Hint: "Metric 4 label (optional)"},
				{ID: "stat_4_number", Role: "optional", Hint: "Metric 4 number (optional)"},
				{ID: "stat_4_caption", Role: "optional", Hint: "Metric 4 caption (optional)"},
			}),
		},
		{
			Kind:    RecipeNumberedGrid,
			Title:   "Numbered grid",
			Summary: "2×3 principles (index is chrome). Title + sentence in each cell.",
			Slots:   headerSlotsPlus(itemSlots(6, false)),
		},
		{
			Kind:    RecipeCalloutRail,
			Title:   "Callout rail",
			Summary: "Left story, right accent lesson/ask card.",
			Slots: headerSlotsPlus(append(bodySlots(2, true),
				themes.SlotHint{ID: "callout_kicker", Role: "label", Hint: "Rail kicker (Lesson / The ask)"},
				themes.SlotHint{ID: "callout_title", Role: "heading", Hint: "Rail heading, 3–8 words"},
				themes.SlotHint{ID: "callout_body", Role: "body", Hint: "Rail takeaway, 12–22 words"},
			)),
		},
		{
			Kind:    RecipeYearHero,
			Title:   "Year hero",
			Summary: "Giant year/numeral plus 3 stacked claims.",
			Slots: append([]themes.SlotHint{
				{ID: "eyebrow", Role: "label", Hint: "2–5 word kicker"},
				{ID: "date", Role: "optional", Hint: "Right marker (optional)"},
				{ID: "headline", Role: "title", Hint: "Takeaway or split line 1"},
				{ID: "headline_2", Role: "optional", Hint: "Split line 2 (optional)"},
				{ID: "year", Role: "title", Hint: "Giant year or numeral (e.g. 1984)"},
			}, itemSlots(3, false)...),
		},
		{
			Kind:    RecipeCloser,
			Title:   "Closer",
			Summary: "Last slide: thesis paragraph plus 3 questions or takeaways.",
			Slots: append([]themes.SlotHint{
				{ID: "eyebrow", Role: "label", Hint: "Closer kicker"},
				{ID: "headline", Role: "title", Hint: "Takeaway title line 1"},
				{ID: "headline_2", Role: "optional", Hint: "Title line 2 (optional)"},
				{ID: "thesis", Role: "body", Hint: "Closing thesis, 40–80 words"},
			}, itemSlots(3, false)...),
		},
	}
}

func headerSlotsPlus(extra []themes.SlotHint) []themes.SlotHint {
	base := []themes.SlotHint{
		{ID: "eyebrow", Role: "label", Hint: "Chapter/section kicker, 2–5 words"},
		{ID: "date", Role: "optional", Hint: "Year range or right marker (optional)"},
		{ID: "headline", Role: "title", Hint: "Display title line 1"},
		{ID: "headline_2", Role: "optional", Hint: "Display title line 2 (optional)"},
	}
	return append(base, extra...)
}

func bodySlots(n int, secondOptional bool) []themes.SlotHint {
	out := make([]themes.SlotHint, 0, n)
	for i := 1; i <= n; i++ {
		h := themes.SlotHint{ID: fmt.Sprintf("body_%d", i), Role: "body", Hint: fmt.Sprintf("Left paragraph %d, ~40–70 words", i)}
		if i == 2 && secondOptional {
			h.Role = "optional"
			h.Hint = "Second paragraph (optional if body_1 is long)"
		}
		out = append(out, h)
	}
	return out
}

func itemSlots(n int, withKicker bool) []themes.SlotHint {
	out := make([]themes.SlotHint, 0, n*3)
	for i := 1; i <= n; i++ {
		if withKicker {
			out = append(out, themes.SlotHint{ID: fmt.Sprintf("item_%d_kicker", i), Role: "label", Hint: fmt.Sprintf("Item %d kicker", i)})
		}
		out = append(out,
			themes.SlotHint{ID: fmt.Sprintf("item_%d_title", i), Role: "heading", Hint: fmt.Sprintf("Item %d heading, 1–6 words", i)},
			themes.SlotHint{ID: fmt.Sprintf("item_%d_body", i), Role: "body", Hint: fmt.Sprintf("Item %d complete sentence, 12–22 words", i)},
		)
	}
	return out
}

func cardSlots(n int) []themes.SlotHint {
	return itemSlots(n, true)
}

func columnSlots(n int) []themes.SlotHint {
	out := make([]themes.SlotHint, 0, n*3)
	for i := 1; i <= n; i++ {
		out = append(out,
			themes.SlotHint{ID: fmt.Sprintf("col_%d_kicker", i), Role: "label", Hint: fmt.Sprintf("Column %d kicker", i)},
			themes.SlotHint{ID: fmt.Sprintf("col_%d_title", i), Role: "heading", Hint: fmt.Sprintf("Column %d heading", i)},
			themes.SlotHint{ID: fmt.Sprintf("col_%d_body", i), Role: "body", Hint: fmt.Sprintf("Column %d paragraph, ~40 words", i)},
		)
	}
	return out
}

func requiredFillSlots(slots []themes.SlotHint) []string {
	var out []string
	for _, s := range slots {
		if strings.EqualFold(s.Role, "optional") {
			continue
		}
		out = append(out, s.ID)
	}
	return out
}

func isRecipeKind(kind string) bool {
	return strings.HasPrefix(strings.TrimSpace(kind), "recipe-")
}

func recipeByKind(kind string) (recipeMeta, bool) {
	kind = strings.TrimSpace(kind)
	for _, m := range allRecipeMeta() {
		if m.Kind == kind {
			return m, true
		}
	}
	return recipeMeta{}, false
}

// RecipeArchetypes returns the layout-type catalog for a template. Markup is a
// prototype (page 0, no running title); fill_slides re-renders with chrome.
func RecipeArchetypes(tmpl themes.Template) []themes.Archetype {
	chrome := ExtractChrome(tmpl)
	sg := tmpl.StyleGuide
	out := make([]themes.Archetype, 0, 10)
	for _, m := range allRecipeMeta() {
		markup, err := RenderRecipe(m.Kind, sg, tmpl.Tokens, chrome, nil)
		if err != nil {
			continue
		}
		out = append(out, themes.Archetype{
			Kind:      m.Kind,
			Title:     m.Title,
			Markup:    markup,
			Tier:      "flexible",
			FillSlots: requiredFillSlots(m.Slots),
			SlotHints: m.Slots,
		})
	}
	return out
}

func catalogFromTemplate(tmpl themes.Template) []ArchetypeCatalogEntry {
	recipes := RecipeArchetypes(tmpl)
	cat := catalogFrom(recipes)
	for i := range cat {
		if m, ok := recipeByKind(cat[i].Kind); ok {
			cat[i].Summary = m.Summary
		}
	}
	return append(cat, catalogFrom(tmpl.Archetypes)...)
}

// RenderRecipe builds ASD markup for one layout type. fills are applied later
// by fillArchetypeMarkup; this emits empty named ast-text slots plus chrome.
func RenderRecipe(kind string, sg *themes.StyleGuide, tokens map[string]string, chrome Chrome, fills map[string]string) (string, error) {
	if _, ok := recipeByKind(kind); !ok {
		return "", fmt.Errorf("unknown recipe kind %q", kind)
	}
	b := newRecipeBuilder(kind, sg, tokens, chrome, fills)
	switch kind {
	case RecipeCover:
		b.layoutCover()
	case RecipeSplitNarrative:
		b.layoutSplitNarrative()
	case RecipeQuoteSplit:
		b.layoutQuoteSplit()
	case RecipeTwoUp:
		b.layoutTwoUp()
	case RecipeThreeUp:
		b.layoutThreeUp()
	case RecipeStatRow:
		b.layoutStatRow()
	case RecipeNumberedGrid:
		b.layoutNumberedGrid()
	case RecipeCalloutRail:
		b.layoutCalloutRail()
	case RecipeYearHero:
		b.layoutYearHero()
	case RecipeCloser:
		b.layoutCloser()
	}
	return b.finish(), nil
}

type pal struct {
	surface, ink, accent, muted, card, secondary string
	display, body                                string
	mx, my, contentW                             int
	h1, h2, h3, bodySize, labelSize              int
}

func paletteFrom(sg *themes.StyleGuide, tokens map[string]string) pal {
	p := pal{
		surface:   "#FFFFFF",
		ink:       "#172033",
		accent:    "#1E40AF",
		display:   "",
		body:      "",
		mx:        120,
		my:        72,
		h1:        92,
		h2:        56,
		h3:        32,
		bodySize:  22,
		labelSize: 15,
	}
	if tokens != nil {
		if v := strings.TrimSpace(tokens["surface"]); v != "" {
			p.surface = v
		}
		if v := strings.TrimSpace(tokens["ink"]); v != "" {
			p.ink = v
		}
		if v := strings.TrimSpace(tokens["accent"]); v != "" {
			p.accent = v
		}
		if v := strings.TrimSpace(tokens["muted"]); v != "" {
			p.muted = v
		}
		if v := strings.TrimSpace(tokens["displayFont"]); v != "" {
			p.display = v
		}
		if v := strings.TrimSpace(tokens["bodyFont"]); v != "" {
			p.body = v
		}
	}
	if sg != nil {
		for _, r := range sg.ColorRoles {
			c := strings.TrimSpace(r.Color)
			if c == "" {
				continue
			}
			switch r.Name {
			case "surface":
				p.surface = c
			case "ink":
				p.ink = c
			case "accent":
				p.accent = c
			case "muted":
				p.muted = c
			}
		}
		if sg.FontPairing != nil {
			if sg.FontPairing.DisplayFont != "" {
				p.display = sg.FontPairing.DisplayFont
			}
			if sg.FontPairing.BodyFont != "" {
				p.body = sg.FontPairing.BodyFont
			}
		}
		if sg.SpacingSystem != nil {
			p.mx = clampInt(sg.SpacingSystem.PageMarginX, 80, 160)
			p.my = clampInt(sg.SpacingSystem.PageMarginY, 56, 100)
		}
		for _, tl := range sg.TypographyScale {
			switch tl.Role {
			case "h1":
				p.h1 = clampInt(tl.FontSize, 72, 108)
			case "h2":
				p.h2 = clampInt(tl.FontSize, 48, 72)
			case "h3":
				p.h3 = clampInt(tl.FontSize, 26, 40)
			case "body":
				p.bodySize = clampInt(tl.FontSize, 20, 28)
			case "label", "caption":
				p.labelSize = clampInt(tl.FontSize, 13, 18)
			}
		}
	}
	if p.muted == "" {
		p.muted = mixHex(p.ink, p.surface, 0.42)
	}
	// Template "muted" is often a decorative wash (pale cyan on white). Use it
	// for body/caption text only when it still meets WCAG AA against surface.
	p.secondary = mixHex(p.ink, p.surface, 0.22)
	if textContrastOK(p.muted, p.surface) {
		p.secondary = p.muted
	}
	p.card = mixHex(p.surface, p.ink, 0.08)
	p.contentW = CanvasWidth - 2*p.mx
	return p
}

func textContrastOK(fg, bg string) bool {
	l1, ok1 := hexLuminance(fg)
	l2, ok2 := hexLuminance(bg)
	if !ok1 || !ok2 {
		return false
	}
	return contrastRatio(l1, l2) >= 4.5
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func mixHex(a, b string, t float64) string {
	ar, ag, ab, okA := parseHexRGB(a)
	br, bg, bb, okB := parseHexRGB(b)
	if !okA || !okB {
		return a
	}
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	r := int(float64(ar) + (float64(br)-float64(ar))*t)
	g := int(float64(ag) + (float64(bg)-float64(ag))*t)
	bl := int(float64(ab) + (float64(bb)-float64(ab))*t)
	return fmt.Sprintf("#%02X%02X%02X", r, g, bl)
}

func parseHexRGB(s string) (r, g, b int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int((n >> 16) & 0xff), int((n >> 8) & 0xff), int(n & 0xff), true
}

type recipeBuilder struct {
	kind   string
	pal    pal
	chrome Chrome
	fills  map[string]string
	parts  []string
}

func newRecipeBuilder(kind string, sg *themes.StyleGuide, tokens map[string]string, chrome Chrome, fills map[string]string) *recipeBuilder {
	return &recipeBuilder{kind: kind, pal: paletteFrom(sg, tokens), chrome: chrome, fills: fills, parts: make([]string, 0, 48)}
}

// want reports whether a named slot should be emitted. Required slots always
// are (so fill_slides can substitute). Optional slots appear in the prototype
// (fills == nil) and at fill time only when the model supplied text.
func (b *recipeBuilder) want(id string) bool {
	optional := false
	if m, ok := recipeByKind(b.kind); ok {
		for _, s := range m.Slots {
			if s.ID == id && strings.EqualFold(s.Role, "optional") {
				optional = true
				break
			}
		}
	}
	if !optional {
		return true
	}
	if b.fills == nil {
		return true
	}
	return strings.TrimSpace(b.fills[id]) != ""
}

func (b *recipeBuilder) finish() string {
	return `<ast-slide id="` + html.EscapeString(b.kind) + `">` + strings.Join(b.parts, "") + `</ast-slide>`
}

func (b *recipeBuilder) shape(id, geom string, x, y, w, h int, fill string, decorative bool) {
	dec := ""
	if decorative {
		dec = ` decorative="true"`
	}
	if geom == "" {
		geom = "rect"
	}
	b.parts = append(b.parts, fmt.Sprintf(
		`<ast-shape id="%s" kind="rect" x="%d" y="%d" w="%d" h="%d" geom="%s" fill="%s" alt=""%s></ast-shape>`,
		html.EscapeString(id), x, y, w, h, geom, fill, dec))
}

func (b *recipeBuilder) slot(id string, x, y, w, h, size int, color, weight, font, align string) {
	if !b.want(id) {
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<ast-text id="%s" x="%d" y="%d" w="%d" h="%d" size="%d" color="%s"`,
		html.EscapeString(id), x, y, w, h, size, color))
	if weight != "" {
		sb.WriteString(` weight="` + html.EscapeString(weight) + `"`)
	}
	if font != "" {
		sb.WriteString(` font="` + html.EscapeString(font) + `"`)
	}
	if align != "" {
		sb.WriteString(` align="` + html.EscapeString(align) + `"`)
	}
	if id == "headline" || id == "headline_2" {
		sb.WriteString(` role="title"`)
	}
	sb.WriteString(`></ast-text>`)
	b.parts = append(b.parts, sb.String())
}

func (b *recipeBuilder) staticText(id string, x, y, w, h, size int, color, weight, font, align, text string) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<ast-text id="%s" x="%d" y="%d" w="%d" h="%d" size="%d" color="%s" decorative="true"`,
		html.EscapeString(id), x, y, w, h, size, color))
	if weight != "" {
		sb.WriteString(` weight="` + html.EscapeString(weight) + `"`)
	}
	if font != "" {
		sb.WriteString(` font="` + html.EscapeString(font) + `"`)
	}
	if align != "" {
		sb.WriteString(` align="` + html.EscapeString(align) + `"`)
	}
	sb.WriteString(`><ast-run>` + html.EscapeString(text) + `</ast-run></ast-text>`)
	b.parts = append(b.parts, sb.String())
}

func (b *recipeBuilder) bg() {
	b.shape("bg", "rect", 0, 0, CanvasWidth, CanvasHeight, b.pal.surface, true)
}

func (b *recipeBuilder) topY() int {
	y := b.pal.my
	if b.chrome.LogoRef != "" {
		_, h := b.logoSize()
		// Keep a full row between the logo and the eyebrow so the kicker
		// is not tucked under the mark.
		y = b.pal.my + h + 28
	}
	return y
}

func (b *recipeBuilder) logoSize() (w, h int) {
	w, h = b.chrome.LogoW, b.chrome.LogoH
	if w <= 0 {
		w = 160
	}
	if h <= 0 {
		h = 48
	}
	const maxW, maxH = 200, 52
	if w > maxW || h > maxH {
		sw := float64(maxW) / float64(w)
		sh := float64(maxH) / float64(h)
		s := sw
		if sh < sw {
			s = sh
		}
		w = int(float64(w) * s)
		h = int(float64(h) * s)
	}
	if w < 72 {
		w = 72
	}
	if h < 28 {
		h = 28
	}
	return w, h
}

func (b *recipeBuilder) paintLogo() {
	if b.chrome.LogoRef == "" {
		return
	}
	w, h := b.logoSize()
	b.parts = append(b.parts, fmt.Sprintf(
		`<ast-image id="chrome-logo" x="%d" y="%d" w="%d" h="%d" asset-ref="%s" fit="contain" alt="" decorative="true"></ast-image>`,
		b.pal.mx, b.pal.my, w, h, html.EscapeString(b.chrome.LogoRef)))
}

func (b *recipeBuilder) paintConfidential() {
	if strings.TrimSpace(b.chrome.Confidential) == "" {
		return
	}
	b.staticText("chrome-confidential", b.pal.mx+b.pal.contentW-720, b.pal.my-4, 720, 28, 13, b.pal.accent, "600", b.pal.body, "right", b.chrome.Confidential)
}

func (b *recipeBuilder) paintFooter() {
	y := 1036
	left := strings.TrimSpace(b.chrome.Legal)
	if left == "" {
		left = strings.TrimSpace(b.chrome.DeckTitle)
	}
	if left != "" {
		id := "chrome-footer"
		if b.chrome.Legal != "" {
			id = "chrome-legal"
		}
		b.staticText(id, b.pal.mx, y, b.pal.contentW-100, 28, 13, b.pal.secondary, "", b.pal.body, "", left)
	}
	if b.chrome.Page > 0 {
		b.staticText("chrome-page", CanvasWidth-b.pal.mx-80, y, 80, 28, 14, b.pal.secondary, "", b.pal.body, "right", fmt.Sprintf("%02d", b.chrome.Page))
	}
}

// header writes eyebrow, optional date, accent rule, headline, optional headline_2.
// Returns the Y where body content should start.
func (b *recipeBuilder) header(split bool) int {
	b.bg()
	b.paintLogo()
	b.paintConfidential()
	y := b.topY()
	b.slot("eyebrow", b.pal.mx, y, 980, 32, b.pal.labelSize, b.pal.accent, "600", b.pal.body, "")
	b.slot("date", b.pal.mx+1000, y, b.pal.contentW-1000, 32, b.pal.labelSize, b.pal.secondary, "", b.pal.body, "right")
	b.shape("rule", "rect", b.pal.mx, y+40, 64, 3, b.pal.accent, true)
	hy := y + 56
	hh := b.pal.h2 + 16
	if hh < 72 {
		hh = 72
	}
	if hh > 96 {
		hh = 96
	}
	b.slot("headline", b.pal.mx, hy, b.pal.contentW, hh, b.pal.h2, b.pal.ink, "700", b.pal.display, "")
	if split && b.want("headline_2") {
		b.slot("headline_2", b.pal.mx, hy+hh-6, b.pal.contentW, hh, b.pal.h2, b.pal.ink, "700", b.pal.display, "")
		return hy + 2*hh + 12
	}
	return hy + hh + 20
}

func (b *recipeBuilder) close() {
	b.paintFooter()
}

// ExtractChrome pulls a logo and legal/confidential strings from template
// archetypes. Generic: any imported master, not a named brand.
func ExtractChrome(tmpl themes.Template) Chrome {
	var ch Chrome
	scan := append([]themes.Archetype{}, tmpl.Archetypes...)
	for _, a := range scan {
		if ch.LogoRef == "" {
			if ref, w, h, ok := firstLogoImage(a.Markup); ok {
				ch.LogoRef, ch.LogoW, ch.LogoH = ref, w, h
			}
		}
		if ch.Legal == "" {
			if s := firstMatchingText(a.Markup, legalTextRe); s != "" {
				ch.Legal = s
			}
		}
		if ch.Confidential == "" {
			if s := firstMatchingText(a.Markup, confidentialTextRe); s != "" {
				ch.Confidential = s
			}
		}
	}
	// One confidentiality line: if legal and confidential are the same stamp,
	// keep it in the footer only so it is not painted twice.
	if sameChromeLine(ch.Legal, ch.Confidential) {
		if ch.Legal == "" {
			ch.Legal = ch.Confidential
		}
		ch.Confidential = ""
	}
	return ch
}

func sameChromeLine(a, b string) bool {
	na := strings.ToLower(strings.Join(strings.Fields(a), " "))
	nb := strings.ToLower(strings.Join(strings.Fields(b), " "))
	if na == "" || nb == "" {
		return false
	}
	return na == nb || strings.Contains(na, nb) || strings.Contains(nb, na)
}

var (
	astImageOpenRe     = regexp.MustCompile(`<ast-image\s+([^>]+)`)
	attrRe             = regexp.MustCompile(`([a-zA-Z0-9:-]+)="([^"]*)"`)
	astRunTextRe       = regexp.MustCompile(`<ast-run[^>]*>([^<]*)</ast-run>`)
	legalTextRe        = regexp.MustCompile(`(?i)©|copyright|all rights reserved|partners only`)
	confidentialTextRe = regexp.MustCompile(`(?i)\b(confidential|internal)\b`)
	thumbOrFontAssetRe = regexp.MustCompile(`^(thumb/|slidethumb/|font:)`)
)

func firstLogoImage(markup string) (ref string, w, h int, ok bool) {
	for _, m := range astImageOpenRe.FindAllStringSubmatch(markup, -1) {
		attrs := parseAttrMap(m[1])
		ref = strings.TrimSpace(attrs["asset-ref"])
		if ref == "" || thumbOrFontAssetRe.MatchString(ref) {
			continue
		}
		w = atoiDefault(attrs["w"], 160)
		h = atoiDefault(attrs["h"], 48)
		if w >= 900 && h >= 400 {
			continue // full-bleed photo, not a logo
		}
		if w > 480 {
			h = h * 220 / w
			w = 220
			if h < 24 {
				h = 24
			}
		}
		return ref, w, h, true
	}
	return "", 0, 0, false
}

func parseAttrMap(s string) map[string]string {
	out := map[string]string{}
	for _, m := range attrRe.FindAllStringSubmatch(s, -1) {
		out[m[1]] = m[2]
	}
	return out
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func firstMatchingText(markup string, re *regexp.Regexp) string {
	for _, m := range astRunTextRe.FindAllStringSubmatch(markup, -1) {
		s := strings.TrimSpace(m[1])
		if s == "" {
			continue
		}
		if re.MatchString(s) {
			if len(s) > 180 {
				s = s[:180]
			}
			return s
		}
	}
	return ""
}

// RecipeGuideMarkdown is prepended to the template style guide so the model
// sees layout types before imported pattern-* holes.
func RecipeGuideMarkdown() string {
	var b strings.Builder
	b.WriteString("## Layout types (recipe-*) — default body slides\n\n")
	b.WriteString("Compose every slide from a recipe-* catalog entry. Recipes use this template's ")
	b.WriteString("colors, fonts, logo, and legal line. Pick the type whose slot count matches the content. ")
	b.WriteString("Do not pick a 6-card imported pattern because it looks rich. A chapter is an eyebrow on a full content slide — do not insert empty section dividers.\n\n")
	for _, m := range allRecipeMeta() {
		b.WriteString(fmt.Sprintf("- **%s** (`%s`): %s Required fills: %s.\n",
			m.Title, m.Kind, m.Summary, strings.Join(requiredFillSlots(m.Slots), ", ")))
	}
	b.WriteString("\nImported `pattern-*` entries are optional when their structure matches the same job. Title and Text is last resort.\n\n")
	return b.String()
}
