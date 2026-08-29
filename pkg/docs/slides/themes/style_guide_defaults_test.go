package themes

import (
	"strings"
	"testing"
)

func TestDefaultStyleRules_NonEmpty(t *testing.T) {
	rules := DefaultStyleRules()
	if rules == "" {
		t.Fatal("DefaultStyleRules returned empty string")
	}
	if len(rules) < 500 {
		t.Fatalf("DefaultStyleRules too short (%d chars), expected comprehensive guidance", len(rules))
	}
}

func TestDefaultStyleRules_ContainsKeySections(t *testing.T) {
	rules := DefaultStyleRules()
	requiredSections := []string{
		"Content Density",
		"Typography Hierarchy",
		"Color Discipline",
		"Spacing & Alignment",
		"Charts & Data",
		"What NOT to Generate",
	}
	for _, section := range requiredSections {
		if !strings.Contains(rules, section) {
			t.Errorf("DefaultStyleRules missing required section: %q", section)
		}
	}
}

func TestDefaultStyleRules_ContainsAvoidItems(t *testing.T) {
	rules := DefaultStyleRules()
	avoidItems := []string{
		"Drop shadows",
		"3-D chart",
		"clip-art",
		"Gradient fills on text",
		"Topic-label titles",
	}
	for _, item := range avoidItems {
		if !strings.Contains(rules, item) {
			t.Errorf("DefaultStyleRules missing avoid item: %q", item)
		}
	}
}

func TestDefaultStyleRules_WellFormatted(t *testing.T) {
	rules := DefaultStyleRules()
	// Should start with a markdown heading
	if !strings.HasPrefix(rules, "##") {
		t.Error("DefaultStyleRules should start with a markdown heading (##)")
	}
	// Should contain bullet points
	if !strings.Contains(rules, "- ") {
		t.Error("DefaultStyleRules should contain bullet points")
	}
	// Should not end with excessive whitespace
	trimmed := strings.TrimRight(rules, "\n ")
	if len(rules)-len(trimmed) > 2 {
		t.Error("DefaultStyleRules has excessive trailing whitespace")
	}
}
