package agent

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
)

func TestHandleToolNode_GlobalAutoApproveBypassesApproval(t *testing.T) {
	state := NewMockState()
	toolExecuted := false
	mockTool := &MockTool{
		NameFunc: func() string { return "shell_command" },
		RunFunc: func(ctx tool.Context, args any) (map[string]any, error) {
			toolExecuted = true
			return map[string]any{"stdout": "ok"}, nil
		},
	}
	a := &AstonishAgent{
		AutoApprove: true,
		Tools:       []tool.Tool{mockTool},
	}
	node := &config.Node{
		Name:            "run_curl",
		Type:            "tool",
		ToolsSelection:  []string{"shell_command"},
		ToolsAutoApproval: false,
		Args:            map[string]interface{}{"command": "echo hi"},
	}

	paused := false
	ok := a.handleToolNode(context.Background(), node, state, func(ev *session.Event, err error) bool {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev != nil && ev.Actions.StateDelta != nil {
			if awaiting, _ := ev.Actions.StateDelta["awaiting_approval"].(bool); awaiting {
				paused = true
			}
		}
		return true
	})
	if !ok {
		t.Fatal("expected handleToolNode to continue (not pause)")
	}
	if paused {
		t.Fatal("expected no approval pause when AutoApprove is true")
	}
	if !toolExecuted {
		t.Fatal("expected tool to run when AutoApprove is true")
	}
	if awaiting, _ := state.Get("awaiting_approval"); awaiting == true {
		t.Fatal("expected awaiting_approval unset when AutoApprove is true")
	}
}

func TestHandleToolNode_NoAutoApproveRequestsApproval(t *testing.T) {
	state := NewMockState()
	toolExecuted := false
	mockTool := &MockTool{
		NameFunc: func() string { return "shell_command" },
		RunFunc: func(ctx tool.Context, args any) (map[string]any, error) {
			toolExecuted = true
			return map[string]any{"stdout": "ok"}, nil
		},
	}
	a := &AstonishAgent{
		AutoApprove: false,
		Tools:       []tool.Tool{mockTool},
	}
	node := &config.Node{
		Name:              "run_curl",
		Type:              "tool",
		ToolsSelection:    []string{"shell_command"},
		ToolsAutoApproval: false,
		Args:              map[string]interface{}{"command": "echo hi"},
	}

	pausedForApproval := false
	ok := a.handleToolNode(context.Background(), node, state, func(ev *session.Event, err error) bool {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev != nil && ev.Actions.StateDelta != nil {
			if awaiting, _ := ev.Actions.StateDelta["awaiting_approval"].(bool); awaiting {
				pausedForApproval = true
			}
		}
		return true
	})
	if ok {
		t.Fatal("expected handleToolNode to pause (return false) when approval required")
	}
	if !pausedForApproval {
		t.Fatal("expected awaiting_approval event when AutoApprove is false")
	}
	if toolExecuted {
		t.Fatal("expected tool NOT to run before approval")
	}
}
