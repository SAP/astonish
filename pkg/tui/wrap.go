package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Layout margins (cells) for transcript content relative to the terminal edge.
const (
	contentMarginX      = 2
	userBubbleMaxLines  = 4 // collapsed height (including "… expand" line when truncated)
	doubleClickWindowMS = 400
)

// contentWidth returns the max text width for transcript bodies.
func contentWidth(termWidth int) int {
	w := termWidth - 2*contentMarginX
	if w < 20 {
		if termWidth > 4 {
			return termWidth - 2
		}
		return 20
	}
	return w
}

// wrapPlain wraps text to width using lipgloss (word-aware where possible).
// Does not apply colors — caller styles the result.
func wrapPlain(text string, width int) string {
	if width < 1 {
		width = 1
	}
	text = strings.ReplaceAll(text, "\t", "    ")
	return lipgloss.NewStyle().Width(width).Render(text)
}

// padBlock indents every line of block by contentMarginX spaces.
func padBlock(block string) string {
	if block == "" {
		return ""
	}
	margin := strings.Repeat(" ", contentMarginX)
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		// Don't pad completely empty trailing lines twice awkwardly.
		if line == "" && i == len(lines)-1 {
			continue
		}
		lines[i] = margin + line
	}
	return strings.Join(lines, "\n")
}

// lineCount returns the number of visual lines in s.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// truncateVisualLines keeps the first maxLines of s. If truncated and
// moreSuffix is non-empty, the last kept line is replaced by moreSuffix
// (so total lines stay <= maxLines).
func truncateVisualLines(s string, maxLines int, moreSuffix string) (out string, truncated bool) {
	if maxLines < 1 {
		return "", s != ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s, false
	}
	if moreSuffix != "" && maxLines >= 1 {
		kept := append([]string{}, lines[:maxLines-1]...)
		kept = append(kept, moreSuffix)
		return strings.Join(kept, "\n"), true
	}
	return strings.Join(lines[:maxLines], "\n"), true
}

// truncateToWidth shortens s to the requested terminal cell width, using an
// ellipsis when truncation is required. It is intended for plain display text.
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}

	var b strings.Builder
	for _, r := range s {
		next := b.String() + string(r) + "…"
		if lipgloss.Width(next) > width {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "…"
}
