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
	}, false)
	if s == "" || s == "Tools" {
		t.Fatalf("summary empty: %q", s)
	}
	if !strings.Contains(s, "Edited") {
		t.Fatalf("summary: %q", s)
	}
}
