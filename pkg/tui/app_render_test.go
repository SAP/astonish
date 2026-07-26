package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderUserBubbleUsesFullWidthRectangleBorder(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 80}
	out := m.renderUserBubble("hello", false, 40)
	plain := lipgloss.NewStyle().Render(out)
	plain = stripANSI(plain)
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), plain)
	}
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") {
		t.Fatalf("top border is not a rectangle: %q", lines[0])
	}
	if !strings.HasPrefix(lines[2], "└") || !strings.HasSuffix(lines[2], "┘") {
		t.Fatalf("bottom border is not a rectangle: %q", lines[2])
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 40 {
			t.Fatalf("line %d width=%d want 40: %q", i, got, line)
		}
	}
}

func TestRenderUserBubbleEmbedsExpandHintInBottomBorder(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 100}
	content := strings.Repeat("word ", 120)
	out := m.renderUserBubble(content, false, 60)
	plain := stripANSI(out)
	lines := strings.Split(plain, "\n")
	bottom := lines[len(lines)-1]
	if !strings.Contains(bottom, "double-click to expand") {
		t.Fatalf("bottom border should contain expand hint: %q", bottom)
	}
	if !strings.HasPrefix(bottom, "└") || !strings.HasSuffix(bottom, "┘") {
		t.Fatalf("bottom border should keep rectangle corners: %q", bottom)
	}
	if !strings.Contains(bottom, "─ … double-click to expand ─") {
		t.Fatalf("hint should interrupt and resume border line: %q", bottom)
	}
	if got := lipgloss.Width(bottom); got != 60 {
		t.Fatalf("bottom width=%d want 60: %q", got, bottom)
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
