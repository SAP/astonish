package render

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
)

// CodeBlock renders a fenced code body with optional language, line numbers,
// and syntax highlighting. If streaming is true, a small indicator is shown.
func CodeBlock(body, lang string, width int, st Styles, streaming bool) string {
	if width < 20 {
		width = 20
	}
	body = strings.TrimRight(body, "\n")
	lines := splitKeepEmpty(body)

	// Gutter width from max line number.
	gutterW := len(fmt.Sprintf("%d", max(1, len(lines))))
	if gutterW < 2 {
		gutterW = 2
	}
	// " N │ " prefix
	prefixW := gutterW + 3
	codeW := width - prefixW
	if codeW < 10 {
		codeW = 10
	}

	// Highlight whole block, then split — preserves token colors across lines.
	highlighted := highlightCode(body, lang, st.NoColor)
	hlLines := splitKeepEmpty(highlighted)
	// Align lengths if highlighter dropped a trailing newline.
	for len(hlLines) < len(lines) {
		hlLines = append(hlLines, "")
	}
	if len(hlLines) > len(lines) {
		hlLines = hlLines[:len(lines)]
	}

	langLabel := lang
	if langLabel == "" {
		langLabel = "code"
	}
	header := st.CodeHeader.Render("◆ " + langLabel)
	if streaming {
		header += st.Muted.Render("  …")
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')

	for i, hl := range hlLines {
		num := fmt.Sprintf("%*d", gutterW, i+1)
		gutter := st.CodeGutter.Render(num) + st.CodeGutter.Render(" │ ")
		// Soft-wrap long code lines with continuation gutters.
		wrapped := wrapCodeLine(hl, codeW, st)
		for j, part := range wrapped {
			if j == 0 {
				b.WriteString(gutter)
				b.WriteString(part)
			} else {
				b.WriteByte('\n')
				b.WriteString(st.CodeGutter.Render(strings.Repeat(" ", gutterW) + " │ "))
				b.WriteString(part)
			}
		}
		if i < len(hlLines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func highlightCode(src, lang string, noColor bool) string {
	if noColor || strings.TrimSpace(src) == "" {
		return src
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Analyse(src)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}
	formatter := formatters.Get("terminal16m")
	if formatter == nil {
		formatter = formatters.Fallback
	}
	it, err := lexer.Tokenise(nil, src)
	if err != nil {
		return src
	}
	var buf strings.Builder
	if err := formatter.Format(&buf, style, it); err != nil {
		return src
	}
	return strings.TrimRight(buf.String(), "\n")
}

// wrapCodeLine wraps a possibly ANSI-colored line to width by visible cells.
func wrapCodeLine(line string, width int, st Styles) []string {
	if width < 1 {
		return []string{line}
	}
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	// lipgloss Width-based wrap with explicit background for any inserted fill.
	wrapped := st.Background.Width(width).Render(line)
	return strings.Split(wrapped, "\n")
}

func splitKeepEmpty(s string) []string {
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
