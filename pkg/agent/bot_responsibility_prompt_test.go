package agent

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotOwnerTemporalTimezone(t *testing.T) {
	for _, mode := range []string{"chat", "control", "background"} {
		t.Run(mode, func(t *testing.T) {
			req := bot.RunRequest{Version: store.DefaultBotVersion(), Run: store.BotRun{Trigger: store.TriggerCron}}
			if mode == "chat" {
				req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
			}
			if mode == "control" {
				req.Run.Intent = store.BotIntentConversation
			}
			b := &SystemPromptBuilder{}
			b.applyBotPrompt(NewBotPromptOverrides(nil, req))
			for _, want := range []string{"previously owner-selected timezone", "schedule, persona or server default (including UTC) is not evidence", "clarify the timezone before saving", "report the missing timezone as a blocker", "Relative cadence alone"} {
				if !strings.Contains(b.Build(), want) {
					t.Errorf("missing timezone rule %q", want)
				}
			}
		})
	}
}

func TestBotResponsibilityPrecedence(t *testing.T) {
	req := bot.RunRequest{Version: store.BotVersion{Charter: "After 4PM America/New_York, cover the next trading day."}, Run: store.BotRun{Trigger: store.TriggerCron}, Instructions: "Cover today's market news."}
	b := &SystemPromptBuilder{}
	b.applyBotPrompt(NewBotPromptOverrides(nil, req))
	prompt := b.Build()
	for _, want := range []string{"applicable charter and goal constraints pinned for this occurrence take precedence", "Keep task-specific scope", "Do not copy the charter into every trigger", "Existing runs retain their pinned contract", "targeted schedule change"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing responsibility rule %q", want)
		}
	}
	if req.Instructions != "Cover today's market news." {
		t.Fatal("rewrote saved task")
	}
}
