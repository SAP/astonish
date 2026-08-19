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
