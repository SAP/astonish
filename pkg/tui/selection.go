package tui

import (
	"strings"

	"github.com/atotto/clipboard"
)

const ansiSelectionStart = "\x1b[7m"

var writeClipboard = clipboard.WriteAll

func (m model) applySelectionToBlock(block string, startLine int) string {
	if m.theme.NoColor || !m.selecting || !m.selectionMoved || block == "" {
		return block
	}
	from, to := normalizeSelection(m.selectionStart, m.selectionEnd)
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		absLine := startLine + i
		if absLine < from.line || absLine > to.line {
			continue
		}
		plain := stripANSI(line)
		startCol, endCol := selectedColsForLine(absLine, from, to, len([]rune(plain)))
		if startCol >= endCol {
			continue
		}
		lines[i] = highlightPlainRange(plain, startCol, endCol)
	}
	return strings.Join(lines, "\n")
}

func selectedColsForLine(line int, from, to selectionPoint, lineLen int) (int, int) {
	start := 0
	end := lineLen
	if line == from.line {
		start = clamp(from.col, 0, lineLen)
	}
	if line == to.line {
		end = clamp(to.col, 0, lineLen)
	}
	if from.line == to.line && start > end {
		start, end = end, start
	}
	return start, end
}

func highlightPlainRange(line string, start, end int) string {
	runes := []rune(line)
	start = clamp(start, 0, len(runes))
	end = clamp(end, 0, len(runes))
	if start >= end {
		return line
	}
	return string(runes[:start]) + ansiSelectionStart + string(runes[start:end]) + ansiReset + string(runes[end:])
}

func selectionText(lines []string, start, end selectionPoint) string {
	if len(lines) == 0 {
		return ""
	}
	from, to := normalizeSelection(start, end)
	from.line = clamp(from.line, 0, len(lines)-1)
	to.line = clamp(to.line, 0, len(lines)-1)
	if from.line > to.line {
		return ""
	}
	var out []string
	for lineNo := from.line; lineNo <= to.line; lineNo++ {
		line := stripANSI(lines[lineNo])
		runes := []rune(line)
		startCol, endCol := selectedColsForLine(lineNo, from, to, len(runes))
		if startCol > len(runes) {
			startCol = len(runes)
		}
		if endCol > len(runes) {
			endCol = len(runes)
		}
		if startCol > endCol {
			startCol, endCol = endCol, startCol
		}
		out = append(out, strings.TrimRight(string(runes[startCol:endCol]), " "))
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

func normalizeSelection(a, b selectionPoint) (selectionPoint, selectionPoint) {
	if a.line > b.line || (a.line == b.line && a.col > b.col) {
		return b, a
	}
	return a, b
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

func clamp(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}
