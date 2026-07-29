package render

import (
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
	// Dual-gutter editor markers
	if !strings.Contains(out, "old") || !strings.Contains(out, "new") {
		t.Fatalf("expected old/new column headers: %q", out)
	}
	if !strings.Contains(out, "return old()") || !strings.Contains(out, "return new()") {
		t.Fatalf("expected ± body: %q", out)
	}
}

func TestDiffFromToolArgs_WriteFile(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
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
	}, 100, true, st)

	if out == "" {
		t.Fatal("expected diff from verification_context")
	}
	if !strings.Contains(out, "README.md") {
		t.Fatalf("path header: %q", out)
	}
	// Dual gutter with real line numbers
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
	}, nil, 80, true, st)
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") {
		t.Fatalf("args fallback: %q", out)
	}
}

func TestRenderVerificationDiff_WriteCreate(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	vc := "@@ new.txt:1 (created)\n+ 1| hello\n+ 2| world\n"
	out := RenderVerificationDiff(vc, "new.txt", 80, true, st)
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

func TestRenderVerificationDiff_DualGutter(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	vc := "@@ f.go:10\n  9| keep\n- 10| old\n+ 10| new\n  11| after\n"
	out := RenderVerificationDiff(vc, "f.go", 80, true, st)
	// Column header
	if !strings.Contains(out, "old") || !strings.Contains(out, "new") {
		t.Fatalf("gutter labels: %q", out)
	}
	// Line numbers appear in gutters
	if !strings.Contains(out, "10") || !strings.Contains(out, "9") {
		t.Fatalf("line numbers: %q", out)
	}
}
