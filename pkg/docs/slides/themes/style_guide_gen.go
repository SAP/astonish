package themes

import (
	"fmt"
	"sort"
	"strings"
)

// GenerateStyleGuide analyzes a TemplateModel and theme tokens to produce a
// comprehensive StyleGuide for LLM consumption. Returns nil only when model is nil.
// When archetypes are provided, it also generates ComponentPatterns describing
// each flexible content archetype's visual structure and fill instructions.
func GenerateStyleGuide(model *TemplateModel, tokens map[string]string, archetypes []Archetype) *StyleGuide {
	if model == nil {
		return nil
	}
	if tokens == nil {
		tokens = map[string]string{}
	}

	sg := &StyleGuide{}
	sg.TypographyScale = deriveTypographyScale(model, tokens)
	sg.ColorRoles = deriveColorRoles(tokens)
	sg.SpacingSystem = deriveSpacingSystem(model)
	sg.LayoutPatterns = deriveLayoutPatterns(model)
	sg.FontPairing = deriveFontPairing(model, tokens)
	sg.AvoidList = generateAvoidList(model, tokens)
	sg.ComponentPatterns = GenerateComponentPatterns(archetypes)
	sg.Markdown = generateMarkdown(sg, tokens)
	return sg
}

// deriveTypographyScale extracts a type hierarchy from placeholder font sizes.
func deriveTypographyScale(model *TemplateModel, tokens map[string]string) []TypeLevel {
	type sizeEntry struct {
		size   int
		font   string
		weight string
		color  string
		phType string // title, body, etc.
	}

	var entries []sizeEntry

	for _, layout := range model.Layouts {
		for _, ph := range layout.Placeholders {
			if ph.Style.FontSize <= 0 {
				continue
			}
			weight := "normal"
			if ph.Style.Bold {
				weight = "700"
			}
			entries = append(entries, sizeEntry{
				size:   ph.Style.FontSize,
				font:   ph.Style.FontFace,
				weight: weight,
				color:  ph.Style.Color,
				phType: ph.Type,
			})
		}
		// Also scan chrome text objects for eyebrow/caption sizes
		for _, obj := range layout.Objects {
			if obj.Kind == "text" && obj.Style != nil && obj.Style.FontSize > 0 {
				weight := "normal"
				if obj.Style.Bold {
					weight = "700"
				}
				entries = append(entries, sizeEntry{
					size:   obj.Style.FontSize,
					font:   obj.Style.FontFace,
					weight: weight,
					color:  obj.Style.Color,
					phType: "chrome",
				})
			}
		}
	}

	if len(entries) == 0 {
		return defaultTypographyScale(tokens)
	}

	// Deduplicate by size and assign roles
	sizeMap := map[int]*sizeEntry{}
	for i := range entries {
		e := &entries[i]
		if existing, ok := sizeMap[e.size]; ok {
			// Prefer title-type entries for role assignment
			if e.phType == "title" {
				sizeMap[e.size] = e
			} else if existing.phType != "title" && e.font != "" {
				sizeMap[e.size] = e
			}
		} else {
			sizeMap[e.size] = e
		}
	}

	// Sort by size descending
	type sized struct {
		size  int
		entry *sizeEntry
	}
	var sorted []sized
	for s, e := range sizeMap {
		sorted = append(sorted, sized{s, e})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].size > sorted[j].size })

	displayFont := tokens["displayFont"]
	bodyFont := tokens["bodyFont"]

	var levels []TypeLevel
	roles := []struct {
		role  string
		usage string
	}{
		{"h1", "Cover slides and major transition titles"},
		{"h2", "Content slide titles and section headers"},
		{"h3", "Subsection headings or card titles"},
		{"body", "Main content text, bullet points, descriptions"},
		{"caption", "Supporting text, footnotes, source citations"},
		{"label", "Eyebrow text, metadata labels, category markers"},
	}

	for i, s := range sorted {
		if i >= len(roles) {
			break
		}
		font := s.entry.font
		if font == "" {
			if i < 3 {
				font = displayFont
			} else {
				font = bodyFont
			}
		}
		levels = append(levels, TypeLevel{
			Role:     roles[i].role,
			FontSize: s.size,
			Weight:   s.entry.weight,
			Font:     font,
			Color:    s.entry.color,
			Usage:    roles[i].usage,
		})
	}

	return levels
}

func defaultTypographyScale(tokens map[string]string) []TypeLevel {
	displayFont := tokens["displayFont"]
	if displayFont == "" {
		displayFont = "Aptos Display"
	}
	bodyFont := tokens["bodyFont"]
	if bodyFont == "" {
		bodyFont = "Aptos"
	}
	ink := tokens["ink"]
	if ink == "" {
		ink = "#172033"
	}
	return []TypeLevel{
		{Role: "h1", FontSize: 84, Weight: "800", Font: displayFont, Color: ink, Usage: "Cover slides and major transition titles"},
		{Role: "h2", FontSize: 56, Weight: "700", Font: displayFont, Color: ink, Usage: "Content slide titles and section headers"},
		{Role: "h3", FontSize: 40, Weight: "600", Font: displayFont, Color: ink, Usage: "Subsection headings or card titles"},
		{Role: "body", FontSize: 28, Weight: "normal", Font: bodyFont, Color: ink, Usage: "Main content text, bullet points, descriptions"},
		{Role: "caption", FontSize: 20, Weight: "normal", Font: bodyFont, Color: ink, Usage: "Supporting text, footnotes, source citations"},
		{Role: "label", FontSize: 16, Weight: "500", Font: bodyFont, Color: ink, Usage: "Eyebrow text, metadata labels, category markers"},
	}
}

// deriveColorRoles maps theme tokens to semantic color roles with usage guidance.
func deriveColorRoles(tokens map[string]string) []ColorRole {
	var roles []ColorRole

	if c := tokens["surface"]; c != "" {
		roles = append(roles, ColorRole{Name: "surface", Color: c, Usage: "Slide backgrounds and panel fills"})
	}
	if c := tokens["ink"]; c != "" {
		roles = append(roles, ColorRole{Name: "ink", Color: c, Usage: "Primary text color for headings and body"})
	}
	if c := tokens["accent"]; c != "" {
		roles = append(roles, ColorRole{Name: "accent", Color: c, Usage: "Key metrics, emphasis highlights, section markers, and chart primary color", Limit: "Maximum once per slide for impact"})
	}
	if c := tokens["accent2"]; c != "" {
		roles = append(roles, ColorRole{Name: "accent2", Color: c, Usage: "Secondary accent for charts, tags, and supporting highlights", Limit: "Use sparingly alongside primary accent"})
	}
	if c := tokens["muted"]; c != "" {
		roles = append(roles, ColorRole{Name: "muted", Color: c, Usage: "Subtle backgrounds, dividers, disabled states, and card surfaces"})
	}

	// If we have no tokens at all, provide defaults
	if len(roles) == 0 {
		roles = []ColorRole{
			{Name: "surface", Color: "#FFFFFF", Usage: "Slide backgrounds and panel fills"},
			{Name: "ink", Color: "#172033", Usage: "Primary text color for headings and body"},
			{Name: "accent", Color: "#2563EB", Usage: "Key metrics, emphasis highlights, section markers", Limit: "Maximum once per slide for impact"},
		}
	}

	return roles
}

// deriveSpacingSystem analyzes placeholder positions to derive spacing rhythm.
func deriveSpacingSystem(model *TemplateModel) *SpacingSystem {
	var leftEdges, topEdges, titleBottoms, bodyTops []int

	for _, layout := range model.Layouts {
		for _, ph := range layout.Placeholders {
			if ph.X > 0 && ph.X < 400 {
				leftEdges = append(leftEdges, ph.X)
			}
			if ph.Y > 0 && ph.Y < 200 {
				topEdges = append(topEdges, ph.Y)
			}
			if ph.Type == "title" {
				titleBottoms = append(titleBottoms, ph.Y+ph.H)
			}
			if ph.Type == "body" {
				bodyTops = append(bodyTops, ph.Y)
			}
		}
	}

	marginX := median(leftEdges)
	if marginX == 0 {
		marginX = 120
	}
	marginY := median(topEdges)
	if marginY == 0 {
		marginY = 80
	}

	titleBodyGap := 40
	if len(titleBottoms) > 0 && len(bodyTops) > 0 {
		medTB := median(titleBottoms)
		medBT := median(bodyTops)
		if medBT > medTB {
			titleBodyGap = medBT - medTB
		}
	}

	contentStartY := 280
	if len(bodyTops) > 0 {
		contentStartY = median(bodyTops)
	}

	return &SpacingSystem{
		PageMarginX:   marginX,
		PageMarginY:   marginY,
		SectionGap:    48,
		ElementGap:    24,
		TitleBodyGap:  titleBodyGap,
		ContentStartY: contentStartY,
	}
}

// deriveLayoutPatterns analyzes layouts and their placeholders to detect patterns.
func deriveLayoutPatterns(model *TemplateModel) []LayoutPattern {
	var patterns []LayoutPattern

	for _, layout := range model.Layouts {
		kind := inferLayoutKind(layout)
		name := layout.Name
		if name == "" {
			name = layout.ID
		}

		cols := detectColumns(layout.Placeholders)

		desc := layoutDescription(kind, cols)

		patterns = append(patterns, LayoutPattern{
			Kind:        kind,
			Name:        name,
			Description: desc,
			Columns:     cols,
		})
	}

	return patterns
}

func inferLayoutKind(layout IRLayout) string {
	nameLower := strings.ToLower(layout.Name)
	hasBody := false
	for _, ph := range layout.Placeholders {
		if ph.Type == "body" {
			hasBody = true
			break
		}
	}
	switch {
	case !hasBody && (strings.Contains(nameLower, "thank") || strings.Contains(nameLower, "copyright") || strings.Contains(nameLower, "contact")):
		return "closing"
	case strings.Contains(nameLower, "agenda") || strings.Contains(nameLower, "toc") || strings.Contains(nameLower, "contents"):
		return "agenda"
	case strings.Contains(nameLower, "divider") || strings.Contains(nameLower, "section") || strings.Contains(nameLower, "chapter"):
		return "section"
	case strings.Contains(nameLower, "cover") || strings.Contains(nameLower, "title slide") || nameLower == "title":
		return "title"
	default:
		return "content"
	}
}

func detectColumns(placeholders []IRPlaceholder) int {
	if len(placeholders) < 2 {
		return 1
	}

	// Look for body-type placeholders at similar Y but different X regions
	var bodyPhs []IRPlaceholder
	for _, ph := range placeholders {
		if ph.Type == "body" || ph.Type == "image" {
			bodyPhs = append(bodyPhs, ph)
		}
	}

	if len(bodyPhs) < 2 {
		return 1
	}

	// Check if body placeholders are arranged side-by-side (similar Y, different X)
	colCount := 1
	for i := 1; i < len(bodyPhs); i++ {
		yDiff := abs(bodyPhs[i].Y - bodyPhs[0].Y)
		xDiff := abs(bodyPhs[i].X - bodyPhs[0].X)
		if yDiff < 100 && xDiff > 200 {
			colCount++
		}
	}

	return colCount
}

func layoutDescription(kind string, cols int) string {
	switch kind {
	case "title":
		return "Cover slide with branded chrome — fill only designated text slots"
	case "section":
		return "Section divider with accent styling — use for topic transitions"
	case "agenda":
		return "Agenda/outline slide — list meeting topics or deck structure"
	case "closing":
		return "Closing slide — summary, CTA, or next-steps"
	default:
		if cols >= 3 {
			return "Multi-column content — use for metrics grid, comparison cards, or parallel items"
		}
		if cols == 2 {
			return "Two-column content — use for side-by-side comparison, text + visual, or pros/cons"
		}
		return "Single-column content — use for narrative text, bullet points, or focused data"
	}
}

// deriveFontPairing documents the font combination.
func deriveFontPairing(model *TemplateModel, tokens map[string]string) *FontPairing {
	displayFont := tokens["displayFont"]
	if displayFont == "" {
		displayFont = "Aptos Display"
	}
	bodyFont := tokens["bodyFont"]
	if bodyFont == "" {
		bodyFont = "Aptos"
	}

	// Detect monospace usage from chrome text objects
	monoFont := detectMonoFont(model)

	fp := &FontPairing{
		DisplayFont:  displayFont,
		BodyFont:     bodyFont,
		DisplayUsage: fmt.Sprintf("%s: headings, titles, key numbers, and hero metrics", displayFont),
		BodyUsage:    fmt.Sprintf("%s: body text, paragraphs, bullets, and descriptions", bodyFont),
	}
	if monoFont != "" {
		fp.MonoFont = monoFont
	}

	return fp
}

func detectMonoFont(model *TemplateModel) string {
	monoFonts := map[string]int{}
	monoKeywords := []string{"mono", "code", "courier", "consola", "jetbrains", "fira code", "source code", "ibm plex mono"}

	for _, layout := range model.Layouts {
		for _, obj := range layout.Objects {
			if obj.Style != nil && obj.Style.FontFace != "" {
				for _, kw := range monoKeywords {
					if strings.Contains(strings.ToLower(obj.Style.FontFace), kw) {
						monoFonts[obj.Style.FontFace]++
						break
					}
				}
			}
		}
	}

	if len(monoFonts) == 0 {
		return ""
	}

	// Return the most-used monospace font
	var best string
	var bestCount int
	for f, c := range monoFonts {
		if c > bestCount {
			best = f
			bestCount = c
		}
	}
	return best
}

// generateAvoidList produces template-specific + universal avoid items.
func generateAvoidList(model *TemplateModel, tokens map[string]string) []string {
	avoid := []string{
		"Drop shadows on text or shapes",
		"3-D chart effects, bevels, or embossing",
		"Decorative clip-art or stock icons not from the template",
		"Gradient fills on text",
		"More than 2 accent colors per slide",
		"Bullet lists exceeding 6 items",
		"Font sizes below 18px (illegible at presentation distance)",
		"Centered body text paragraphs (use left-aligned for readability)",
		"Topic-label titles (\"Q3 Results\") — always write takeaway sentences",
	}

	// Template-specific items
	if isDarkTemplate(tokens) {
		avoid = append(avoid, "Light or warm backgrounds (cream, ivory, sand, beige)")
		avoid = append(avoid, "Black or very dark text on dark backgrounds — use the template's ink color")
	} else {
		avoid = append(avoid, "Dark/black slide backgrounds — stay with the template's light surface")
	}

	if detectMonoFont(model) != "" {
		avoid = append(avoid, "Sans-serif fonts for labels/eyebrows/metadata — use the template's monospace font")
	}

	// Check if template has minimal chrome (few decorative objects)
	totalChrome := 0
	for _, layout := range model.Layouts {
		totalChrome += len(layout.Objects)
	}
	if len(model.Layouts) > 0 && totalChrome/len(model.Layouts) < 3 {
		avoid = append(avoid, "Heavy decorative borders, ornamental dividers, or decorative shapes")
	}

	return avoid
}

// GenerateComponentPatterns analyzes flexible archetypes and produces a
// ComponentPattern for each, describing its visual structure, fill slots,
// usage recommendations, and chrome notes. Fixed-tier archetypes are excluded.
func GenerateComponentPatterns(archetypes []Archetype) []ComponentPattern {
	if len(archetypes) == 0 {
		return nil
	}

	var patterns []ComponentPattern
	for _, a := range archetypes {
		// Only include flexible-tier archetypes (tier=="" or tier=="flexible")
		if a.Tier == "fixed" {
			continue
		}
		// Skip blank/empty archetypes and "blank" kind
		if a.Markup == "" || a.Kind == "blank" {
			continue
		}

		markup := a.Markup
		cp := ComponentPattern{
			ArchetypeLabel: a.Title,
			Kind:           a.Kind,
			FillSlots:      a.FillSlots,
		}

		// Analyze fill slots to understand structure
		picSlots := 0
		textSlots := 0
		for _, slot := range a.FillSlots {
			if strings.HasPrefix(slot, "ph-pic-") {
				picSlots++
			} else {
				textSlots++
			}
		}

		// Detect column count from fill-slot x-positions
		cols := detectColumnsFromMarkup(markup, a.FillSlots)

		// Detect roundRect cards and image elements
		roundRects := strings.Count(markup, `geom="roundRect"`)
		imageCount := countNonDecorativeImages(markup)
		// Also count image placeholder shapes (alt="image-*" shapes used as swappable image areas)
		imageCount += countImagePlaceholderShapes(markup)

		// Total image areas: use the larger of picSlots and detected images
		// (ph-pic-* slots and alt="image-*" shapes often refer to the same elements)
		totalImages := picSlots
		if imageCount > totalImages {
			totalImages = imageCount
		}

		// Build visual summary using all signals
		cp.VisualSummary = buildVisualSummary(a.Title, roundRects, totalImages, cols, textSlots, markup)

		// Build usage rule based on detected characteristics
		cp.UsageRule = buildUsageRule(cols, totalImages, roundRects, a.Title)

		// Build chrome note - simplified for large markups
		totalShapes := strings.Count(markup, "<ast-shape ")
		if strings.HasPrefix(a.Kind, "pattern") {
			cp.ChromeNote = "Sample-derived designed slide — fill_slides substitutes fillSlots; cards/boxes stay."
		} else {
			cp.ChromeNote = buildChromeNoteSmart(totalShapes, roundRects, picSlots, imageCount, markup)
		}

		patterns = append(patterns, cp)
	}

	return patterns
}

// countImagePlaceholderShapes counts ast-shape elements with alt="image-*"
// which are image placeholder areas (user can swap in a real image).
func countImagePlaceholderShapes(markup string) int {
	count := 0
	idx := 0
	for {
		pos := strings.Index(markup[idx:], `alt="image-`)
		if pos == -1 {
			break
		}
		count++
		idx += pos + 1
	}
	return count
}

// countNonDecorativeImages counts ast-image elements that are NOT decorative
// (i.e., image placeholders the user could fill with content).
func countNonDecorativeImages(markup string) int {
	// Count all ast-image elements
	total := strings.Count(markup, "<ast-image ")
	// Count decorative ones
	decorative := 0
	idx := 0
	for {
		pos := strings.Index(markup[idx:], "<ast-image ")
		if pos == -1 {
			break
		}
		start := idx + pos
		end := strings.Index(markup[start:], ">")
		if end == -1 {
			break
		}
		tag := markup[start : start+end+1]
		if strings.Contains(tag, `decorative="true"`) {
			decorative++
		}
		idx = start + end + 1
	}
	return total - decorative
}

// detectColumnsFromMarkup estimates column count by analyzing x-positions of
// fill-slot elements in the markup.
func detectColumnsFromMarkup(markup string, fillSlots []string) int {
	if len(fillSlots) < 2 {
		return 1
	}

	// Extract x-positions of fill slot elements
	var xPositions []int
	for _, slot := range fillSlots {
		// Find the element with this id in the markup
		idSearch := fmt.Sprintf(`id="%s"`, slot)
		pos := strings.Index(markup, idSearch)
		if pos == -1 {
			continue
		}
		// Look backward for x= attribute (within the same tag)
		tagStart := strings.LastIndex(markup[:pos], "<")
		if tagStart == -1 {
			continue
		}
		tag := markup[tagStart:min(pos+len(idSearch)+200, len(markup))] // grab enough context
		if endIdx := strings.Index(tag, ">"); endIdx > 0 {
			tag = tag[:endIdx]
		}
		xVal := extractAttrInt(tag, "x")
		if xVal > 0 {
			xPositions = append(xPositions, xVal)
		}
	}

	if len(xPositions) < 2 {
		return 1
	}

	// Cluster x-positions: if we find distinct clusters separated by >200px, count columns
	sort.Ints(xPositions)
	cols := 1
	lastCluster := xPositions[0]
	for _, x := range xPositions[1:] {
		if x-lastCluster > 200 {
			cols++
			lastCluster = x
		}
	}
	return cols
}

// extractAttrInt extracts an integer attribute value from an XML tag string.
func extractAttrInt(tag, attr string) int {
	search := attr + `="`
	pos := strings.Index(tag, search)
	if pos == -1 {
		return 0
	}
	start := pos + len(search)
	end := strings.Index(tag[start:], `"`)
	if end == -1 {
		return 0
	}
	val := 0
	for _, c := range tag[start : start+end] {
		if c >= '0' && c <= '9' {
			val = val*10 + int(c-'0')
		} else {
			break
		}
	}
	return val
}

// buildVisualSummary generates a human-readable description of a layout's visual structure.
func buildVisualSummary(title string, roundRects, imageCount, cols, phCount int, markup string) string {
	var parts []string

	// Detect overall structure
	titleLower := strings.ToLower(title)

	if cols >= 3 {
		parts = append(parts, fmt.Sprintf("%d-column grid layout", cols))
	} else if cols == 2 {
		if imageCount > 0 {
			parts = append(parts, "Two-panel layout with text on one side and image area on the other")
		} else {
			parts = append(parts, "Two-column layout for side-by-side content")
		}
	} else {
		parts = append(parts, "Single-column layout")
	}

	if roundRects > 0 {
		parts = append(parts, fmt.Sprintf("with %d rounded-corner card shape(s) for structured content", roundRects))
	}

	if imageCount > 0 {
		parts = append(parts, fmt.Sprintf("with %d image placeholder area(s)", imageCount))
	}

	// Detect specific patterns from the title
	if strings.Contains(titleLower, "screenshot") {
		parts = append(parts, "— designed for pairing text with a screenshot or UI capture")
	} else if strings.Contains(titleLower, "quote") {
		parts = append(parts, "— designed for a featured quote or testimonial")
	} else if strings.Contains(titleLower, "full bleed") {
		parts = append(parts, "— full-canvas image with minimal overlay text")
	}

	// Note the fillable slots
	if phCount > 0 {
		parts = append(parts, fmt.Sprintf("(%d fillable text slot(s))", phCount))
	}

	return strings.Join(parts, " ")
}

// buildUsageRule determines when to pick this layout based on its characteristics.
func buildUsageRule(cols, imageCount, roundRects int, title string) string {
	titleLower := strings.ToLower(title)

	// Specific layout name-based rules take priority
	if strings.Contains(titleLower, "screenshot") {
		return "Use when content pairs explanatory text with a screenshot, UI mockup, or diagram"
	}
	if strings.Contains(titleLower, "quote") {
		return "Use for featured quotes, testimonials, or key statements"
	}
	if strings.Contains(titleLower, "full bleed") {
		return "Use when a single dramatic image should dominate the slide"
	}

	// Structure-based rules
	if cols >= 3 {
		return "Use for parallel items, metrics grid, comparison cards, or feature highlights"
	}
	if cols == 2 && imageCount > 0 {
		return "Use when content pairs narrative text with a visual (photo, screenshot, diagram)"
	}
	if cols == 2 {
		return "Use for side-by-side comparison, pros/cons, or two complementary ideas"
	}
	if roundRects > 0 {
		return "Use for structured callouts, feature highlights, or key-point cards with visual emphasis"
	}
	if imageCount > 0 {
		return "Use when content benefits from a supporting image or visual"
	}
	return "Use for narrative text, bullet points, or focused single-topic content"
}

// buildChromeNoteSmart generates a concise chrome description suitable for
// large archetypes with many master-chrome shapes. Instead of counting all
// shapes (which produces useless "121 shapes" notes), it focuses on the
// structural elements that matter for content authoring.
func buildChromeNoteSmart(totalShapes, roundRects, picSlots, imageCount int, markup string) string {
	var notes []string

	if roundRects > 0 {
		notes = append(notes, fmt.Sprintf("%d rounded-corner card(s)", roundRects))
	}
	if picSlots > 0 {
		notes = append(notes, fmt.Sprintf("%d image placeholder area(s)", picSlots))
	} else if imageCount > 0 {
		notes = append(notes, fmt.Sprintf("%d image area(s)", imageCount))
	}

	// For large markups (slide-master chrome), note it concisely
	if totalShapes > 10 {
		notes = append(notes, "branded page chrome (background, footer, decorative elements)")
	} else if totalShapes > 2 {
		notes = append(notes, fmt.Sprintf("%d accent/background shapes", totalShapes-1))
	}

	if len(notes) == 0 {
		return "Branded background and accent elements — preserve all, change only text in fillSlots"
	}

	return "Contains " + strings.Join(notes, ", ") + " — copy archetype VERBATIM, change only fillSlots text"
}

func isDarkTemplate(tokens map[string]string) bool {
	surface := tokens["surface"]
	if surface == "" {
		return false
	}
	// Simple luminance check: dark surfaces have low R+G+B
	if len(surface) == 7 && surface[0] == '#' {
		r := hexVal(surface[1:3])
		g := hexVal(surface[3:5])
		b := hexVal(surface[5:7])
		return (r+g+b)/3 < 128
	}
	return false
}

func hexVal(s string) int {
	var v int
	for _, c := range s {
		v *= 16
		switch {
		case c >= '0' && c <= '9':
			v += int(c - '0')
		case c >= 'a' && c <= 'f':
			v += int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v += int(c-'A') + 10
		}
	}
	return v
}

// generateMarkdown assembles all style guide components into a single LLM-readable document.
func generateMarkdown(sg *StyleGuide, tokens map[string]string) string {
	var b strings.Builder

	b.WriteString("# Style Guide\n\n")

	// Typography Scale
	if len(sg.TypographyScale) > 0 {
		b.WriteString("## Typography Scale\n")
		b.WriteString("| Role | Size | Weight | Font | Usage |\n")
		b.WriteString("|------|------|--------|------|-------|\n")
		for _, t := range sg.TypographyScale {
			font := t.Font
			if font == "" {
				font = "-"
			}
			b.WriteString(fmt.Sprintf("| %s | %dpx | %s | %s | %s |\n",
				t.Role, t.FontSize, t.Weight, font, t.Usage))
		}
		b.WriteString("\n")
	}

	// Color Palette
	if len(sg.ColorRoles) > 0 {
		b.WriteString("## Color Palette & Usage\n")
		b.WriteString("| Role | Color | Usage | Limit |\n")
		b.WriteString("|------|-------|-------|-------|\n")
		for _, c := range sg.ColorRoles {
			limit := c.Limit
			if limit == "" {
				limit = "-"
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
				c.Name, c.Color, c.Usage, limit))
		}
		b.WriteString("\n")
	}

	// Spacing System
	if sg.SpacingSystem != nil {
		sp := sg.SpacingSystem
		b.WriteString("## Spacing System\n")
		b.WriteString(fmt.Sprintf("- Page margins: %dpx horizontal, %dpx vertical\n", sp.PageMarginX, sp.PageMarginY))
		b.WriteString(fmt.Sprintf("- Title → body gap: %dpx\n", sp.TitleBodyGap))
		b.WriteString(fmt.Sprintf("- Between elements: %dpx\n", sp.ElementGap))
		b.WriteString(fmt.Sprintf("- Between sections: %dpx\n", sp.SectionGap))
		b.WriteString(fmt.Sprintf("- Content starts at Y=%dpx (after title area)\n", sp.ContentStartY))
		b.WriteString("\n")
	}

	// Font Pairing
	if sg.FontPairing != nil {
		fp := sg.FontPairing
		b.WriteString("## Font Pairing\n")
		b.WriteString(fmt.Sprintf("- **Headlines**: %s\n", fp.DisplayUsage))
		b.WriteString(fmt.Sprintf("- **Body text**: %s\n", fp.BodyUsage))
		if fp.MonoFont != "" {
			b.WriteString(fmt.Sprintf("- **Labels/metadata**: %s (monospace — uppercase, letter-spacing for visual distinction)\n", fp.MonoFont))
		}
		b.WriteString("\n")
	}

	// Layout Patterns
	if len(sg.LayoutPatterns) > 0 {
		b.WriteString("## Layout Patterns\n")
		for _, lp := range sg.LayoutPatterns {
			b.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", lp.Name, lp.Kind, lp.Description))
		}
		b.WriteString("\n")
	}

	// Content Layout Guide (Component Patterns)
	if len(sg.ComponentPatterns) > 0 {
		b.WriteString("## Content Layout Guide (FLEXIBLE Archetypes)\n\n")
		b.WriteString("Each entry is a fillable layout or sample-derived pattern. Author it with fill_slides\n")
		b.WriteString("(kind or label + fills map). Prefer pattern-* entries for body slides — they carry the\n")
		b.WriteString("template's cards and colored boxes. Do not rebuild chrome by hand.\n\n")
		for _, cp := range sg.ComponentPatterns {
			b.WriteString(fmt.Sprintf("### %s (kind: %s)\n", cp.ArchetypeLabel, cp.Kind))
			b.WriteString(fmt.Sprintf("**Looks like:** %s\n", cp.VisualSummary))
			if len(cp.FillSlots) > 0 {
				b.WriteString(fmt.Sprintf("**Fill slots:** %s\n", strings.Join(cp.FillSlots, ", ")))
			}
			b.WriteString(fmt.Sprintf("**Use when:** %s\n", cp.UsageRule))
			if cp.ChromeNote != "" {
				b.WriteString(fmt.Sprintf("**Chrome:** %s\n", cp.ChromeNote))
			}
			b.WriteString("\n")
		}
	}

	// Avoid List
	if len(sg.AvoidList) > 0 {
		b.WriteString("## DO NOT (Avoid List)\n")
		for _, item := range sg.AvoidList {
			b.WriteString(fmt.Sprintf("- %s\n", item))
		}
		b.WriteString("\n")
	}

	// Append universal rules
	b.WriteString(DefaultStyleRules())

	return b.String()
}

// Helper: median of a slice of ints.
func median(vals []int) int {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]int, len(vals))
	copy(sorted, vals)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}

// Helper: absolute value.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
