package agent

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotRoutineScheduleConsentPrompt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		run     store.BotRun
		routine bool
	}{
		{"interactive-chat", store.BotRun{ExecutionMode: execution.BotExecutionModeChatInteractive}, true},
		{"home-owner-turn", store.BotRun{Intent: store.BotIntentRegular, Presentation: store.BotRunPresentationHomeInteractive}, true},
		{"control-only", store.BotRun{Intent: store.BotIntentDraftConversation}, false},
		{"background", store.BotRun{Trigger: store.TriggerCron}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &SystemPromptBuilder{}
			b.applyBotPrompt(NewBotPromptOverrides(nil, bot.RunRequest{Run: tc.run}))
			prompt := b.Build()
			for _, want := range []string{"RequireAuthorization", "unknown effects are never allowed", "pending worker commit", "explicit user consent", "Never parse prose as consent"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("lost boundary %q", want)
				}
			}
			const direct = "An explicit owner request for recurring work within the current bounded mandate"
			if !tc.routine {
				if strings.Contains(prompt, direct) {
					t.Fatal("owner scheduling guidance leaked into non-executing/background mode")
				}
				return
			}
			for _, want := range []string{direct, "call schedule_job", "do not ask for redundant frequency confirmation", "even every minute", "minimum interval and budgets", "actual ambiguity", "unknown destination", "cost or policy limit"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("missing routine scheduling guidance %q", want)
				}
			}
			if strings.Contains(prompt, "Background responsibilities remain separate and require review.") {
				t.Error("unconditional review contradicts routine mandate")
			}
		})
	}
}

func TestSchedulingGuidanceRoutineConsent(t *testing.T) {
	for _, want := range []string{"explicit owner request", "current bounded mandate", "do not ask for redundant frequency confirmation", "minimum interval and budgets", "legacy non-mandated", "RequireAuthorization"} {
		if !strings.Contains(guidanceJobScheduling, want) {
			t.Errorf("missing scheduling guidance %q", want)
		}
	}
	if strings.Contains(guidanceJobScheduling, "Never test, activate, or enable recurrence silently.") {
		t.Error("unqualified silent recurrence warning conflicts with explicit owner request")
	}
}
