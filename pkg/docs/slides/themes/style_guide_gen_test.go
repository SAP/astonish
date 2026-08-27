package themes

import (
	"strings"
	"testing"
)

func TestGenerateStyleGuide_NilModel(t *testing.T) {
	sg := GenerateStyleGuide(nil, nil, nil)
	if sg != nil {
		t.Fatal("expected nil for nil model")
	}
}

func TestGenerateStyleGuide_EmptyModel(t *testing.T) {
	model := &TemplateModel{Schema: 3, Size: IRSize{W: 1920, H: 1080}}
	tokens := map[string]string{
		"surface":     "#FFFFFF",
		"ink":         "#172033",
		"accent":      "#2563EB",
		"displayFont": "Inter",
		"bodyFont":    "Inter",
	}
	sg := GenerateStyleGuide(model, tokens, nil)
	if sg == nil {
		t.Fatal("expected non-nil style guide")
	}
	if sg.Markdown == "" {
		t.Fatal("expected non-empty markdown")
	}
	if len(sg.ColorRoles) == 0 {
		t.Fatal("expected color roles")
	}
	if sg.SpacingSystem == nil {
		t.Fatal("expected spacing system")
	}
	if sg.FontPairing == nil {
		t.Fatal("expected font pairing")
	}
}

func TestGenerateStyleGuide_WithLayouts(t *testing.T) {
	model := &TemplateModel{
		Schema: 3,
		Size:   IRSize{W: 1920, H: 1080},
		Layouts: []IRLayout{
			{
				ID:   "title-layout",
				Name: "Title Slide",
				Placeholders: []IRPlaceholder{
					{Name: "title-1", Type: "title", X: 120, Y: 300, W: 1680, H: 200, Style: IRTextStyle{FontSize: 84, Bold: true, FontFace: "Inter", Color: "#000000"}},
					{Name: "body-1", Type: "body", X: 120, Y: 540, W: 1680, H: 300, Style: IRTextStyle{FontSize: 34, FontFace: "Inter", Color: "#333333"}},
				},
			},
			{
				ID:   "content-layout",
				Name: "Content Slide",
				Placeholders: []IRPlaceholder{
					{Name: "title-1", Type: "title", X: 120, Y: 80, W: 1680, H: 100, Style: IRTextStyle{FontSize: 56, Bold: true, FontFace: "Inter", Color: "#000000"}},
					{Name: "body-1", Type: "body", X: 120, Y: 240, W: 800, H: 700, Style: IRTextStyle{FontSize: 28, FontFace: "Inter", Color: "#333333"}},
					{Name: "body-2", Type: "body", X: 1000, Y: 240, W: 800, H: 700, Style: IRTextStyle{FontSize: 28, FontFace: "Inter", Color: "#333333"}},
				},
				Objects: []IRChrome{
					{Kind: "text", X: 120, Y: 50, W: 300, H: 30, Text: "SECTION", Style: &IRTextStyle{FontSize: 14, FontFace: "JetBrains Mono", Color: "#2563EB"}},
				},
			},
		},
	}
	tokens := map[string]string{
		"surface":     "#FFFFFF",
		"ink":         "#172033",
		"accent":      "#2563EB",
		"accent2":     "#10B981",
		"muted":       "#F3F4F6",
		"displayFont": "Inter",
		"bodyFont":    "Inter",
	}

	sg := GenerateStyleGuide(model, tokens, nil)
	if sg == nil {
		t.Fatal("expected non-nil style guide")
	}

	// Typography should have detected sizes
	if len(sg.TypographyScale) == 0 {
		t.Fatal("expected typography scale entries")
	}
	// Should detect the largest size (84) as h1
	if sg.TypographyScale[0].FontSize != 84 {
		t.Errorf("expected h1 at 84px, got %d", sg.TypographyScale[0].FontSize)
	}

	// Should detect 2-column layout
	foundTwoCol := false
	for _, lp := range sg.LayoutPatterns {
		if lp.Columns >= 2 {
			foundTwoCol = true
		}
	}
	if !foundTwoCol {
		t.Error("expected to detect a 2-column layout pattern")
	}

	// Should detect JetBrains Mono
	if sg.FontPairing == nil || sg.FontPairing.MonoFont == "" {
		t.Error("expected to detect monospace font (JetBrains Mono)")
	}

	// Should have spacing system
	if sg.SpacingSystem == nil {
		t.Fatal("expected spacing system")
	}
	if sg.SpacingSystem.PageMarginX != 120 {
		t.Errorf("expected page margin X=120, got %d", sg.SpacingSystem.PageMarginX)
	}

	// Markdown should be non-empty and contain key sections
	if sg.Markdown == "" {
		t.Fatal("expected non-empty markdown")
	}
	for _, section := range []string{"Typography Scale", "Color Palette", "Spacing System", "Font Pairing", "DO NOT"} {
		if !strings.Contains(sg.Markdown, section) {
			t.Errorf("markdown missing section: %s", section)
		}
	}
}

func TestGenerateStyleGuide_DarkTemplate(t *testing.T) {
	model := &TemplateModel{Schema: 3, Size: IRSize{W: 1920, H: 1080}}
	tokens := map[string]string{
		"surface":     "#0A1530",
		"ink":         "#FFFFFF",
		"accent":      "#3B82F6",
		"displayFont": "IBM Plex Sans",
		"bodyFont":    "Inter",
	}

	sg := GenerateStyleGuide(model, tokens, nil)
	if sg == nil {
		t.Fatal("expected non-nil style guide")
	}

	// Avoid list should include dark-template-specific items
	foundDarkRule := false
	for _, item := range sg.AvoidList {
		if strings.Contains(item, "Light") || strings.Contains(item, "cream") || strings.Contains(item, "warm") {
			foundDarkRule = true
			break
		}
	}
	if !foundDarkRule {
		t.Error("expected dark-template-specific avoid list item")
	}
}

func TestGenerateComponentPatterns_FlexibleArchetypes(t *testing.T) {
	archetypes := []Archetype{
		{
			Kind:      "title",
			Title:     "Blue Cover",
			Tier:      "fixed",
			Markup:    `<ast-slide id="title"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#0057D2"></ast-shape></ast-slide>`,
			FillSlots: []string{"ph-1"},
		},
		{
			Kind:      "content",
			Title:     "2 Columns - Text and Images",
			Tier:      "flexible",
			Markup:    `<ast-slide id="2col-text-img"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape><ast-shape id="c-1" kind="rect" x="79" y="140" w="800" h="400" geom="roundRect" fill="#89D1FF"></ast-shape><ast-shape id="c-2" kind="rect" x="79" y="580" w="800" h="350" geom="roundRect" fill="#0057D2"></ast-shape><ast-image id="ph-pic-1" x="960" y="100" w="880" h="880" asset-ref="sha256-abc123" fit="cover" decorative="true"></ast-image><ast-text id="ph-1" x="120" y="180" w="720" h="120" size="42" color="#FFFFFF" weight="bold">{{TITLE}}</ast-text><ast-text id="ph-2" x="120" y="620" w="720" h="280" size="28" color="#FFFFFF">{{BODY}}</ast-text></ast-slide>`,
			FillSlots: []string{"ph-1", "ph-2"},
		},
		{
			Kind:      "content-2",
			Title:     "3 Columns",
			Tier:      "flexible",
			Markup:    `<ast-slide id="3col"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape><ast-text id="ph-1" x="79" y="200" w="560" h="700" size="28" color="#000000">{{BODY}}</ast-text><ast-text id="ph-2" x="680" y="200" w="560" h="700" size="28" color="#000000">{{BODY}}</ast-text><ast-text id="ph-3" x="1280" y="200" w="560" h="700" size="28" color="#000000">{{BODY}}</ast-text></ast-slide>`,
			FillSlots: []string{"ph-1", "ph-2", "ph-3"},
		},
	}

	patterns := GenerateComponentPatterns(archetypes)

	// Should exclude the fixed-tier title archetype
	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns (excluding fixed-tier), got %d", len(patterns))
	}

	// First pattern: "2 Columns - Text and Images"
	p1 := patterns[0]
	if p1.ArchetypeLabel != "2 Columns - Text and Images" {
		t.Errorf("expected label '2 Columns - Text and Images', got %q", p1.ArchetypeLabel)
	}
	if p1.Kind != "content" {
		t.Errorf("expected kind 'content', got %q", p1.Kind)
	}
	if p1.VisualSummary == "" {
		t.Error("expected non-empty VisualSummary")
	}
	if len(p1.FillSlots) != 2 {
		t.Errorf("expected 2 fill slots, got %d", len(p1.FillSlots))
	}
	if p1.UsageRule == "" {
		t.Error("expected non-empty UsageRule")
	}
	// Should detect roundRect cards
	if !strings.Contains(p1.VisualSummary, "rounded-corner card") {
		t.Errorf("expected visual summary to mention rounded-corner cards, got: %s", p1.VisualSummary)
	}
	// Should have chrome note about the cards
	if p1.ChromeNote == "" {
		t.Error("expected non-empty ChromeNote for layout with roundRect shapes")
	}
	if !strings.Contains(p1.ChromeNote, "rounded-corner") {
		t.Errorf("expected chrome note to mention rounded-corner shapes, got: %s", p1.ChromeNote)
	}

	// Second pattern: "3 Columns"
	p2 := patterns[1]
	if p2.ArchetypeLabel != "3 Columns" {
		t.Errorf("expected label '3 Columns', got %q", p2.ArchetypeLabel)
	}
	if p2.Kind != "content-2" {
		t.Errorf("expected kind 'content-2', got %q", p2.Kind)
	}
	if len(p2.FillSlots) != 3 {
		t.Errorf("expected 3 fill slots, got %d", len(p2.FillSlots))
	}
	// Should detect multi-column
	if !strings.Contains(p2.UsageRule, "parallel") && !strings.Contains(p2.UsageRule, "metrics") {
		t.Errorf("expected usage rule to mention parallel/metrics for 3-col, got: %s", p2.UsageRule)
	}
}

func TestGenerateStyleGuide_WithArchetypes(t *testing.T) {
	model := &TemplateModel{Schema: 3, Size: IRSize{W: 1920, H: 1080}}
	tokens := map[string]string{
		"surface":     "#FFFFFF",
		"ink":         "#000000",
		"accent":      "#0057D2",
		"displayFont": "72 Brand Medium",
		"bodyFont":    "72 Brand",
	}
	archetypes := []Archetype{
		{Kind: "title", Title: "Cover", Tier: "fixed", Markup: "<ast-slide></ast-slide>", FillSlots: []string{"ph-1"}},
		{Kind: "content", Title: "Text and Screenshot", Tier: "flexible", Markup: `<ast-slide id="ts"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="#FFFFFF" decorative="true"></ast-shape><ast-text id="ph-1" x="79" y="100" w="900" h="800" size="28" color="#000000">{{BODY}}</ast-text><ast-shape id="ph-pic-1" kind="rect" x="1020" y="100" w="820" h="800" geom="rect" fill="#89D1FF" alt="image-1"></ast-shape></ast-slide>`, FillSlots: []string{"ph-1"}},
	}

	sg := GenerateStyleGuide(model, tokens, archetypes)
	if sg == nil {
		t.Fatal("expected non-nil style guide")
	}

	// Should have component patterns
	if len(sg.ComponentPatterns) == 0 {
		t.Fatal("expected non-empty ComponentPatterns when archetypes provided")
	}
	// Should only include the flexible one (not the fixed title)
	if len(sg.ComponentPatterns) != 1 {
		t.Fatalf("expected 1 component pattern (excluding fixed), got %d", len(sg.ComponentPatterns))
	}
	if sg.ComponentPatterns[0].ArchetypeLabel != "Text and Screenshot" {
		t.Errorf("expected 'Text and Screenshot', got %q", sg.ComponentPatterns[0].ArchetypeLabel)
	}

	// Markdown should contain the Content Layout Guide section
	if !strings.Contains(sg.Markdown, "## Content Layout Guide") {
		t.Error("markdown missing '## Content Layout Guide' section")
	}
	if !strings.Contains(sg.Markdown, "Text and Screenshot") {
		t.Error("markdown missing archetype label 'Text and Screenshot'")
	}
	if !strings.Contains(sg.Markdown, "fill_slides") {
		t.Error("markdown missing fill_slides instruction")
	}
}

func TestGenerateStyleGuide_NilArchetypes(t *testing.T) {
	model := &TemplateModel{Schema: 3, Size: IRSize{W: 1920, H: 1080}}
	tokens := map[string]string{"surface": "#FFFFFF", "ink": "#000000"}

	sg := GenerateStyleGuide(model, tokens, nil)
	if sg == nil {
		t.Fatal("expected non-nil style guide")
	}
	// ComponentPatterns should be nil/empty when no archetypes provided
	if len(sg.ComponentPatterns) != 0 {
		t.Errorf("expected empty ComponentPatterns with nil archetypes, got %d", len(sg.ComponentPatterns))
	}
	// Markdown should NOT contain the Content Layout Guide section
	if strings.Contains(sg.Markdown, "## Content Layout Guide") {
		t.Error("markdown should not contain Content Layout Guide without archetypes")
	}
}
