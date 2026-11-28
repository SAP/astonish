package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Contract coverage only: live provider adherence requires separate acceptance.
func TestBotPromptReaderFacingNews(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		req := bot.RunRequest{Bot: store.Bot{ID: uuid.New()}, Run: store.BotRun{ID: uuid.New()}, Version: store.BotVersion{Version: 1}, Instructions: "AI News", Interactive: interactive}
		req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: req.Instructions})
		po := NewBotPromptOverrides(nil, req)
		builder := &SystemPromptBuilder{}
		builder.applyBotPrompt(po)
		ctx := bot.WithBotWorkServices(context.Background(), botPromptWorkReader{read: func(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) {
			return &bot.BotWorkReadResult{Work: store.BuildBotWorkProjection(req.Work)}, nil
		}})
		for _, prompt := range []string{builder.Build(), builder.BuildBotPrompt(ctx, po)} {
			for _, want := range []string{
				"reader-facing news",
				"what happened",
				"multiple substantive developments when available",
				"bare headline/category inventory is not a briefing",
				"read the actual stories",
				"other relevant specialist coverage within existing search authority",
				"Internal evidence notes are optional",
				"material uncertainty briefly and naturally",
				"source links at the bottom",
				"not an exhaustive survey or fixed story/vendor count",
			} {
				if !strings.Contains(prompt, want) {
					t.Errorf("interactive=%v: missing reader outcome guidance %q", interactive, want)
				}
			}
		}
		if req.Instructions != "AI News" || buildTurnContextContent(po, "", "") != nil {
			t.Fatal("hidden reader guidance changed the owner turn")
		}
	}
}
