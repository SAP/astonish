package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// A prepared recovery must reject a catalog without its private plan engine
// before calling the saved leaf, including an already-published mirror.
func TestBotPreparedReviewMissingEngineNeverDispatches(t *testing.T) {
	for _, stage := range []store.BotPreparedPlanStage{store.BotPlanAwaitingApproval, store.BotPlanPublished} {
		t.Run(string(stage), func(t *testing.T) {
			calls := 0
			leaf, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "controlled mirror"}, func(context tool.Context, args map[string]any) (map[string]any, error) {
				calls++
				return map[string]any{"ok": true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			index := agent.NewLexicalToolIndex()
			if err := index.PrimeTools(context.Background(), []tool.Tool{leaf}, nil); err != nil {
				t.Fatal(err)
			}
			record := &store.BotPreparedPlan{Stage: stage, Action: execution.ActionDescriptor{Tool: "write_file", Arguments: json.RawMessage(`{"path":"/workspace/PLAN.md","content":"saved"}`)}}
			bridge := &BotPreparedPlanSession{Resume: record}
			ctx := context.WithValue(context.Background(), botPreparedPlanSessionKey{}, bridge)
			_, err = executePreparedPlan(ctx, bot.RunRequest{}, &BotChatResources{Agent: &agent.ChatAgent{ToolIndex: index}}, nil, nil, record)
			if err == nil {
				t.Fatal("missing prepared engine was accepted")
			}
			if calls != 0 {
				t.Fatalf("missing engine dispatched saved leaf %d time(s) before rejection", calls)
			}
		})
	}
}
