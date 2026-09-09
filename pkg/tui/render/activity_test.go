package render

import (
	"strings"
	"testing"
)

func TestStatsFromSteps_Diff(t *testing.T) {
	// old "a\nb" → new "a\nb\nc": only line "c" is added (a/b unchanged), so a
	// line-level diff yields +1 −0 — not "every old line removed, every new added".
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
	if st.Kind != "diff" || st.Added != 1 || st.Removed != 0 {
		t.Fatalf("stats: %+v want +1 −0 (line-level diff)", st)
	}
}

func TestStatsFromSteps_DiffReplace(t *testing.T) {
	// Replacing one line inside an unchanged block: +1 −1.
	st := StatsFromSteps([]ToolStep{
		{
			Name: "edit_file",
			Args: map[string]any{
				"old_string": "a\nb\nc\n",
				"new_string": "a\nX\nc\n",
			},
			Status: "complete",
		},
	})
	if st.Kind != "diff" || st.Added != 1 || st.Removed != 1 {
		t.Fatalf("stats: %+v want +1 −1 (only the changed line)", st)
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
	if s != "Running command" {
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
	if strings.Contains(body, "…") {
		t.Fatalf("command body must not hard-truncate with ellipsis: %q", body)
	}
	for _, part := range []string{"kubectl get clusters", "all-namespaces", "output wide", "very-long-openstack-", "context-name"} {
		if !strings.Contains(body, part) {
			t.Fatalf("expected full command part %q in body %q", part, body)
		}
	}
}

func TestToolDetailBodyShowsFullCommand(t *testing.T) {
	cmd := strings.Repeat("alpha-beta-gamma-delta ", 12) // well over 48 chars and 8 wrapped lines at width 32
	body := ToolDetailBody(ToolStep{Name: "shell_command", Args: map[string]any{"command": cmd}, Status: "running"}, 32)
	if strings.Contains(body, "…") {
		t.Fatalf("command body must not hard-truncate with ellipsis: %q", body)
	}
	joined := strings.ReplaceAll(body, "\n", "")
	joined = strings.ReplaceAll(joined, "command: ", "")
	joined = strings.ReplaceAll(joined, "          ", "")
	want := strings.TrimSpace(cmd)
	got := strings.Join(strings.Fields(joined), " ")
	if got != strings.Join(strings.Fields(want), " ") {
		t.Fatalf("expected full command %q, got %q from body %q", want, got, body)
	}
	for _, line := range strings.Split(body, "\n") {
		if len([]rune(line)) > 32 {
			t.Fatalf("line too wide (%d): %q", len([]rune(line)), line)
		}
	}
}
