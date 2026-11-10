package agent

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotPromptHomeDeliveryModes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		req        bot.RunRequest
		background bool
	}{
		{"scheduled", bot.RunRequest{Run: store.BotRun{Trigger: store.TriggerCron}}, true},
		{"autonomous", bot.RunRequest{}, true},
		{"interactive", bot.RunRequest{Run: store.BotRun{ExecutionMode: execution.BotExecutionModeChatInteractive}}, false},
		{"home_conversation", bot.RunRequest{Run: store.BotRun{Intent: store.BotIntentConversation}}, false},
		{"control_only", bot.RunRequest{Run: store.BotRun{Intent: store.BotIntentDraftConversation}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &SystemPromptBuilder{}
			b.applyBotPrompt(NewBotPromptOverrides(nil, tc.req))
			prompt := b.Build()
			for _, want := range []string{"Your final answer is automatically delivered to this Bot's own Home chat", "Do not search for or invoke a posting tool", "exact destination and action"} {
				if strings.Contains(prompt, want) != tc.background {
					t.Errorf("background=%v: contract %q", tc.background, want)
				}
			}
			for _, want := range []string{"inspect actual sources", "request specific corrections", "deliver truthfully", "Model assessment is judgment", "Do not repeat writes or sends", "One occurrence does not complete an ongoing goal"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("missing responsibility guidance %q", want)
				}
			}
			if !strings.Contains(prompt, "unknown effects are never allowed") {
				t.Fatal("lost authority boundary")
			}
		})
	}
	b := &SystemPromptBuilder{CodeMode: true}
	if b.applyBotPrompt(NewBotPromptOverrides(nil, bot.RunRequest{})) || strings.Contains(b.Build(), "automatically delivered") {
		t.Fatal("Bot delivery leaked into Code mode")
	}
}

// This is a prompt contract test, not evidence that batching caused events 84/90.
func TestBotPromptSerialToolGeneration(t *testing.T) {
	b := &SystemPromptBuilder{}
	b.applyBotPrompt(NewBotPromptOverrides(nil, bot.RunRequest{}))
	for _, want := range []string{"at most one tool call per model response", "Wait for its result before generating the next tool call", "Do not batch or parallelize tool calls"} {
		if !strings.Contains(b.Build(), want) {
			t.Errorf("missing serial guidance %q", want)
		}
	}
}
