package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/SAP/astonish/pkg/tui/render"
)

func TestDelegationToolRowsMatchMainThread(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 120, Height: 30})
	steps := []render.ToolStep{
		{Name: "read_file", Args: map[string]any{"path": "pkg/example.go"}, Status: "complete"},
		{Name: "shell_command", Args: map[string]any{"command": "go test ./pkg/...\n&& git diff --check"}, Status: "complete"},
		{Name: "grep_search", Args: map[string]any{"pattern": "Example"}, Status: "running"},
		{Name: "process_read", Status: "complete"},
	}
	var activity []events.DelegationActivity
	for i, s := range steps {
		id := strings.Repeat("x", i+1)
		activity = append(activity, events.DelegationActivity{Type: "tool_call", CallID: id, ToolName: s.Name, Args: s.Args})
		if s.Status == "complete" {
			activity = append(activity, events.DelegationActivity{Type: "tool_result", CallID: id, ToolName: s.Name, Result: map[string]any{"success": true}})
		}
	}
	for _, width := range []int{28, 120} {
		got := strings.TrimSuffix(stripANSI(m.renderDelegationActivity(events.DelegationTaskState{Activity: activity}, width)), "\n")
		lines := strings.Split(got, "\n")
		if len(lines) != len(steps) {
			t.Fatalf("width %d: wanted %d consecutive rows, got %q", width, len(steps), got)
		}
		for i, s := range steps {
			want := collapsedToolLine(render.ToolDetailLine(s), width)
			if lines[i] != want {
				t.Errorf("row %d: %q != main %q", i, lines[i], want)
			}
		}
	}
}
