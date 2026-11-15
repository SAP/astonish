package agent

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotBroadAssignmentPlansDeliverableBeforeResearch(t *testing.T) {
	for _, title := range []string{"AI News", "Stock Market News", "Garden care", "Fix the failing import", "Analyze the sales spreadsheet", "Revise the onboarding document"} {
		t.Run(title, func(t *testing.T) {
			req := bot.RunRequest{Instructions: title, Bot: store.Bot{Name: title}, Run: store.BotRun{ExecutionMode: execution.BotExecutionModeChatInteractive}}
			b := &SystemPromptBuilder{}
			b.applyBotPrompt(NewBotPromptOverrides(nil, req))
			prompt := b.Build()
			for _, want := range []string{"Before the first work tool call", "shortest reliable approach", "working assumptions, not owner requirements", "Do not let the first search results define", "material gaps", "not a fixed topic checklist", "shared origin", "presentation-only", "optional enrichment", "no extra planning-model call"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("missing deliverable planning contract %q", want)
				}
			}
			if req.Instructions != title {
				t.Fatal("planning rewrote the owner's request")
			}
		})
	}
}
