package render

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestSplitFences(t *testing.T) {
	src := "Hello\n\n```go\npackage main\n```\n\nBye"
	segs := splitFences(src)
	if len(segs) != 3 {
		t.Fatalf("segs=%d want 3: %+v", len(segs), segs)
	}
	if segs[0].code || !strings.Contains(segs[0].body, "Hello") {
		t.Fatalf("seg0: %+v", segs[0])
	}
	if !segs[1].code || segs[1].lang != "go" || !segs[1].complete {
		t.Fatalf("seg1: %+v", segs[1])
	}
	if segs[1].body != "package main" {
		t.Fatalf("body %q", segs[1].body)
	}
}

func TestSplitFences_Streaming(t *testing.T) {
	src := "Intro\n```ts\nconst x = 1"
	segs := splitFences(src)
	if len(segs) != 2 {
		t.Fatalf("segs=%d: %+v", len(segs), segs)
	}
	if !segs[1].code || segs[1].complete {
		t.Fatalf("expected incomplete code: %+v", segs[1])
	}
}

func TestMarkdown_ContainsLineNumbers(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	out := Markdown("```go\npackage main\nfunc main() {}\n```", 80, st)
	if !strings.Contains(out, "│") {
		t.Fatalf("expected gutter: %q", out)
	}
	if !strings.Contains(out, "1") {
		t.Fatalf("expected line number: %q", out)
	}
	if !strings.Contains(out, "go") {
		t.Fatalf("expected language header: %q", out)
	}
}

func TestMarkdown_WrapsProse(t *testing.T) {
	st := DefaultStyles()
	long := strings.Repeat("word ", 50)
	out := Markdown(long, 40, st)
	for _, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Fatalf("line too wide (%d): %q", got, line)
		}
	}
}

func TestMarkdown_DoesNotPadWrappedProseLines(t *testing.T) {
	st := DefaultStyles()
	out := prose("porta-copos", 8, st)
	plain := stripANSI(out)
	lines := strings.Split(plain, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected hyphenated text to wrap into two lines, got %d: %q", len(lines), plain)
	}
	if lines[0] != "porta-" || lines[1] != "copos" {
		t.Fatalf("unexpected wrap: %#v", lines)
	}
	for _, line := range lines {
		if strings.HasSuffix(line, " ") {
			t.Fatalf("wrapped prose line should not be padded with spaces: %q", line)
		}
	}
}

func TestMarkdown_IndentedCodeBlock(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	// Code written as an indented sub-block (no ``` fences), preceded by prose.
	src := "Here is the interface:\n\n    type InitBackend interface {\n        SupportsInit() bool\n    }\n\nThat is all."
	out := Markdown(src, 80, st)
	plain := stripANSI(out)
	// The indented block must be promoted to a CodeBlock: a gutter with line
	// numbers, not flat prose.
	if !strings.Contains(plain, "│") {
		t.Fatalf("expected code gutter for indented block, got:\n%s", plain)
	}
	if !strings.Contains(plain, "1 │") && !strings.Contains(plain, "1 ") {
		t.Fatalf("expected line numbers, got:\n%s", plain)
	}
	// The 4-space margin should be stripped from the rendered code.
	if !strings.Contains(plain, "type InitBackend interface {") {
		t.Fatalf("expected dedented code content, got:\n%s", plain)
	}
	// Surrounding prose must still render.
	if !strings.Contains(plain, "Here is the interface:") || !strings.Contains(plain, "That is all.") {
		t.Fatalf("expected surrounding prose preserved, got:\n%s", plain)
	}
}

func TestMarkdown_IndentedCodeBlockPreservesRelativeIndent(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	src := "    func f() {\n        return 1\n    }"
	out := Markdown(src, 80, st)
	plain := stripANSI(out)
	// Only the 4-space code margin is stripped; the interior 4-space indent
	// (relative to the block) survives.
	if !strings.Contains(plain, "    return 1") {
		t.Fatalf("expected relative indentation preserved, got:\n%s", plain)
	}
}

func TestMarkdown_IndentedBlockDoesNotInterruptParagraph(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	// An indented line immediately following a prose line (no blank line
	// between) is a wrapped continuation, NOT a code block.
	src := "This is a paragraph\n    with an indented continuation."
	out := Markdown(src, 80, st)
	plain := stripANSI(out)
	if strings.Contains(plain, "│") {
		t.Fatalf("indented continuation of a paragraph must not become a code block, got:\n%s", plain)
	}
}

func TestMarkdown_ListItemNotTreatedAsCode(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	// A blank line then a bullet is a list, not indented code, even though the
	// bullet is not indented — guard against over-eager detection.
	src := "Intro\n\n- first\n- second"
	out := Markdown(src, 80, st)
	plain := stripANSI(out)
	if strings.Contains(plain, "│") {
		t.Fatalf("list must not become a code block, got:\n%s", plain)
	}
	if !strings.Contains(plain, "• first") {
		t.Fatalf("expected bullet rendering, got:\n%s", plain)
	}
}

func TestMarkdown_IndentedCodeWithInteriorBlankLine(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	src := "Code:\n\n    line one\n\n    line two\n\nDone."
	out := Markdown(src, 80, st)
	plain := stripANSI(out)
	// Both code lines and the interior blank should be inside one code block.
	if !strings.Contains(plain, "line one") || !strings.Contains(plain, "line two") {
		t.Fatalf("expected both code lines in block, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Done.") {
		t.Fatalf("expected trailing prose, got:\n%s", plain)
	}
}

func TestWrapCodeLine_NoWrapWhenFits(t *testing.T) {
	line := "short line"
	result := wrapCodeLine(line, 80)
	if len(result) != 1 || result[0] != line {
		t.Fatalf("expected no wrap for short line, got %v", result)
	}
}

func TestWrapCodeLine_WrapsLongLine(t *testing.T) {
	// A line longer than width must be split; no part may exceed the limit.
	long := strings.Repeat("x", 120)
	result := wrapCodeLine(long, 40)
	if len(result) < 2 {
		t.Fatalf("expected wrap into multiple parts, got %d: %v", len(result), result)
	}
	for i, part := range result {
		if w := lipgloss.Width(part); w > 40 {
			t.Fatalf("part %d too wide (%d > 40): %q", i, w, part)
		}
	}
}

func TestWrapCodeLine_ANSIColoredLine(t *testing.T) {
	// An ANSI-colored line (e.g. from Chroma) that is wider than width must
	// split without leaving any part wider than width.
	colored := "\x1b[38;2;102;217;239msome_function_name_that_is_very_long_indeed(arg1, arg2, arg3)\x1b[0m"
	result := wrapCodeLine(colored, 30)
	for i, part := range result {
		if w := lipgloss.Width(part); w > 30 {
			t.Fatalf("ANSI part %d too wide (%d > 30): %q", i, w, part)
		}
	}
}

func TestCodeBlock_LongLineDoesNotExceedWidth(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	// A very long code line must not produce output lines wider than the block width.
	longLine := strings.Repeat("x", 200)
	out := CodeBlock(longLine, "text", 60, st, false)
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 60 {
			t.Fatalf("CodeBlock output line too wide (%d > 60): %q", w, line)
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\x1b' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			break
		}
		if s[i+1] != '[' {
			i++
			continue
		}
		i += 2
		for i < len(s) {
			if s[i] >= '@' && s[i] <= '~' {
				break
			}
			i++
		}
	}
	return b.String()
}

// TestProse_HeadingLevelsDistinct verifies that #, ##, and ### render with
// three visually distinct styles (not all the same brand+bold) and that a blank
// spacer line precedes the second and third headings.
// TestProse_HeadingLevelsDistinct verifies headings render in the accent color
// (distinct from plain body text) and that a blank spacer line precedes the
// second and third headings. H1/H2 share the accent+bold section style; H3 is
// accent without bold, so headings are accent-colored and H3 differs from H2.
func TestProse_HeadingLevelsDistinct(t *testing.T) {
	// Mimic the tui theme's RenderStyles heading mapping: accent-colored (208)
	// H1/H2 bold, H3 accent without bold; body text plain (252).
	st := DefaultStyles()
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	st.Heading1 = accent.Bold(true)
	st.Heading2 = accent.Bold(true)
	st.Heading3 = accent
	out := prose("# H1 title\n## H2 section\n### H3 sub\n\nplain body line", 80, st)

	// All heading texts + body must be present (strip per-glyph ANSI first).
	plainOut := stripTestANSI(out)
	for _, want := range []string{"H1 title", "H2 section", "H3 sub", "plain body line"} {
		if !strings.Contains(plainOut, want) {
			t.Fatalf("text %q missing from output:\n%s", want, plainOut)
		}
	}

	lines := strings.Split(out, "\n")
	var h2, h3, body string
	for _, l := range lines {
		switch {
		case strings.Contains(stripTestANSI(l), "H2 section"):
			h2 = l
		case strings.Contains(stripTestANSI(l), "H3 sub"):
			h3 = l
		case strings.Contains(stripTestANSI(l), "plain body line"):
			body = l
		}
	}
	// Headings must carry the code-mode accent (orange 208), body must not.
	if !strings.Contains(h2, "38;5;208") {
		t.Fatalf("expected accent-colored H2 heading, got:\n%q", h2)
	}
	if strings.Contains(body, "38;5;208") {
		t.Fatalf("plain body should not carry the heading accent, got:\n%q", body)
	}
	// H3 (accent, no bold) must differ from H2 (accent + bold).
	if h2 == h3 {
		t.Fatalf("expected H2 and H3 to render with different weights:\nH2=%q\nH3=%q", h2, h3)
	}

	// NO_COLOR path: text preserved and a blank line precedes H2 and H3.
	plainSt := DefaultStyles()
	plainSt.NoColor = true
	plain := prose("# H1 title\n## H2 section\n### H3 sub", 80, plainSt.Effective())
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("NO_COLOR output should carry no ANSI escapes:\n%q", plain)
	}
	if !strings.Contains(plain, "H1 title\n\nH2 section\n\nH3 sub") {
		t.Fatalf("expected blank spacer lines between headings in NO_COLOR output:\n%q", plain)
	}
}

// stripTestANSI removes ANSI escape sequences for stable text assertions in
// this test file.
func stripTestANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) {
				c := s[i]
				i++
				if c >= '@' && c <= '~' {
					break
				}
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestProse_BlankLineBeforeHeading verifies a spacer line is inserted before a
// heading that follows a paragraph, but NOT when the heading is the first line
// of a block (block start).
func TestProse_BlankLineBeforeHeading(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	st = st.Effective() // NO_COLOR for stable text assertions

	// Heading at block start: no leading blank line.
	atStart := prose("# Title\nbody text", 80, st)
	if strings.HasPrefix(atStart, "\n") {
		t.Fatalf("heading at block start should not be preceded by a blank line:\n%q", atStart)
	}

	// Heading after a paragraph: a blank spacer line must be inserted.
	afterPara := prose("some paragraph text\n## Section", 80, st)
	if !strings.Contains(afterPara, "some paragraph text\n\nSection") {
		t.Fatalf("expected a blank spacer line before a heading that follows a paragraph:\n%q", afterPara)
	}
}

func TestProse_HeadingBarCarvesRooms(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	st.HeadingBar = true
	st = st.Effective()

	rule := strings.Repeat("─", 40)
	want := "Problem\n" + rule + "\n\nThe overview is too flat.\n\nRoot causes\n" + rule + "\n\nA packed wall of text."

	// Source with and without a blank after the heading must both produce one
	// trailing spacer — the bar already owns that room, so extra source blanks
	// must not stack a second empty row.
	for _, src := range []string{
		"## Problem\nThe overview is too flat.\n## Root causes\nA packed wall of text.",
		"## Problem\n\nThe overview is too flat.\n\n## Root causes\n\nA packed wall of text.",
	} {
		plain := stripTestANSI(prose(src, 40, st))
		if plain != want {
			t.Fatalf("heading bars should carve rooms (title, full-width rule, blank, body):\nsrc %q\n got %q\nwant %q", src, plain, want)
		}
	}
}

func TestProse_HeadingBarOffLeavesHeadingsAsWords(t *testing.T) {
	st := DefaultStyles()
	st.NoColor = true
	st = st.Effective()
	if st.HeadingBar {
		t.Fatal("chat markdown must leave HeadingBar off")
	}

	out := prose("## Problem\nThe overview is too flat.", 40, st)
	plain := stripTestANSI(out)
	if strings.Contains(plain, "─") {
		t.Fatalf("chat markdown must not draw a heading bar:\n%q", plain)
	}
	if !strings.Contains(plain, "Problem\nThe overview is too flat.") {
		t.Fatalf("expected heading glued to the next paragraph without a bar:\n%q", plain)
	}
}

