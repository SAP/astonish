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
