package skills

import (
	"strings"
	"testing"
)

func findSkill(skills []Skill, name string) (Skill, bool) {
	for _, s := range skills {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

func TestBuiltinSkills_IncludesSlides(t *testing.T) {
	slides, ok := findSkill(BuiltinSkills(), "slides")
	if !ok {
		t.Fatal("BuiltinSkills() must contain a skill named \"slides\"")
	}
	if slides.Content == "" {
		t.Error("slides skill Content must be non-empty")
	}
	if slides.Content != BuiltinSlides {
		t.Error("slides skill Content must be BuiltinSlides")
	}
	if !strings.Contains(slides.Description, "template") && !strings.Contains(slides.Description, "presentation") {
		t.Errorf("slides skill Description should mention template/presentation, got %q", slides.Description)
	}
	if slides.ExcludeFromCodeMode {
		t.Error("slides skill must not be ExcludeFromCodeMode")
	}
	for _, want := range []string{
		"slidesTemplatePicker",
		"The template choice belongs to the user",
		"Inferring a tone",
		"list_slide_templates",
		"one short headline",
		"Title and Text is last resort",
		"fill_slides",
		"one LLM round-trip per slide",
		"At most ONE section divider",
	} {
		if !strings.Contains(slides.Content, want) {
			t.Errorf("slides skill must contain %q (visual template picker is mandatory)", want)
		}
	}
}

func TestBuiltinSkillsForCode_IncludesSlidesExcludesGenerativeUI(t *testing.T) {
	forCode := BuiltinSkillsForCode()
	if _, ok := findSkill(forCode, "slides"); !ok {
		t.Error("BuiltinSkillsForCode() must include \"slides\" (not excluded from code mode)")
	}
	if _, ok := findSkill(forCode, "generative-ui"); ok {
		t.Error("BuiltinSkillsForCode() must still exclude \"generative-ui\"")
	}
}
