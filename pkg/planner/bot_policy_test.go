package planner

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"testing"
)

func TestBotReActNoGuardCannotAutoApprove(t *testing.T) {
	calls := 0
	candidate, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "fixture"}, func(tool.Context, map[string]any) (map[string]any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewReActPlanner(nil, []tool.Tool{candidate})
	p.ApprovalCallback = func(string, map[string]any) (bool, error) { return true, nil }
	_, err = p.executeTool(execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{}), "read_file", `{}`)
	if err == nil || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
