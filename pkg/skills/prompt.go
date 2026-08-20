package skills

import (
	"fmt"
	"strings"
)

// BuildSkillIndex creates the lightweight skill listing for the system prompt.
// All installed skills are listed (eligible and ineligible) so the agent can discover
// them. Ineligible skills are marked with their missing requirements.
// Full skill content is loaded on-demand via the skill_lookup tool.
// Built-in skills are always prepended to ensure they appear regardless of
// whether any user/platform skills are configured.
func BuildSkillIndex(skills []Skill) string {
	return buildSkillIndex(skills, BuiltinSkills())
}

// BuildCodeSkillIndex is the code-mode variant of BuildSkillIndex. It uses
// BuiltinSkillsForCode() instead of BuiltinSkills() so that skills flagged
// with ExcludeFromCodeMode (e.g. generative-ui) are omitted from the
// "Available Skills" section of the coding agent's system prompt.
// This prevents the coding agent from being directed to generate
// Studio-only artifacts such as astonish-app fences.
func BuildCodeSkillIndex(skills []Skill) string {
	return buildSkillIndex(skills, BuiltinSkillsForCode())
}

// buildSkillIndex is the shared implementation used by BuildSkillIndex and
// BuildCodeSkillIndex. builtins is the caller-chosen set of built-in skills
// to prepend before the caller-provided slice.
func buildSkillIndex(skills []Skill, builtins []Skill) string {
	// Merge: builtins first, then caller-provided skills.
	// Caller skills override builtins on name collision.
	seen := make(map[string]bool, len(skills))
	for _, s := range skills {
		seen[s.Name] = true
	}

	var merged []Skill
	for _, b := range builtins {
		if !seen[b.Name] {
			merged = append(merged, b)
		}
	}
	merged = append(merged, skills...)

	if len(merged) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available Skills\n\n")
	sb.WriteString("**MANDATORY:** When a task matches any skill below, call `skill_lookup` to load it as part of your ")
	sb.WriteString("first round of tool calls. This is a context-loading step, not optional — skills provide canonical ")
	sb.WriteString("commands and up-to-date patterns that complement stored memory. Never skip this because you already ")
	sb.WriteString("have a working method from memory or knowledge.\n\n")
	sb.WriteString("**Credentials:** If a skill references environment variables for authentication (e.g. `$PVE_TOKEN`, `$AWS_ACCESS_KEY_ID`), ")
	sb.WriteString("use `resolve_credential` or `list_credentials` to find matching credentials in the store, then pass the resolved values ")
	sb.WriteString("(as `{{CREDENTIAL:name:field}}` placeholders) directly in shell commands. Do NOT skip a skill because an env var is unset.\n\n")

	for _, skill := range merged {
		if skill.IsEligible() {
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", skill.Name, skill.Description))
		} else {
			missing := skill.MissingRequirements()
			sb.WriteString(fmt.Sprintf("- **%s**: %s *(setup required: %s)*\n",
				skill.Name, skill.Description, strings.Join(missing, ", ")))
		}
	}

	return sb.String()
}
