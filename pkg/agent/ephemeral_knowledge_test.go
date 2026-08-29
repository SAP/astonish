package agent

import (
	"fmt"
	"iter"
	"maps"
	"strings"
	"testing"
)

func TestBuildTurnContextContent(t *testing.T) {
	content := buildTurnContextContent(&PromptOverrides{
		ChannelHints:   "Use plain text.",
		SchedulerHints: "Return only the result.",
		SessionContext: "Continue the fleet wizard.",
		SkillIndex:     "- deploy: Deploy services",
		PlanMode:       true,
	}, "- send_email", "Host is 10.0.0.4")
	if content == nil || content.Role != "user" || len(content.Parts) != 1 {
		t.Fatalf("unexpected content: %#v", content)
	}
	got := content.Parts[0].Text
	for _, want := range []string{
		"[Astonish Per-Turn Context — not user-authored]",
		"## Output Constraints\n\nUse plain text.",
		"## Execution Context\n\nReturn only the result.",
		"## Session Task\n\nContinue the fleet wizard.",
		"## Available Skills For This Request\n\n- deploy: Deploy services",
		"## Runtime Mode\n\nPlan mode is active.",
		"## Relevant Tools For This Request",
		"- send_email",
		"## Knowledge For This Task",
		"Host is 10.0.0.4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("context missing %q", want)
		}
	}
}

func TestBuildTurnContextContentEmpty(t *testing.T) {
	if got := buildTurnContextContent(nil, "", ""); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestTurnContextEventIsPersistedModelContentButHidden(t *testing.T) {
	content := buildTurnContextContent(nil, "", "remember this")
	event := newTurnContextEvent(content)
	if !IsTurnContextEvent(event) {
		t.Fatal("expected marked turn context event")
	}
	if event.Author != "user" || event.Content == nil || event.Content.Parts[0].Text != content.Parts[0].Text {
		t.Fatalf("event did not preserve exact model-facing content: %#v", event)
	}
	if got := CleanUserText(event.Content.Parts[0].Text); got != "" {
		t.Fatalf("turn context leaked as clean user text: %q", got)
	}
}

func TestStableSystemPromptBuildsAndPersistsOnce(t *testing.T) {
	state := &testPromptState{values: map[string]any{}}
	builds := 0
	prompt, event := stableSystemPrompt(state, func() string {
		builds++
		return "stable prompt"
	})
	if prompt != "stable prompt" || event == nil || builds != 1 {
		t.Fatalf("unexpected initial result: prompt=%q event=%#v builds=%d", prompt, event, builds)
	}
	if event.Content != nil {
		t.Fatal("system prompt state event must not enter model contents")
	}
	if got := event.Actions.StateDelta[systemPromptStateKey]; got != prompt {
		t.Fatalf("unexpected persisted prompt: %#v", got)
	}

	state.values[systemPromptStateKey] = prompt
	resumed, event := stableSystemPrompt(state, func() string {
		builds++
		return "changed prompt"
	})
	if resumed != prompt || event != nil || builds != 1 {
		t.Fatalf("resume did not reuse prompt: prompt=%q event=%#v builds=%d", resumed, event, builds)
	}
}

func TestStableAgentPathPersistsSelection(t *testing.T) {
	state := &testPromptState{values: map[string]any{}}
	selected, event := stableAgentPath(state, true)
	if !selected || event == nil {
		t.Fatalf("first selection = %v, event=%v", selected, event)
	}
	for key, value := range event.Actions.StateDelta {
		state.values[key] = value
	}
	selected, event = stableAgentPath(state, false)
	if !selected || event != nil {
		t.Fatalf("persisted selection = %v, event=%v", selected, event)
	}
}

type testPromptState struct {
	values map[string]any
}

func (s *testPromptState) Get(key string) (any, error) {
	value, ok := s.values[key]
	if !ok {
		return nil, fmt.Errorf("missing")
	}
	return value, nil
}

func (s *testPromptState) Set(key string, value any) error {
	s.values[key] = value
	return nil
}

func (s *testPromptState) All() iter.Seq2[string, any] {
	return maps.All(s.values)
}
