package skills

// BuiltinSkills returns skills that are always available regardless of
// platform/org/team configuration. These ship with the binary and provide
// essential guidance that the LLM must load before performing certain tasks.
//
// Built-in skills are:
// - Always included in the "Available Skills" index in the system prompt
// - Always resolvable via skill_lookup (as a fallback after DB stores)
// - Never require validation (they ship with the binary)
func BuiltinSkills() []Skill {
	return []Skill{
		{
			Name:        "generative-ui",
			Description: "Complete API docs, design system, and examples for building visual apps (astonish-app). MUST load before generating any visual app.",
			Content:     BuiltinGenerativeUI,
			Source:      "builtin",
			// ExcludeFromCodeMode: generative-ui produces astonish-app fences which
			// are Studio-only. In Astonish Code mode there is no app renderer, so
			// advertising this skill causes the coding agent to generate the wrong
			// kind of app. Keep it resolvable via skill_lookup for explicit calls,
			// but hide it from the Available Skills index and the /skills picker.
			ExcludeFromCodeMode: true,
		},
		{
			Name:        "slides",
			Description: "Complete reference for building styled Astonish Slides decks (ASD v2): elements, attributes, gradients/rich text, the template workflow (list_templates -> create_deck template -> archetypes), and requirement-gathering tips. Load before authoring any presentation/PowerPoint/deck.",
			Content:     BuiltinSlides,
			Source:      "builtin",
			// Not ExcludeFromCodeMode: slide decks are server-side artifacts
			// usable wherever the slide tools are present, in both platform and
			// code modes, so this skill should resolve and be discoverable in
			// both. BuiltinSkillsForCode() will therefore include it.
		},
	}
}

// BuiltinSkillsForCode returns the subset of BuiltinSkills() that is
// appropriate for Astonish Code mode. Skills with ExcludeFromCodeMode set are
// omitted so they do not appear in the system-prompt skill index or the
// /skills picker, preventing the coding agent from accidentally generating
// Studio-only artifacts (e.g. astonish-app fences).
func BuiltinSkillsForCode() []Skill {
	all := BuiltinSkills()
	filtered := make([]Skill, 0, len(all))
	for _, s := range all {
		if !s.ExcludeFromCodeMode {
			filtered = append(filtered, s)
		}
	}
	return filtered
}
