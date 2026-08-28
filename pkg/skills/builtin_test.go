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
	for _, want := range []string{"slides", "slide deck", "slideshow", "slide show", "presentation", "PowerPoint"} {
		if !strings.Contains(slides.Description, want) {
			t.Errorf("slides skill Description should mention %q, got %q", want, slides.Description)
		}
	}
	if slides.ExcludeFromCodeMode {
		t.Error("slides skill must not be ExcludeFromCodeMode")
	}
	for _, want := range []string{
		"slidesTemplatePicker",
		"slidesPalettePicker",
		"You pick what fits",
		"Modern",
		"Inferring a tone",
		"list_slide_templates",
		"complete thought",
		"Fill every required text slot",
		"Title and Text is last resort",
		"fill_slides",
		"one LLM round-trip per slide",
		"recipe-cover",
		"recipe-split-narrative",
		"recipe-data-table",
		"recipe-process-terminal",
		"headline_accent",
		"Pick layout by job",
		"modern",
		"official title",
		"titleKind",
		"research constraint",
		"next tool call is",
		"cannot generate them",
		"slidesImagePicker",
		"titleImage",
		"I'll provide my own image",
		"proceed without a logo",
		"never skip the title-variant question",
		"example photos",
		"add_deck_image",
		"rehost an attached file",
		"per-session slug",
		"slideshow",
		"slide show",
		"always-on system-prompt pointer is not a substitute",
		"cover-like",
		"at least once when comparing",
		"all-card deck",
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
