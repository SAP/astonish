package tui

import (
	"strings"
	"testing"
)

func TestContentWidth(t *testing.T) {
	if got := contentWidth(100); got != 96 {
		t.Fatalf("contentWidth(100)=%d want 96", got)
	}
	if got := contentWidth(10); got != 8 {
		t.Fatalf("contentWidth(10)=%d want 8", got)
	}
}

func TestWrapPlain_RespectsWidth(t *testing.T) {
	long := strings.Repeat("word ", 40)
	out := wrapPlain(long, 40)
	for i, line := range strings.Split(out, "\n") {
		// lipgloss may leave trailing spaces; count runes conservatively.
		if len([]rune(strings.TrimRight(line, " "))) > 40 {
			t.Fatalf("line %d too long (%d): %q", i, len([]rune(line)), line)
		}
	}
}

func TestTruncateVisualLines(t *testing.T) {
	in := "a\nb\nc\nd\ne"
	out, trunc := truncateVisualLines(in, 3, "… more")
	if !trunc {
		t.Fatal("expected truncated")
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("lines=%d want 3: %q", len(lines), out)
	}
	if lines[2] != "… more" {
		t.Fatalf("last line %q", lines[2])
	}
}

func TestPadBlock(t *testing.T) {
	out := padBlock("hi\nthere")
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "  ") || !strings.HasPrefix(lines[1], "  ") {
		t.Fatalf("expected margin: %q", out)
	}
}
