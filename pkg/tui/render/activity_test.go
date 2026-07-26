package render

import (
	"strings"
	"testing"
)

func TestStatsFromSteps_Diff(t *testing.T) {
	st := StatsFromSteps([]ToolStep{
		{
			Name: "edit_file",
			Args: map[string]any{
				"old_string": "a\nb\n",
				"new_string": "a\nb\nc\n",
			},
			Status: "complete",
		},
	})
	if st.Kind != "diff" || st.Added < 1 || st.Removed < 1 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestActivitySummary_Categories(t *testing.T) {
	s := ActivitySummary([]ToolStep{
		{Name: "edit_file", Args: map[string]any{"path": "a.go"}, Status: "complete"},
		{Name: "edit_file", Args: map[string]any{"path": "b.go"}, Status: "complete"},
		{Name: "read_file", Args: map[string]any{"path": "c.go"}, Status: "complete"},
		{Name: "run_terminal_command", Args: map[string]any{"command": "go test ./..."}, Status: "complete"},
	}, false)
	if s == "" || s == "Tools" {
		t.Fatalf("summary empty: %q", s)
	}
	for _, want := range []string{"Edited 2 files", "explored 1 file", "ran 1 command"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary %q missing %q", s, want)
		}
	}
}

func TestActivitySummary_LiveCommandHint(t *testing.T) {
	s := ActivitySummary([]ToolStep{
		{Name: "run_terminal_command", Args: map[string]any{"command": "go test ./pkg/tui/..."}, Status: "running"},
	}, true)
	if !strings.Contains(s, "Running `go test ./pkg/tui/...`") {
		t.Fatalf("live hint: %q", s)
	}
}

func TestToolDetailLineAndPreview(t *testing.T) {
	step := ToolStep{
		Name:   "run_terminal_command",
		Args:   map[string]any{"command": "go test ./pkg/tui/..."},
		Result: map[string]any{"stdout": "ok pkg/tui\nok pkg/tui/render\nok pkg/tui/events\nok pkg/tui/backend\nok pkg/tui/launcher\nok pkg/tui/cmd\nok pkg/tui/more\nok pkg/tui/extra"},
		Status: "complete",
	}
	line := ToolDetailLine(step)
	if !strings.Contains(line, "✓") || !strings.Contains(line, "Run command") {
		t.Fatalf("detail line: %q", line)
	}
	body := ToolDetailBody(step, 80)
	if !strings.Contains(body, "command:") || !strings.Contains(body, "go test") {
		t.Fatalf("detail body: %q", body)
	}
	preview := ToolResultPreview(step, 80)
	if !strings.Contains(preview, "ok pkg/tui") || !strings.Contains(preview, "…") {
		t.Fatalf("preview: %q", preview)
	}
}

func TestToolDetailLineError(t *testing.T) {
	step := ToolStep{
		Name:   "grep",
		Args:   map[string]any{"pattern": "TODO"},
		Result: map[string]any{"error": "no matches"},
		Status: "error",
	}
	line := ToolDetailLine(step)
	if !strings.Contains(line, "✖") || !strings.Contains(line, "Search") {
		t.Fatalf("detail line: %q", line)
	}
	preview := ToolResultPreview(step, 40)
	if preview != "no matches" {
		t.Fatalf("preview: %q", preview)
	}
}

func TestToolResultPreviewWrapsLongContent(t *testing.T) {
	long := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron"
	preview := ToolResultPreview(ToolStep{Name: "grep", Result: long, Status: "complete"}, 24)
	lines := strings.Split(preview, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapped preview, got %q", preview)
	}
	for _, line := range lines {
		if len([]rune(line)) > 24 {
			t.Fatalf("line too wide (%d): %q", len([]rune(line)), line)
		}
	}
}

func TestToolDetailBodyWrapsLongCommand(t *testing.T) {
	cmd := "kubectl get clusters --all-namespaces --output wide --context very-long-openstack-context-name"
	body := ToolDetailBody(ToolStep{Name: "run_terminal_command", Args: map[string]any{"command": cmd}, Status: "complete"}, 32)
	lines := strings.Split(body, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapped body, got %q", body)
	}
	for _, line := range lines {
		if len([]rune(line)) > 32 {
			t.Fatalf("line too wide (%d): %q", len([]rune(line)), line)
		}
	}
}
