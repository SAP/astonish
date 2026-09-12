package agent

import (
	"strings"
	"testing"
)

func TestSystemPromptBuilderExternal(t *testing.T) {
	builder := &SystemPromptBuilder{
		CustomPrompt:        "Use the customer's preferred format.",
		InstructionsContent: "Be concise.",
		RelevantKnowledge:   "must not be retained in stable external prompt",
		RelevantTools:       "must not be retained in stable external prompt",
	}

	prompt := builder.BuildExternal()
	for _, want := range []string{
		"Use the customer's preferred format.",
		"## Behavior Instructions",
		"## External MCP Tool Loop",
		"get_agent_context",
		"search_tools",
		"describe_tools",
		"execute_tool",
		"Available Skills",
		"skill_lookup",
		"Astonish's returned catalog",
		"Local PATH, environment-variable, SDK, or package probes outside Astonish do not establish",
		"credential names or placeholders",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("external prompt missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"must not be retained in stable external prompt",
		"call them directly",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("external prompt exposed native-only content %q", forbidden)
		}
	}
}
