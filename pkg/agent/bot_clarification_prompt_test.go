package agent

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotClarificationContractIsInteractiveOnly(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		req := bot.RunRequest{Bot: store.Bot{Name: "Orbit"}}
		if interactive {
			req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
		}
		b := &SystemPromptBuilder{}
		b.applyBotPrompt(NewBotPromptOverrides(nil, req))
		prompt := b.Build()
		for _, clause := range []string{"call the actual ask_user tool", "kind=text", "kind=select with allow_custom_answer=true", "do the useful work first", "instead of asking questions in plain chat text", "The user does not need to say to call a tool.", "Default scheduled delivery is this Bot's own home chat", "Do not ask who should receive", "explicitly requests an external destination"} {
			if strings.Contains(prompt, clause) != interactive {
				t.Errorf("interactive=%v clause=%q", interactive, clause)
			}
		}
	}
	ordinary := (&SystemPromptBuilder{}).Build()
	if strings.Contains(ordinary, "call the actual ask_user tool") {
		t.Fatal("Bot-only instruction leaked to ordinary Chat")
	}
}
