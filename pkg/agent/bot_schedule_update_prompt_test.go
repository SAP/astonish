package agent

import (
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"strings"
	"testing"
)

func TestBotPreferenceOnlyScheduleGuidance(t *testing.T) {
	for _, run := range []store.BotRun{{ExecutionMode: execution.BotExecutionModeChatInteractive}, {Intent: store.BotIntentRegular, Presentation: store.BotRunPresentationHomeInteractive}, {Trigger: store.TriggerCron}} {
		b := &SystemPromptBuilder{}
		b.applyBotPrompt(NewBotPromptOverrides(nil, bot.RunRequest{Run: run}))
		for _, want := range []string{"preference-only memory request", "do not rewrite, replace or remove schedules", "job_id plus at least one", "enabled, schedule or timezone", "does not change task instructions", "pending is not applied"} {
			if !strings.Contains(b.Build(), want) {
				t.Errorf("missing %q", want)
			}
		}
	}
}
