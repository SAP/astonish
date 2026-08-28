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
	RecipeCover             = "recipe-cover"
	RecipeSplitNarrative    = "recipe-split-narrative"
	RecipeQuoteSplit        = "recipe-quote-split"
	RecipeTwoUp             = "recipe-two-up"
	RecipeThreeUp           = "recipe-three-up"
	RecipeStatRow           = "recipe-stat-row"
	RecipeNumberedGrid      = "recipe-numbered-grid"
	RecipeCalloutRail       = "recipe-callout-rail"
	RecipeYearHero          = "recipe-year-hero"
	RecipeCloser            = "recipe-closer"
	RecipeStatementEvidence = "recipe-statement-evidence"
	RecipeDataTable         = "recipe-data-table"
	RecipeLayerStack        = "recipe-layer-stack"
	RecipeProcessTerminal   = "recipe-process-terminal"
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
	Total        int
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
			Summary: "Cover lockup — eyebrow, split display title, dek, 2–4 meta cells. Slide 0.",
			Slots: []themes.SlotHint{
				{ID: "eyebrow", Role: "label", Hint: "2–5 word kicker (uppercase)"},
				{ID: "headline", Role: "title", Hint: "Display title line 1"},
				{ID: "headline_2", Role: "optional", Hint: "Display title line 2 (optional)"},
				{ID: "dek", Role: "body", Hint: "One-sentence thesis, 12–22 words"},
				{ID: "dek_accent", Role: "optional", Hint: "Exact substring of dek to paint in accent"},
				{ID: "prompt", Role: "optional", Hint: "Cover prompt (e.g. $ astonish chat) — product skin"},
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
				{ID: "detail_1_kicker", Role: "optional", Hint: "Wide card 1 kicker under the stats"},
				{ID: "detail_1_title", Role: "optional", Hint: "Wide card 1 title"},
				{ID: "detail_1_body", Role: "optional", Hint: "Wide card 1 sentence"},
				{ID: "detail_2_kicker", Role: "optional", Hint: "Wide card 2 kicker"},
				{ID: "detail_2_title", Role: "optional", Hint: "Wide card 2 title"},
				{ID: "detail_2_body", Role: "optional", Hint: "Wide card 2 sentence"},
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
			Summary: "Last slide. Corporate: thesis plus 3 takeaway cards. Product: quote, one-line thesis, 3 takeaway chips — never a lone headline on empty canvas.",
			Slots: append([]themes.SlotHint{
				{ID: "eyebrow", Role: "label", Hint: "Closer kicker"},
				{ID: "headline", Role: "title", Hint: "Takeaway title line 1"},
				{ID: "headline_2", Role: "optional", Hint: "Title line 2 (optional)"},
				{ID: "headline_accent", Role: "optional", Hint: "Exact substring of headline to paint in accent"},
				{ID: "thesis", Role: "body", Hint: "Closing thesis, 40–80 words"},
				{ID: "thesis_accent", Role: "optional", Hint: "Exact substring of thesis to paint in accent"},
				{ID: "cta_kicker", Role: "optional", Hint: "Closing CTA kicker (Get started)"},
				{ID: "cta_body", Role: "optional", Hint: "Install commands or next step, one per line"},
			}, itemSlots(3, false)...),
		},
		{
			Kind:    RecipeStatementEvidence,
			Title:   "Statement + evidence",
			Summary: "Big claim left, evidence list right. Use for the problem or why-now slide.",
			Slots: headerSlotsPlus([]themes.SlotHint{
				{ID: "body_1", Role: "body", Hint: "Left argument, two short paragraphs"},
				{ID: "evidence_kicker", Role: "label", Hint: "Evidence panel kicker"},
				{ID: "evidence_1_title", Role: "heading", Hint: "Evidence 1 heading"},
				{ID: "evidence_1_body", Role: "body", Hint: "Evidence 1 sentence"},
				{ID: "evidence_2_title", Role: "heading", Hint: "Evidence 2 heading"},
				{ID: "evidence_2_body", Role: "body", Hint: "Evidence 2 sentence"},
				{ID: "evidence_3_title", Role: "heading", Hint: "Evidence 3 heading"},
				{ID: "evidence_3_body", Role: "body", Hint: "Evidence 3 sentence"},
			}),
		},
		{
			Kind:    RecipeDataTable,
			Title:   "Data table",
			Summary: "Hairline table — tiers, comparisons, spec sheets. Not a card grid.",
			Slots: headerSlotsPlus([]themes.SlotHint{
				{ID: "col_1", Role: "label", Hint: "Column 1 header"},
				{ID: "col_2", Role: "label", Hint: "Column 2 header"},
				{ID: "col_3", Role: "label", Hint: "Column 3 header"},
				{ID: "col_4", Role: "optional", Hint: "Column 4 header"},
				{ID: "row_1_col_1", Role: "body", Hint: "Row 1 col 1"},
				{ID: "row_1_col_2", Role: "body", Hint: "Row 1 col 2"},
				{ID: "row_1_col_3", Role: "body", Hint: "Row 1 col 3"},
				{ID: "row_1_col_4", Role: "optional", Hint: "Row 1 col 4"},
				{ID: "row_2_col_1", Role: "body", Hint: "Row 2 col 1"},
				{ID: "row_2_col_2", Role: "body", Hint: "Row 2 col 2"},
				{ID: "row_2_col_3", Role: "body", Hint: "Row 2 col 3"},
				{ID: "row_2_col_4", Role: "optional", Hint: "Row 2 col 4"},
				{ID: "row_3_col_1", Role: "body", Hint: "Row 3 col 1"},
				{ID: "row_3_col_2", Role: "body", Hint: "Row 3 col 2"},
				{ID: "row_3_col_3", Role: "body", Hint: "Row 3 col 3"},
				{ID: "row_3_col_4", Role: "optional", Hint: "Row 3 col 4"},
				{ID: "table_note", Role: "optional", Hint: "One-line footnote under the table"},
			}),
		},
		{
			Kind:    RecipeLayerStack,
			Title:   "Layer stack",
			Summary: "Numbered stack left, 3 argument cards right. Highlight one layer with emphasis=2.",
			Slots: headerSlotsPlus([]themes.SlotHint{
				{ID: "lede", Role: "optional", Hint: "One-line lede under the headline"},
				{ID: "stack_label", Role: "label", Hint: "Stack panel label"},
				{ID: "layer_4_name", Role: "heading", Hint: "Top layer name"},
				{ID: "layer_4_meta", Role: "optional", Hint: "Top layer meta"},
				{ID: "layer_3_name", Role: "heading", Hint: "Layer 3 name"},
				{ID: "layer_3_meta", Role: "optional", Hint: "Layer 3 meta"},
				{ID: "layer_2_name", Role: "heading", Hint: "Layer 2 name (often the argument)"},
				{ID: "layer_2_meta", Role: "optional", Hint: "Layer 2 meta"},
				{ID: "layer_1_name", Role: "heading", Hint: "Layer 1 name"},
				{ID: "layer_1_meta", Role: "optional", Hint: "Layer 1 meta"},
				{ID: "layer_0_name", Role: "heading", Hint: "Foundation layer name"},
				{ID: "layer_0_meta", Role: "optional", Hint: "Foundation meta"},
				{ID: "card_1_kicker", Role: "label", Hint: "Right card 1 kicker"},
				{ID: "card_1_title", Role: "heading", Hint: "Right card 1 title"},
				{ID: "card_1_body", Role: "body", Hint: "Right card 1 sentence"},
				{ID: "card_2_kicker", Role: "label", Hint: "Right card 2 kicker"},
				{ID: "card_2_title", Role: "heading", Hint: "Right card 2 title"},
				{ID: "card_2_body", Role: "body", Hint: "Right card 2 sentence"},
				{ID: "card_3_kicker", Role: "label", Hint: "Right card 3 kicker"},
				{ID: "card_3_title", Role: "heading", Hint: "Right card 3 title"},
				{ID: "card_3_body", Role: "body", Hint: "Right card 3 sentence"},
			}),
		},
		{
			Kind:    RecipeProcessTerminal,
			Title:   "Process + terminal",
			Summary: "Three steps plus a full-width transcript. Use for how-it-works. emphasis=3 is the payoff.",
			Slots: headerSlotsPlus([]themes.SlotHint{
				{ID: "step_1_kicker", Role: "label", Hint: "Step 1 kicker"},
				{ID: "step_1_title", Role: "heading", Hint: "Step 1 title"},
				{ID: "step_1_body", Role: "body", Hint: "Step 1 sentence"},
				{ID: "step_2_kicker", Role: "label", Hint: "Step 2 kicker"},
				{ID: "step_2_title", Role: "heading", Hint: "Step 2 title"},
				{ID: "step_2_body", Role: "body", Hint: "Step 2 sentence"},
				{ID: "step_3_kicker", Role: "label", Hint: "Step 3 kicker"},
				{ID: "step_3_title", Role: "heading", Hint: "Step 3 title"},
				{ID: "step_3_body", Role: "body", Hint: "Step 3 sentence"},
				{ID: "terminal_kicker", Role: "label", Hint: "Transcript label"},
				{ID: "terminal_body", Role: "body", Hint: "4-line transcript, one command per line"},
			}),
		},
	}
}

func headerSlotsPlus(extra []themes.SlotHint) []themes.SlotHint {
	base := []themes.SlotHint{
		{ID: "eyebrow", Role: "label", Hint: "Chapter/section kicker, 2–5 words"},
		{ID: "date", Role: "optional", Hint: "Year range or // kicker (optional)"},
		{ID: "headline", Role: "title", Hint: "Display title line 1"},
		{ID: "headline_2", Role: "optional", Hint: "Display title line 2 (optional)"},
		{ID: "headline_accent", Role: "optional", Hint: "Exact substring of headline to paint in accent (one phrase)"},
		{ID: "emphasis", Role: "optional", Hint: "Which card/column to emphasize: 1, 2, or 3"},
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
	out := make([]themes.Archetype, 0, 16)
	for _, m := range allRecipeMeta() {
		a, err := recipeArchetypeFor(tmpl, m.Kind, chrome, nil)
		if err != nil {
			continue
		}
		out = append(out, a)
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
	return append(cat, catalogFrom(agentCatalogBookends(tmpl))...)
}

// RenderRecipe builds ASD markup for one layout type. fills are applied later
// by fillArchetypeMarkup; this emits empty named ast-text slots plus chrome.
func RenderRecipe(kind string, skin Skin, sg *themes.StyleGuide, chrome Chrome, fills map[string]string) (string, error) {
	if _, ok := recipeByKind(kind); !ok {
		return "", fmt.Errorf("unknown recipe kind %q", kind)
	}
	b := newRecipeBuilder(kind, skin, sg, chrome, fills)
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
	case RecipeStatementEvidence:
		b.layoutStatementEvidence()
	case RecipeDataTable:
		b.layoutDataTable()
	case RecipeLayerStack:
		b.layoutLayerStack()
	case RecipeProcessTerminal:
		b.layoutProcessTerminal()
	}
	return b.finish(), nil
}

type pal struct {
	skinID                                            string
	surface, ink, accent, muted, card, secondary      string
	panel, inset, line, accentFill, accentEdge        string
	accentGlow, inkDim                                string
	display, body, mono                               string
	mx, my, contentW                                  int
	h1, h2, h3, bodySize, labelSize, chromeSize, stat int
}

func paletteFrom(skin Skin, sg *themes.StyleGuide) pal {
	p := pal{
		skinID:     skin.ID,
		surface:    skin.Surface,
		ink:        skin.Ink,
		accent:     skin.Accent,
		muted:      skin.InkMute,
		secondary:  skin.InkMute,
		card:       skin.Panel,
		panel:      skin.Panel,
		inset:      skin.Inset,
		line:       skin.Line,
		accentFill: skin.AccentFill,
		accentEdge: skin.AccentEdge,
		accentGlow: skin.AccentGlow,
		inkDim:     skin.InkDim,
		display:    skin.DisplayFont,
		body:       skin.BodyFont,
		mono:       skin.MonoFont,
		mx:         skin.MarginX,
		my:         skin.MarginY,
		h1:         skin.HeroSize,
		h2:         skin.H2Size,
		h3:         skin.CardTitle,
		bodySize:   skin.BodySize,
		labelSize:  skin.EyebrowSize,
		chromeSize: skin.ChromeSize,
		stat:       skin.Stat,
	}
	if !skin.IsProduct() && sg != nil && sg.SpacingSystem != nil {
		p.mx = clampInt(sg.SpacingSystem.PageMarginX, 80, 160)
		p.my = clampInt(sg.SpacingSystem.PageMarginY, 56, 100)
	}
	if !textContrastOK(p.secondary, p.surface) {
		p.secondary = mixHex(p.ink, p.surface, 0.22)
	}
	if p.card == "" {
		p.card = mixHex(p.surface, p.ink, 0.08)
		p.panel = p.card
	}
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

func newRecipeBuilder(kind string, skin Skin, sg *themes.StyleGuide, chrome Chrome, fills map[string]string) *recipeBuilder {
	return &recipeBuilder{kind: kind, pal: paletteFrom(skin, sg), chrome: chrome, fills: fills, parts: make([]string, 0, 48)}
}

func (b *recipeBuilder) isProduct() bool { return b.pal.skinID == SkinProduct }

func (b *recipeBuilder) chromeFont() string {
	if b.isProduct() && b.pal.mono != "" {
		return b.pal.mono
	}
	return b.pal.body
}

func (b *recipeBuilder) emphasis(def int) int {
	if b.fills == nil {
		return def
	}
	s := strings.TrimSpace(b.fills["emphasis"])
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func (b *recipeBuilder) panel(id string, x, y, w, h int, emphasized bool) {
	x, y, w, h, ok := clampCanvas(x, y, w, h)
	if !ok {
		return
	}
	fill := b.pal.panel
	line := b.pal.line
	if emphasized {
		fill = b.pal.accentFill
		if fill == "" {
			fill = mixHex(b.pal.surface, b.pal.accent, 0.14)
		}
		line = b.pal.accentEdge
		if line == "" {
			line = b.pal.accent
		}
	}
	extra := ""
	if line != "" {
		extra = fmt.Sprintf(` line="%s" line-width="1"`, line)
	}
	dec := ""
	b.parts = append(b.parts, fmt.Sprintf(
		`<ast-shape id="%s" kind="rect" x="%d" y="%d" w="%d" h="%d" geom="roundRect" fill="%s"%s alt=""%s></ast-shape>`,
		html.EscapeString(id), x, y, w, h, fill, extra, dec))
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
	x, y, w, h, ok := clampCanvas(x, y, w, h)
	if !ok {
		return
	}
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

func clampCanvas(x, y, w, h int) (int, int, int, int, bool) {
	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	if x >= CanvasWidth || y >= CanvasHeight {
		return 0, 0, 0, 0, false
	}
	if x+w > CanvasWidth {
		w = CanvasWidth - x
	}
	if y+h > CanvasHeight {
		h = CanvasHeight - y
	}
	if w <= 0 || h <= 0 {
		return 0, 0, 0, 0, false
	}
	return x, y, w, h, true
}

func (b *recipeBuilder) slot(id string, x, y, w, h, size int, color, weight, font, align string) {
	if !b.want(id) {
		return
	}
	x, y, w, h, ok := clampCanvas(x, y, w, h)
	if !ok {
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
	x, y, w, h, ok := clampCanvas(x, y, w, h)
	if !ok {
		return
	}
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
	if b.isProduct() {
		b.productBG()
		return
	}
	b.shape("bg", "rect", 0, 0, CanvasWidth, CanvasHeight, b.pal.surface, true)
}

// productBG paints an opaque radial wash (accent mixed into surface). Stops
// are solid colors so HTML/PDF/PPTX do not depend on shape opacity. The first
// stop stays clearly violet; it must still read as atmosphere, not a disk.
func (b *recipeBuilder) productBG() {
	hi := mixHex(b.pal.accent, b.pal.surface, 0.30)
	mid := mixHex(b.pal.accent, b.pal.surface, 0.72)
	lo := b.pal.surface
	payload := fmt.Sprintf(`{"kind":"radial","stops":[{"pos":0,"color":"%s"},{"pos":42,"color":"%s"},{"pos":100,"color":"%s"}]}`, hi, mid, lo)
	b.parts = append(b.parts, fmt.Sprintf(
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="%d" h="%d" geom="rect" fill="%s" alt="" decorative="true"><script type="application/json" id="bg-gradient">%s</script></ast-shape>`,
		CanvasWidth, CanvasHeight, html.EscapeString(lo), payload))
}

func (b *recipeBuilder) topY() int {
	if b.isProduct() {
		return 188
	}
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
	// Only a real template asset. Never invent a mark, circle, or wordmark.
	if b.isProduct() || b.chrome.LogoRef == "" {
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
	if b.isProduct() {
		b.shape("chrome-hairline", "rect", b.pal.mx, 1020, b.pal.contentW, 1, b.pal.line, true)
		left := strings.TrimSpace(b.chrome.DeckTitle)
		if left == "" {
			left = "deck"
		}
		b.staticText("chrome-footer", b.pal.mx, y, 900, 24, b.pal.chromeSize, b.pal.inkDim, "", b.pal.mono, "", strings.ToLower(left))
		return
	}
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

func (b *recipeBuilder) paintProductRail() {
	p := b.pal
	page := b.chrome.Page
	if page <= 0 {
		page = 1
	}
	total := b.chrome.Total
	if total <= 0 {
		total = page
	}
	b.shape("rail-dot", "ellipse", p.mx, 70, 10, 10, p.accent, true)
	// Eyebrow sits in the rail as "05 · SECTION". The slot is empty; static
	// prefix is chrome. The actual eyebrow text is a following slot.
	b.staticText("rail-page-left", p.mx+20, 64, 80, 28, p.chromeSize, p.secondary, "600", p.mono, "", fmt.Sprintf("%02d", page)+"  ·")
	b.slot("eyebrow", p.mx+108, 64, 900, 28, p.chromeSize, p.secondary, "600", p.mono, "")
	b.staticText("rail-page-right", CanvasWidth-p.mx-160, 64, 160, 28, p.chromeSize, p.secondary, "", p.mono, "right", fmt.Sprintf("%02d / %02d", page, total))
}

// header writes eyebrow, optional date, accent rule, headline, optional headline_2.
// Returns the Y where body content should start.
func (b *recipeBuilder) header(split bool) int {
	b.bg()
	if b.isProduct() {
		b.paintProductRail()
		y := 188
		if b.want("date") {
			b.slot("date", b.pal.mx, y, b.pal.contentW, 28, b.pal.labelSize, b.pal.accent, "", b.pal.mono, "")
			y += 36
		}
		line := b.pal.h2 + 16
		if line < 80 {
			line = 80
		}
		if split && b.want("headline_2") {
			b.slot("headline", b.pal.mx, y, b.pal.contentW, line, b.pal.h2, b.pal.ink, "700", b.pal.display, "")
			b.slot("headline_2", b.pal.mx, y+line-8, b.pal.contentW, line, b.pal.h2, b.pal.ink, "700", b.pal.display, "")
			return y + 2*line + 16
		}
		// One takeaway slot: two wrapped lines so long titles are not clipped.
		hh := line*2 + 8
		b.slot("headline", b.pal.mx, y, b.pal.contentW, hh, b.pal.h2, b.pal.ink, "700", b.pal.display, "")
		return y + hh + 20
	}
	b.paintLogo()
	b.paintConfidential()
	y := b.topY()
	b.slot("eyebrow", b.pal.mx, y, 980, 32, b.pal.labelSize, b.pal.accent, "600", b.pal.body, "")
	b.slot("date", b.pal.mx+1000, y, b.pal.contentW-1000, 32, b.pal.labelSize, b.pal.secondary, "", b.pal.body, "right")
	b.shape("rule", "rect", b.pal.mx, y+40, 64, 3, b.pal.accent, true)
	hy := y + 56
	line := b.pal.h2 + 16
	if line < 72 {
		line = 72
	}
	if split && b.want("headline_2") {
		b.slot("headline", b.pal.mx, hy, b.pal.contentW, line, b.pal.h2, b.pal.ink, "700", b.pal.display, "")
		b.slot("headline_2", b.pal.mx, hy+line-6, b.pal.contentW, line, b.pal.h2, b.pal.ink, "700", b.pal.display, "")
		return hy + 2*line + 12
	}
	hh := line*2 + 4
	b.slot("headline", b.pal.mx, hy, b.pal.contentW, hh, b.pal.h2, b.pal.ink, "700", b.pal.display, "")
	return hy + hh + 16
}

// contentBottom is the last Y body cards may occupy (above the footer hairline).
func (b *recipeBuilder) contentBottom() int { return 1004 }

func (b *recipeBuilder) bodyH(fromY int) int {
	h := b.contentBottom() - fromY
	if h < 220 {
		h = 220
	}
	return h
}

// cappedBodyH is for card grids: tall enough to feel like a panel, short
// enough that two sentences are not floating in an empty slab.
func (b *recipeBuilder) cappedBodyH(fromY, cap int) int {
	h := b.bodyH(fromY)
	if cap > 0 && h > cap {
		return cap
	}
	return h
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

func applyAccentSpans(markup string, fills map[string]string, accent string) string {
	if accent == "" || fills == nil {
		return markup
	}
	pairs := [][2]string{
		{"headline", "headline_accent"},
		{"dek", "dek_accent"},
		{"thesis", "thesis_accent"},
	}
	for _, p := range pairs {
		phrase := strings.TrimSpace(fills[p[1]])
		if phrase == "" {
			continue
		}
		markup = colorizeTextSlot(markup, p[0], phrase, accent)
	}
	return markup
}

func colorizeTextSlot(markup, id, phrase, accent string) string {
	_, tag, _, innerStart, innerEnd, _, ok := findElement(markup, id)
	if !ok || tag != "ast-text" {
		return markup
	}
	inner := markup[innerStart:innerEnd]
	text := inner
	if m := astRunTextRe.FindStringSubmatch(inner); len(m) == 2 {
		text = html.UnescapeString(m[1])
	} else {
		text = html.UnescapeString(stripTags(inner))
	}
	if text == "" {
		return markup
	}
	idx := strings.Index(strings.ToLower(text), strings.ToLower(phrase))
	if idx < 0 {
		return markup
	}
	// Preserve original phrase casing from the filled text.
	mid := text[idx : idx+len(phrase)]
	before := text[:idx]
	after := text[idx+len(phrase):]
	var b strings.Builder
	if before != "" {
		b.WriteString(`<ast-run>` + html.EscapeString(before) + `</ast-run>`)
	}
	b.WriteString(`<ast-run color="` + html.EscapeString(accent) + `">` + html.EscapeString(mid) + `</ast-run>`)
	if after != "" {
		b.WriteString(`<ast-run>` + html.EscapeString(after) + `</ast-run>`)
	}
	return markup[:innerStart] + b.String() + markup[innerEnd:]
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RecipeGuideMarkdown is prepended to the template style guide so the model
// sees layout types before imported pattern-* holes.
func RecipeGuideMarkdown() string {
	var b strings.Builder
	b.WriteString("## Layout types (recipe-*) — default body slides\n\n")
	b.WriteString("Compose body slides from a recipe-* catalog entry. Official title/closing in the catalog are the bookends. Recipes use this template's ")
	b.WriteString("**skin** (corporate: logo/legal/accent rule; product: dark canvas, mono rails, panels). ")
	b.WriteString("Pick the type whose slot count matches the content. Mix card layouts with table, stack, and terminal so pages do not all look like the same boxes. ")
	b.WriteString("Optional `headline_accent` colors one phrase; `emphasis` (1/2/3) highlights one card. ")
	b.WriteString("The catalog's fillSlots for this template is authoritative (product cover has two meta cells, optional third — not meta_4; product closer requires thesis + 3 takeaway chips). ")
	b.WriteString("If the catalog lists title / title-N, slide 0 is that official cover (fill those slot ids, not recipe names). If it lists closing / closing-N, the last slide is that official end page. ")
	b.WriteString("pattern-*, section, and agenda are not in the default catalog; fetch them with get_archetype only if the user asked. A chapter is an eyebrow on a full content slide — do not insert empty section dividers.\n\n")
	for _, m := range allRecipeMeta() {
		b.WriteString(fmt.Sprintf("- **%s** (`%s`): %s\n", m.Title, m.Kind, m.Summary))
	}
	b.WriteString("\nTitle and Text is last resort. Do not pour a story into an imported sample.\n\n")
	return b.String()
}
