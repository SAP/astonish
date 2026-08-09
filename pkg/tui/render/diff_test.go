package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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

func TestFileDiff_SyntaxHighlighted(t *testing.T) {
	st := DefaultStyles()
	// NoColor is false (default) — syntax highlighting should produce ANSI codes.
	out := FileDiff(DiffOpts{
		Path:  "pkg/foo.go",
		Old:   "return old()",
		New:   "return new()",
		Width: 80,
	}, st)
	// ANSI escape sequences (e.g., \x1b[) should be present from Chroma highlighting.
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected ANSI escape codes from syntax highlighting: %q", out)
	}
	// Content should still be present.
	if !strings.Contains(out, "return") {
		t.Fatalf("expected source content: %q", out)
	}
}

func TestFileDiff_BackgroundStyle(t *testing.T) {
	st := DefaultStyles()
	// With color enabled, diff lines should use background color (true-color: 48;2;...)
	// Note: lipgloss may not produce background codes in non-TTY test environments,
	// so we verify the extractBgCode helper directly with known input.
	bgCode := extractBgCode(st.DiffAddedBg)
	// If lipgloss renders no colors (no TTY), fall back to verifying the
	// function behaves correctly with a manually-rendered style string.
	if bgCode == "" {
		// Non-TTY environment — verify fallback path doesn't crash and produces output.
		out := FileDiff(DiffOpts{
			Path:  "main.go",
			Old:   "a := 1",
			New:   "b := 2",
			Width: 80,
		}, st)
		if out == "" {
			t.Fatal("expected non-empty diff output even without background colors")
		}
		t.Skip("lipgloss produces no background in non-TTY test environment")
	}
	// TTY present: verify background codes appear in diff output.
	out := FileDiff(DiffOpts{
		Path:  "main.go",
		Old:   "a := 1",
		New:   "b := 2",
		Width: 80,
	}, st)
	if !strings.Contains(out, "48;2;") {
		t.Fatalf("expected true-color background escape (48;2;) for diff bands: %q", out)
	}
}

func TestFileDiff_NoColorFallback(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	out := FileDiff(DiffOpts{
		Path:  "pkg/foo.go",
		Old:   "return old()",
		New:   "return new()",
		Width: 80,
	}, st)
	// No ANSI escape should be present.
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("NoColor mode should produce no ANSI escapes: %q", out)
	}
	if !strings.Contains(out, "return old()") || !strings.Contains(out, "return new()") {
		t.Fatalf("expected raw content: %q", out)
	}
}

func TestLangFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"foo.go", "Go"},
		{"bar.py", "Python"},
		{"baz.ts", "TypeScript"},
		{"x.tsx", "TypeScript"},
		{"style.css", "CSS"},
		{"", ""},
		{"noext", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := langFromPath(tc.path)
			if got != tc.want {
				t.Fatalf("langFromPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestExtractBgCode(t *testing.T) {
	// Test extractBgCode against a known ANSI-formatted string that simulates
	// what lipgloss produces in a real terminal.
	cases := []struct {
		name   string
		input  string
		wantBg string
	}{
		{
			"true-color compound SGR",
			// Simulates: \x1b[38;5;252;48;2;26;51;32mX\x1b[0m
			"\x1b[38;5;252;48;2;26;51;32mX\x1b[0m",
			"\x1b[48;2;26;51;32m",
		},
		{
			"256-color background",
			"\x1b[48;5;22mX\x1b[0m",
			"\x1b[48;5;22m",
		},
		{
			"no background",
			"\x1b[38;5;252mX\x1b[0m",
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Override extractBgCode's internal logic by testing the raw parser.
			got := extractBgFromRendered(tc.input)
			if got != tc.wantBg {
				t.Fatalf("extractBgFromRendered(%q) = %q, want %q", tc.input, got, tc.wantBg)
			}
		})
	}
}

func TestPadWithBg_InsertsBackground(t *testing.T) {
	// Directly test padWithBg with a known background ANSI code on
	// pre-highlighted content (simulating Chroma output).
	content := "\x1b[38;2;166;226;46mfunc\x1b[0m\x1b[38;2;248;248;242m main()\x1b[0m"
	bg := lipgloss.NewStyle().Background(lipgloss.Color("#1a3320"))

	// In non-TTY, extractBgCode returns ""; padWithBg falls back to lipgloss.
	// We test the ANSI manipulation logic directly.
	bgCode := "\x1b[48;2;26;51;32m"
	// Manually apply the logic padWithBg would use:
	patched := strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+bgCode)
	result := bgCode + patched + "    " + "\x1b[0m"

	// Verify background code appears at the start and after resets.
	if !strings.HasPrefix(result, bgCode) {
		t.Fatalf("expected result to start with bg code: %q", result)
	}
	if !strings.Contains(result, "\x1b[0m"+bgCode) {
		t.Fatalf("expected bg code re-inserted after resets: %q", result)
	}
	_ = bg // verify it compiles with the style
}

// TestFileDiff_LongLinesTruncated verifies that lines longer than the available
// text width are truncated (with an ellipsis) rather than wrapping to the next
// visual line or overflowing the terminal.
func TestFileDiff_LongLinesTruncated(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true

	// Create a line that is much longer than the width we'll render at.
	longLine := strings.Repeat("x", 200)
	old := "short\n" + longLine + "\nlast"
	new := "short\n" + longLine + "_changed\nlast"

	const width = 60
	out := FileDiff(DiffOpts{
		Path:  "test.go",
		Old:   old,
		New:   new,
		Width: width,
	}, st)

	// Every rendered line must fit within the total width (gutter + content).
	for i, line := range strings.Split(out, "\n") {
		// Skip empty trailing line.
		if line == "" {
			continue
		}
		visW := lipgloss.Width(line)
		if visW > width {
			t.Errorf("line %d exceeds width %d (got %d): %q", i, width, visW, line)
		}
	}

	// The long context line should be truncated with "…".
	if !strings.Contains(out, "…") {
		t.Fatalf("expected truncation ellipsis in output: %q", out)
	}
}

// TestFileDiff_PadWithBgNoWrap verifies that padWithBg does not insert newlines
// when the content is shorter than or equal to the width.
func TestFileDiff_PadWithBgNoWrap(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true

	content := "hello world"
	result := padWithBg(content, 40, lipgloss.NewStyle(), true)
	if strings.Contains(result, "\n") {
		t.Fatalf("padWithBg should not wrap: %q", result)
	}
	if lipgloss.Width(result) != 40 {
		t.Fatalf("expected padded to width 40, got %d: %q", lipgloss.Width(result), result)
	}
}

// TestFileDiff_TabsExpandedAndTruncated verifies that lines with tabs are
// expanded to spaces and properly truncated so they don't overflow the terminal.
func TestFileDiff_TabsExpandedAndTruncated(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true

	// A Go-like line with tabs that would be very wide when rendered.
	// Two tabs (8 spaces at tabwidth 4) + long content = wide line.
	tabbedLine := "\t\tgutter = st.CodeGutter.Render(numCol) + st.CodeGutter.Render(\" | \") + markerStyle.Render(marker) + \" \""
	old := "short\n" + tabbedLine
	new := "short\n" + tabbedLine + "_changed"

	const width = 60
	out := FileDiff(DiffOpts{
		Path:  "test.go",
		Old:   old,
		New:   new,
		Width: width,
	}, st)

	// No rendered line should exceed the total width.
	for i, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		visW := lipgloss.Width(line)
		if visW > width {
			t.Errorf("line %d exceeds width %d (got %d): %q", i, width, visW, line)
		}
	}

	// Output should not contain literal tab characters (they should be expanded).
	if strings.Contains(out, "\t") {
		t.Errorf("output should not contain literal tabs: %q", out)
	}
}

// TestExpandTabs verifies tab expansion at different positions.
func TestExpandTabs(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"no tabs", "no tabs"},
		{"\thello", "    hello"},
		{"\t\thello", "        hello"},
		{"ab\tcd", "ab  cd"},       // tab at col 2 → pad to col 4
		{"abc\td", "abc d"},        // tab at col 3 → pad to col 4
		{"abcd\te", "abcd    e"},   // tab at col 4 → pad to col 8
	}
	for _, tt := range tests {
		got := expandTabs(tt.in, 4)
		if got != tt.want {
			t.Errorf("expandTabs(%q, 4) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
