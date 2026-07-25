package render

import (
	"strings"
	"testing"
)

func TestFileDiff_Edit(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	out := FileDiff(DiffOpts{
		Path: "pkg/foo.go",
		Old:  "return old()",
		New:  "return new()",
		Width: 80,
	}, st)
	if !strings.Contains(out, "foo.go") {
		t.Fatalf("path: %q", out)
	}
	if !strings.Contains(out, "+") && !strings.Contains(out, "−") && !strings.Contains(out, "-") {
		t.Fatalf("expected diff markers: %q", out)
	}
}

func TestDiffFromToolArgs_WriteFile(t *testing.T) {
	st := DefaultStyles()
	out := DiffFromToolArgs("write_file", map[string]any{
		"file_path": "/tmp/a.md",
		"content":   "# Hi\n\nWorld\n",
	}, 80, false, st)
	if out == "" {
		t.Fatal("expected diff")
	}
	if !strings.Contains(out, "a.md") {
		t.Fatalf("%q", out)
	}
}
