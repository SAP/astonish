package render

import (
	"fmt"
	"strings"
	"testing"
)

func TestFileDiff_Edit(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	out := FileDiff(DiffOpts{
		Path:  "pkg/foo.go",
		Old:   "return old()",
		New:   "return new()",
		Width: 80,
	}, st)
	if !strings.Contains(out, "foo.go") {
		t.Fatalf("path: %q", out)
	}
	// Single-gutter editor: one line-number column with a │ separator.
	if !strings.Contains(out, "│") {
		t.Fatalf("expected gutter separator: %q", out)
	}
	if !strings.Contains(out, "return old()") || !strings.Contains(out, "return new()") {
		t.Fatalf("expected ± body: %q", out)
	}
}

// TestFileDiff_OnlyChangedLinesWithContext verifies the args-fallback diff shows
// only the changed line as ± while keeping surrounding lines as context (git
// style), instead of replacing the whole block.
func TestFileDiff_OnlyChangedLinesWithContext(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	old := "line1\nline2\nOLD\nline4\nline5"
	new := "line1\nline2\nNEW\nline4\nline5"
	out := FileDiff(DiffOpts{Path: "pkg/foo.go", Old: old, New: new, Width: 100}, st)

	lines := strings.Split(out, "\n")
	var added, removed, context int
	for _, ln := range lines {
		// Skip the header (◆) row.
		if strings.Contains(ln, "◆") {
			continue
		}
		switch {
		case strings.Contains(ln, "− OLD") || strings.Contains(ln, "−OLD"):
			removed++
		case strings.Contains(ln, "+ NEW") || strings.Contains(ln, "+NEW"):
			added++
		case strings.Contains(ln, "line1") || strings.Contains(ln, "line2") ||
			strings.Contains(ln, "line4") || strings.Contains(ln, "line5"):
			context++
		}
	}
	if removed != 1 {
		t.Fatalf("expected exactly 1 removed line (OLD): %q", out)
	}
	if added != 1 {
		t.Fatalf("expected exactly 1 added line (NEW): %q", out)
	}
	if context < 2 {
		t.Fatalf("expected surrounding context lines, got %d: %q", context, out)
	}
	// The unchanged lines must NOT be marked as added/removed.
	if strings.Contains(out, "− line1") || strings.Contains(out, "+ line1") {
		t.Fatalf("unchanged line1 must not be a diff line: %q", out)
	}
}

// TestFileDiff_CollapsesDistantContext verifies long unchanged runs between the
// hunk and the file edges collapse into a "…" gap row.
func TestFileDiff_CollapsesDistantContext(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	var oldB, newB strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&oldB, "line%d\n", i)
		if i == 10 {
			newB.WriteString("CHANGED\n")
		} else {
			fmt.Fprintf(&newB, "line%d\n", i)
		}
	}
	out := FileDiff(DiffOpts{Path: "f.go", Old: oldB.String(), New: newB.String(), Width: 100, Expanded: true}, st)
	if !strings.Contains(out, "…") {
		t.Fatalf("expected a collapsed gap row (…) for distant unchanged lines: %q", out)
	}
	// line1 (far from the change) should be elided, not shown.
	if strings.Contains(out, "line1\n") && !strings.Contains(out, "line10") {
		t.Fatalf("distant lines should be elided: %q", out)
	}
	if !strings.Contains(out, "CHANGED") {
		t.Fatalf("changed line must appear: %q", out)
	}
}

func TestDiffLineStats(t *testing.T) {
	cases := []struct {
		name         string
		old, new     string
		wantA, wantR int
	}{
		{"replace one", "a\nb\nc", "a\nX\nc", 1, 1},
		{"pure add", "a\nb", "a\nb\nc", 1, 0},
		{"pure remove", "a\nb\nc", "a\nc", 0, 1},
		{"no change", "a\nb", "a\nb", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := diffLineStats(tc.old, tc.new)
			if a != tc.wantA || r != tc.wantR {
				t.Fatalf("diffLineStats = +%d −%d, want +%d −%d", a, r, tc.wantA, tc.wantR)
			}
		})
	}
}

func TestDiffFromToolArgs_WriteFile(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	out := DiffFromToolArgs("write_file", map[string]any{
		"file_path": "/tmp/a.md",
		"content":   "# Hi\n\nWorld\n",
	}, 80, false, "", st)
	if out == "" {
		t.Fatal("expected diff")
	}
	if !strings.Contains(out, "a.md") {
		t.Fatalf("%q", out)
	}
	if !strings.Contains(out, "# Hi") {
		t.Fatalf("classic create preview: %q", out)
	}
}

func TestDiffFromToolStep_PrefersVerificationContext(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	vc := "@@ README.md:169\n  168| before\n- 169| Share & Reuse\n- 170| \n  169| ```\n"
	out := DiffFromToolStep("edit_file", map[string]any{
		"path":       "/root/astonish/README.md",
		"old_string": "Share & Reuse\n\n",
		"new_string": "",
	}, map[string]any{
		"success":              true,
		"verification_context": vc,
	}, 100, true, "", st)

	if out == "" {
		t.Fatal("expected diff from verification_context")
	}
	if !strings.Contains(out, "README.md") {
		t.Fatalf("path header: %q", out)
	}
	// Single gutter with real line numbers
	if !strings.Contains(out, "169") {
		t.Fatalf("expected real line number 169: %q", out)
	}
	if !strings.Contains(out, "Share & Reuse") {
		t.Fatalf("expected removed body: %q", out)
	}
	if !strings.Contains(out, "before") {
		t.Fatalf("expected context line: %q", out)
	}
}

func TestDiffFromToolStep_FallbackToArgs(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	out := DiffFromToolStep("edit_file", map[string]any{
		"path":       "x.go",
		"old_string": "a",
		"new_string": "b",
	}, nil, 80, true, "", st)
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") {
		t.Fatalf("args fallback: %q", out)
	}
}

func TestRenderVerificationDiff_WriteCreate(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	vc := "@@ new.txt:1 (created)\n+ 1| hello\n+ 2| world\n"
	out := RenderVerificationDiff(vc, "new.txt", 80, true, "", st)
	if !strings.Contains(out, "created") {
		t.Fatalf("want created note: %q", out)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Fatalf("want + body: %q", out)
	}
}

func TestStatsFromSteps_UsesVerification(t *testing.T) {
	steps := []ToolStep{{
		Name: "edit_file",
		Args: map[string]any{
			"old_string": "a\nb\nc\nd\ne",
			"new_string": "x",
		},
		Result: map[string]any{
			"verification_context": "@@ f:1\n- 1| a\n+ 1| x\n",
		},
		Status: "complete",
	}}
	st := StatsFromSteps(steps)
	if st.Kind != "diff" || st.Added != 1 || st.Removed != 1 {
		t.Fatalf("stats = %+v, want +1 −1 from verification", st)
	}
}

func TestRenderVerificationDiff_SingleGutter(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	vc := "@@ f.go:10\n  9| keep\n- 10| old\n+ 10| new\n  11| after\n"
	out := RenderVerificationDiff(vc, "f.go", 80, true, "", st)
	// Single line-number column with a │ separator (no old/new label row).
	if !strings.Contains(out, "│") {
		t.Fatalf("gutter separator: %q", out)
	}
	// Line numbers appear in the single gutter.
	if !strings.Contains(out, "10") || !strings.Contains(out, "9") {
		t.Fatalf("line numbers: %q", out)
	}
	// No row carries two space-separated line numbers anymore.
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "10 10") {
			t.Fatalf("expected single number column, got dual: %q", ln)
		}
	}
}

func TestFileDiff_RelativePathHeader(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	out := FileDiff(DiffOpts{
		Path:  "/home/user/project/pkg/tui/render/diff.go",
		Old:   "old",
		New:   "new",
		Width: 100,
		Root:  "/home/user/project",
	}, st)
	if !strings.Contains(out, "pkg/tui/render/diff.go") {
		t.Fatalf("expected project-relative path in header: %q", out)
	}
}

func TestRenderVerificationDiff_UsesFullPathOverHeaderBasename(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	// verification_context header carries only the basename (README.md); the
	// full fallbackPath + root should win so the project-relative path shows.
	vc := "@@ README.md:1\n- 1| old\n+ 1| new\n"
	out := RenderVerificationDiff(vc, "/home/user/project/docs/README.md", 100, true, "/home/user/project", st)
	if !strings.Contains(out, "docs/README.md") {
		t.Fatalf("expected project-relative path: %q", out)
	}
}

func TestDisplayPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		root string
		want string
	}{
		{"empty", "", "", "file"},
		{"no root keeps path", "pkg/foo.go", "", "pkg/foo.go"},
		{"abs under root", "/home/u/proj/pkg/a.go", "/home/u/proj", "pkg/a.go"},
		{"rel under root", "pkg/a.go", "/home/u/proj", "pkg/a.go"},
		{"outside root falls back", "/etc/passwd", "/home/u/proj", "/etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayPath(tc.path, tc.root); got != tc.want {
				t.Fatalf("displayPath(%q, %q) = %q, want %q", tc.path, tc.root, got, tc.want)
			}
		})
	}
}
