package agent

import (
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"strings"
	"testing"
)

func TestBotInteractivePromptUsesCurrentChatWork(t *testing.T) {
	po := NewBotPromptOverrides(nil, bot.RunRequest{Bot: store.Bot{Name: "Orbit"}, Run: store.BotRun{ExecutionMode: execution.BotExecutionModeChatInteractive, Intent: store.BotIntentDraftConversation}})
	builder := &SystemPromptBuilder{}
	if !builder.applyBotPrompt(po) {
		t.Fatal("missing bot prompt")
	}
	for _, want := range []string{"useful work for the current request", "ask_user", "Do not require task_once"} {
		if !strings.Contains(builder.SchedulerHints, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(builder.SchedulerHints, "control-only conversation") || strings.Contains(builder.SchedulerHints, "Only after configuration review") {
		t.Fatal("autonomous onboarding leaked into chat")
	}
}
