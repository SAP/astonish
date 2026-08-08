package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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

func TestVisualLineCount(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  int
	}{
		{"empty", "", 40, 0},
		{"short single line", "hello", 40, 1},
		{"explicit newlines", "a\nb\nc", 40, 3},
		{"wrapped long line", strings.Repeat("word ", 20), 40, 3},
		{"very long single word", strings.Repeat("x", 85), 40, 3},
		{"mixed newline and wrap", "short\n" + strings.Repeat("word ", 20), 40, 4},
		{"trailing newline", "a\n", 40, 2},
		{"zero width falls back to logical", "a\nb\nc", 0, 3},
		{"negative width falls back to logical", strings.Repeat("word ", 20), -5, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := visualLineCount(tc.text, tc.width); got != tc.want {
				t.Fatalf("visualLineCount(%q, %d) = %d, want %d", tc.text, tc.width, got, tc.want)
			}
		})
	}
}

func TestWrappedRows(t *testing.T) {
	if got := wrappedRows("", 40); got != 1 {
		t.Fatalf("empty line rows = %d, want 1", got)
	}
	if got := wrappedRows("hello", 40); got != 1 {
		t.Fatalf("short line rows = %d, want 1", got)
	}
	// A word exactly at the width fits on one row.
	if got := wrappedRows(strings.Repeat("x", 40), 40); got != 1 {
		t.Fatalf("word == width rows = %d, want 1", got)
	}
	// One cell over the width spills to a second row.
	if got := wrappedRows(strings.Repeat("x", 41), 40); got != 2 {
		t.Fatalf("word > width rows = %d, want 2", got)
	}
}

func TestTruncateToWidth(t *testing.T) {
	if got := truncateToWidth("hello", 10); got != "hello" {
		t.Fatalf("truncateToWidth without truncation = %q", got)
	}
	got := truncateToWidth("hello world", 6)
	if lipgloss.Width(got) > 6 {
		t.Fatalf("truncateToWidth width=%d want <=6: %q", lipgloss.Width(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncateToWidth should add ellipsis: %q", got)
	}
}
