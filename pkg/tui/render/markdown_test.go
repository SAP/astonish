package render

import (
	"strings"
	"testing"
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
		// strip might still be long with styles; NoColor path
		if lipglossWidth(line) > 45 {
			t.Fatalf("line too wide (%d): %q", lipglossWidth(line), line)
		}
	}
}

func lipglossWidth(s string) int {
	// simple: count non-ansi roughly using visible - use lipgloss in real test via import
	return len([]rune(s))
}
