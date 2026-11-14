package launcher

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

func TestBotTaskClassifierConcreteOwnership(t *testing.T) {
	for _, name := range []string{"bot_task_list", "bot_task_read", "bot_task_select", "bot_task_update", "bot_task_execute"} {
		t.Run(name, func(t *testing.T) {
			generic, err := functiontool.New(functiontool.Config{Name: name, Description: "fixture"}, func(tool.Context, botTaskArgs) (map[string]any, error) { return nil, nil })
			if err != nil {
				t.Fatal(err)
			}
			owned := &botTaskTool{botRunnableTool: generic.(botRunnableTool)}
			classifier := WithBotTaskClassifier(BotCatalogueClassifier{}, []tool.Tool{owned})
			action, err := classifier.DescribeAction(context.Background(), name, json.RawMessage(`{}`))
			if err != nil || !action.ConstraintsVerified {
				t.Fatalf("installed classifier: %+v %v", action, err)
			}
			expected := execution.EffectReversibleWrite
			if name == "bot_task_list" || name == "bot_task_read" {
				expected = execution.EffectRead
			}
			if action.Effect != expected {
				t.Fatalf("effect=%s want=%s", action.Effect, expected)
			}
			spoof := WithBotTaskClassifier(BotCatalogueClassifier{}, []tool.Tool{generic})
			action, err = spoof.DescribeAction(context.Background(), name, json.RawMessage(`{}`))
			if err == nil && action.ConstraintsVerified {
				t.Fatal("matching name acquired concrete Task authority")
			}
			if substantiveTaskTool(owned) {
				t.Fatal("Task metadata operation treated as substantive first-party work")
			}
		})
	}
}
