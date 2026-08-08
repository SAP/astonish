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

// visualLineCount returns the number of display rows text occupies when
// soft-wrapped to width, counting both explicit newlines and word-wrapped
// long lines. It mirrors the bubbles/textarea word-wrap behaviour so the
// composer can grow when a single typed line spills onto a second visual row,
// the same way it grows for an explicit Shift+Enter newline.
//
// When width <= 0 (e.g. the terminal size is not yet known) it falls back to
// counting logical lines so behaviour matches the pre-wrap-aware path.
func visualLineCount(text string, width int) int {
	if text == "" {
		return 0
	}
	if width <= 0 {
		return lineCount(text)
	}
	total := 0
	for _, logical := range strings.Split(text, "\n") {
		total += wrappedRows(logical, width)
	}
	return total
}

// wrappedRows returns how many display rows a single logical line (no newline)
// occupies at width. An empty line still occupies one row. Word boundaries are
// preferred, but a word longer than width is broken across rows so the count
// tracks the textarea's actual rendering.
func wrappedRows(line string, width int) int {
	if width < 1 {
		width = 1
	}
	if line == "" {
		return 1
	}
	rows := 1
	col := 0
	for _, word := range splitKeepSpaces(line) {
		wlen := lipgloss.Width(word)
		if wlen == 0 {
			continue
		}
		if col > 0 && col+wlen > width {
			// Word does not fit on the current row: wrap to a new row.
			rows++
			col = 0
		}
		if wlen > width {
			// Word itself is wider than the row: it spans multiple rows.
			// The first row is the current one; each further full width adds a row.
			extra := (col + wlen - 1) / width
			rows += extra
			col = (col + wlen) % width
			if col == 0 {
				col = width
			}
			continue
		}
		col += wlen
	}
	return rows
}

// splitKeepSpaces splits s into tokens, keeping runs of spaces attached to the
// preceding word so wrap accounting matches how spaces are laid out on a row.
func splitKeepSpaces(s string) []string {
	var tokens []string
	var b strings.Builder
	inSpace := false
	flush := func() {
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		isSpace := r == ' '
		if isSpace != inSpace && b.Len() > 0 {
			flush()
		}
		inSpace = isSpace
		b.WriteRune(r)
	}
	flush()
	return tokens
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
