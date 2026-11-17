package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Instruction delivery is deterministic; actual model judgment needs separate
// provider evaluation, not a scripted model pretending to choose research depth.
func assertBotProportionalRigor(t *testing.T, prompt string) {
	t.Helper()
	for _, want := range []string{
		"Autonomy changes responsibility, not the requested standard of proof",
		"Substantive, attributed search summaries and credible secondary reporting can support an ordinary overview",
		"not a mandatory fetch for every item",
		"Preserve relevant, reasonably supported breadth",
		"A blocked primary page alone is not a reason to discard",
		"For an ordinary roundup, substantive sourced search output is usable by default",
		"Primary-source fetches fill a material gap, not a routine verification step",
		"Explicit verification requests and high-stakes decisions or actions require stronger sources",
		"An older page, a different version or a failed fetch is not contradictory evidence by itself",
		"Preserve distinct relevant developments already supported by returned coverage",
		"shorten individual stories before dropping supported coverage",
		"not a verification diary",
		"Do not present weak or contradictory claims as certain",
		"Each additional research call should address a material gap",
		"Do not add specialist details that create unnecessary verification work",
		"Do not ask a rigor questionnaire",
		"when the answer would materially change the work",
		"Do not save assistant-chosen rigor as an owner requirement",
		"External actions still require authority and committed execution evidence",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing proportional rigor guidance %q", want)
		}
	}
	for _, forbidden := range []string{
		"For actionable facts inspect the primary/live source",
		"Open the decisive original article or primary source before relying on consequential claims, including actionable numbers",
		"Inspect decisive articles or primary documents selectively for consequential details such as availability, licensing, prices and performance",
		"Listing headlines, search snippets and URL slugs are discovery leads",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("unqualified research rule competes with composition guidance: %q", forbidden)
		}
	}
}

func TestBotPromptProportionalRigor(t *testing.T) {
	for _, mode := range []struct {
		name string
		run  store.BotRun
	}{
		{"chat", store.BotRun{ExecutionMode: execution.BotExecutionModeChatInteractive}},
		{"home", store.BotRun{Presentation: store.BotRunPresentationHomeInteractive}},
		{"scheduled", store.BotRun{Trigger: store.TriggerCron}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			mode.run.ID = uuid.New()
			req := bot.RunRequest{Bot: store.Bot{ID: uuid.New()}, Run: mode.run, Version: store.BotVersion{Version: 1}, Instructions: "Summarize new model releases"}
			req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: req.Instructions})
			po := NewBotPromptOverrides(nil, req)
			b := &SystemPromptBuilder{}
			b.applyBotPrompt(po)
			ctx := bot.WithBotWorkServices(context.Background(), botPromptWorkReader{read: func(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) {
				return &bot.BotWorkReadResult{Work: store.BuildBotWorkProjection(req.Work)}, nil
			}})
			for _, prompt := range []string{b.Build(), b.BuildBotPrompt(ctx, po)} {
				assertBotProportionalRigor(t, prompt)
				if strings.Contains(prompt, "Listing headlines, search snippets and URL slugs are discovery leads") {
					t.Error("blanket search-summary exclusion remains")
				}
			}
			if req.Instructions != req.Work.Contract.Instructions || buildTurnContextContent(po, "", "") != nil {
				t.Fatal("guidance changed the owner request")
			}
		})
	}
}

func TestBotPromptRigorDoesNotChangeTitleOrCode(t *testing.T) {
	title := &SystemPromptBuilder{}
	title.applyBotPrompt(NewBotPromptOverrides(nil, bot.RunRequest{Run: store.BotRun{TitleConfirmation: &store.BotTitleConfirmation{State: store.BotTitleConfirmationNeedsQuestion}}}))
	if strings.Contains(title.Build(), "Autonomy changes responsibility") || !strings.Contains(title.SchedulerHints, "Do not research") {
		t.Fatal("title confirmation changed")
	}
	code := &SystemPromptBuilder{CodeMode: true}
	before := code.Build()
	if code.applyBotPrompt(NewBotPromptOverrides(nil, bot.RunRequest{})) || code.Build() != before {
		t.Fatal("Code mode changed")
	}
}
