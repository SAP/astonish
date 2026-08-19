// Package render provides pure terminal rendering helpers for the chat TUI
// (markdown, code blocks, diffs, activity summaries).
package render

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// Styles carries lipgloss colors used by renderers (subset of tui.Theme).
type Styles struct {
	Background    lipgloss.Style
	Text          lipgloss.Style
	Muted         lipgloss.Style
	Brand         lipgloss.Style
	Success       lipgloss.Style
	Danger        lipgloss.Style
	Number        lipgloss.Style
	CodeGutter    lipgloss.Style
	CodeHeader    lipgloss.Style
	Heading       lipgloss.Style
	Bold          lipgloss.Style
	Italic        lipgloss.Style
	DiffAddedBg   lipgloss.Style // background band for added lines in diffs
	DiffRemovedBg lipgloss.Style // background band for removed lines in diffs
	NoColor       bool
}

// Render applies style to text, returning plain text when NoColor is set.
func (s Styles) Render(style lipgloss.Style, text string) string {
	if s.NoColor {
		return text
	}
	return style.Render(text)
}

// Effective returns a copy of the Styles with all style fields replaced by
// lipgloss.NewStyle() when NoColor is true, so that .Render() calls produce
// plain text. When NoColor is false, returns the styles unchanged.
func (s Styles) Effective() Styles {
	if !s.NoColor {
		return s
	}
	plain := lipgloss.NewStyle()
	return Styles{
		Background:    plain,
		Text:          plain,
		Muted:         plain,
		Brand:         plain,
		Success:       plain,
		Danger:        plain,
		Number:        plain,
		CodeGutter:    plain,
		CodeHeader:    plain,
		Heading:       plain,
		Bold:          plain,
		Italic:        plain,
		DiffAddedBg:   plain,
		DiffRemovedBg: plain,
		NoColor:       true,
	}
}

// DefaultStyles returns a dark-friendly palette matching the TUI theme.
func DefaultStyles() Styles {
	brand := lipgloss.Color("63")
	muted := lipgloss.Color("245")
	text := lipgloss.Color("252")
	green := lipgloss.Color("78")
	red := lipgloss.Color("203")
	orange := lipgloss.Color("208")
	diffAddedBg := lipgloss.Color("#1a3320")
	diffRemovedBg := lipgloss.Color("#3d1f1f")
	return Styles{
		Background:    lipgloss.NewStyle().Background(lipgloss.Color("#000000")),
		Text:          lipgloss.NewStyle().Foreground(text).Background(lipgloss.Color("#000000")),
		Muted:         lipgloss.NewStyle().Foreground(muted).Background(lipgloss.Color("#000000")),
		Brand:         lipgloss.NewStyle().Foreground(brand).Background(lipgloss.Color("#000000")).Bold(true),
		Success:       lipgloss.NewStyle().Foreground(green).Background(lipgloss.Color("#000000")),
		Danger:        lipgloss.NewStyle().Foreground(red).Background(lipgloss.Color("#000000")),
		Number:        lipgloss.NewStyle().Foreground(orange).Background(lipgloss.Color("#000000")),
		CodeGutter:    lipgloss.NewStyle().Foreground(muted).Background(lipgloss.Color("#000000")),
		CodeHeader:    lipgloss.NewStyle().Foreground(brand).Background(lipgloss.Color("#000000")),
		Heading:       lipgloss.NewStyle().Foreground(brand).Background(lipgloss.Color("#000000")).Bold(true),
		Bold:          lipgloss.NewStyle().Foreground(text).Background(lipgloss.Color("#000000")).Bold(true),
		Italic:        lipgloss.NewStyle().Foreground(text).Background(lipgloss.Color("#000000")).Italic(true),
		DiffAddedBg:   lipgloss.NewStyle().Foreground(text).Background(diffAddedBg),
		DiffRemovedBg: lipgloss.NewStyle().Foreground(text).Background(diffRemovedBg),
	}
}

// segment is a piece of markdown source.
type segment struct {
	code     bool
	lang     string
	body     string
	complete bool // false = unclosed fence (streaming)
}

// Markdown renders agent markdown for the terminal at the given width.
// Code fences get syntax highlighting + line numbers; other text is lightly
// styled and hard-wrapped.
func Markdown(src string, width int, st Styles) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	if width < 20 {
		width = 20
	}
	segs := splitFences(src)
	var parts []string
	for _, seg := range segs {
		if seg.code {
			parts = append(parts, CodeBlock(seg.body, seg.lang, width, st, !seg.complete))
			continue
		}
		if strings.TrimSpace(seg.body) == "" {
			continue
		}
		parts = append(parts, prose(seg.body, width, st))
	}
	return strings.TrimRight(strings.Join(parts, "\n\n"), "\n")
}

func splitFences(src string) []segment {
	lines := strings.Split(src, "\n")
	var segs []segment
	var buf strings.Builder
	inFence := false
	lang := ""

	flushText := func() {
		if buf.Len() == 0 {
			return
		}
		segs = append(segs, segment{code: false, body: buf.String()})
		buf.Reset()
	}

	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if !inFence {
			if strings.HasPrefix(trim, "```") {
				flushText()
				inFence = true
				lang = strings.TrimSpace(strings.TrimPrefix(trim, "```"))
				// language may include attrs; take first token
				if i := strings.IndexAny(lang, " \t"); i >= 0 {
					lang = lang[:i]
				}
				continue
			}
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(line)
			continue
		}
		// inside fence: closer is a line that is exactly ```
		if trim == "```" {
			segs = append(segs, segment{code: true, lang: lang, body: buf.String(), complete: true})
			buf.Reset()
			inFence = false
			lang = ""
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
	}
	if inFence {
		segs = append(segs, segment{code: true, lang: lang, body: buf.String(), complete: false})
	} else {
		flushText()
	}
	return segs
}

func prose(src string, width int, st Styles) string {
	lines := strings.Split(src, "\n")
	var out []string
	// blockStart is true at the top of the input and immediately after a blank
	// line — the only positions where a CommonMark indented code block may
	// begin (it cannot interrupt a paragraph).
	blockStart := true
	for i := 0; i < len(lines); {
		line := strings.TrimRight(lines[i], "\r")
		trim := strings.TrimSpace(line)

		// CommonMark indented code block: a run of lines each indented by at
		// least 4 spaces, beginning at a block boundary. The model sometimes
		// emits code this way (e.g. in plan details) instead of using ``` fences;
		// route it through the same syntax-highlighted CodeBlock renderer so it
		// gets a gutter and highlighting rather than flat prose.
		if blockStart && isIndentedCodeLine(line) {
			var codeLines []string
			for i < len(lines) {
				l := strings.TrimRight(lines[i], "\r")
				if isIndentedCodeLine(l) {
					codeLines = append(codeLines, dedentCodeLine(l))
					i++
					continue
				}
				// Blank lines are part of the code block only when followed by
				// another indented code line; otherwise they terminate it.
				if strings.TrimSpace(l) == "" {
					j := i + 1
					for j < len(lines) && strings.TrimSpace(strings.TrimRight(lines[j], "\r")) == "" {
						j++
					}
					if j < len(lines) && isIndentedCodeLine(strings.TrimRight(lines[j], "\r")) {
						for i < j {
							codeLines = append(codeLines, "")
							i++
						}
						continue
					}
				}
				break
			}
			body := strings.Join(codeLines, "\n")
			out = append(out, CodeBlock(body, "", width, st, false))
			blockStart = false
			continue
		}

		// Markdown table: consecutive lines starting with |
		if isTableRow(trim) {
			var tableLines []string
			for i < len(lines) {
				l := strings.TrimRight(lines[i], "\r")
				t := strings.TrimSpace(l)
				if !isTableRow(t) {
					break
				}
				tableLines = append(tableLines, t)
				i++
			}
			out = append(out, renderTable(tableLines, width, st))
			blockStart = false
			continue
		}

		if trim == "" {
			out = append(out, "")
			i++
			blockStart = true
			continue
		}

		// Horizontal rule
		if isHorizontalRule(trim) {
			ruleW := width
			if ruleW > 60 {
				ruleW = 60
			}
			if ruleW < 8 {
				ruleW = 8
			}
			out = append(out, st.Muted.Render(strings.Repeat("─", ruleW)))
			i++
			blockStart = false
			continue
		}

		switch {
		case strings.HasPrefix(line, "###### "):
			out = append(out, wrapStyledPlain(strings.TrimPrefix(line, "###### "), width, st.Heading, st, false))
		case strings.HasPrefix(line, "##### "):
			out = append(out, wrapStyledPlain(strings.TrimPrefix(line, "##### "), width, st.Heading, st, false))
		case strings.HasPrefix(line, "#### "):
			out = append(out, wrapStyledPlain(strings.TrimPrefix(line, "#### "), width, st.Heading, st, false))
		case strings.HasPrefix(line, "### "):
			out = append(out, wrapStyledPlain(strings.TrimPrefix(line, "### "), width, st.Heading, st, false))
		case strings.HasPrefix(line, "## "):
			out = append(out, wrapStyledPlain(strings.TrimPrefix(line, "## "), width, st.Heading, st, false))
		case strings.HasPrefix(line, "# "):
			out = append(out, wrapStyledPlain(strings.TrimPrefix(line, "# "), width, st.Heading, st, false))
		case strings.HasPrefix(line, "- "):
			out = append(out, wrapStyledPlain("• "+strings.TrimPrefix(line, "- "), width, st.Text, st, true))
		case strings.HasPrefix(line, "* ") && !strings.HasPrefix(line, "**"):
			out = append(out, wrapStyledPlain("• "+strings.TrimPrefix(line, "* "), width, st.Text, st, true))
		case strings.HasPrefix(line, "> "):
			out = append(out, wrapStyledPlain("│ "+strings.TrimPrefix(line, "> "), width, st.Muted.Italic(true), st, true))
		default:
			out = append(out, wrapStyledPlain(line, width, st.Text, st, true))
		}
		i++
		blockStart = false
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func isTableRow(trim string) bool {
	return strings.HasPrefix(trim, "|") && strings.Contains(trim[1:], "|")
}

// isIndentedCodeLine reports whether a line qualifies as a CommonMark indented
// code line: non-blank content indented by at least 4 leading spaces (a leading
// tab also counts). Blank lines are handled separately by the caller.
func isIndentedCodeLine(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	if strings.HasPrefix(line, "\t") {
		return true
	}
	return strings.HasPrefix(line, "    ")
}

// dedentCodeLine strips the 4-space (or single-tab) code-block margin from a
// line, preserving any relative indentation beyond it.
func dedentCodeLine(line string) string {
	if strings.HasPrefix(line, "\t") {
		return strings.TrimPrefix(line, "\t")
	}
	return strings.TrimPrefix(line, "    ")
}

func isHorizontalRule(trim string) bool {
	if len(trim) < 3 {
		return false
	}
	// --- or *** or ___ (optionally with spaces between)
	only := strings.Map(func(r rune) rune {
		if r == '-' || r == '*' || r == '_' {
			return r
		}
		if r == ' ' {
			return -1
		}
		return 'x'
	}, trim)
	if strings.Contains(only, "x") || only == "" {
		return false
	}
	// single character type, length >= 3
	c := only[0]
	for i := 1; i < len(only); i++ {
		if only[i] != c {
			return false
		}
	}
	return len(only) >= 3
}

func isTableSeparator(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		s := strings.TrimSpace(c)
		if s == "" {
			return false
		}
		// :---, ---, ---:, :---:
		s = strings.TrimLeft(s, ":")
		s = strings.TrimRight(s, ":")
		if s == "" || strings.Trim(s, "-") != "" {
			return false
		}
	}
	return true
}

func parseTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

// renderTable formats GFM pipe tables into aligned columns.
func renderTable(rows []string, width int, st Styles) string {
	if len(rows) == 0 {
		return ""
	}
	parsed := make([][]string, 0, len(rows))
	for _, r := range rows {
		cells := parseTableRow(r)
		// Skip pure separator rows for data, but remember we had a header.
		if isTableSeparator(cells) {
			// Insert a marker row so we know where the header ends.
			parsed = append(parsed, nil)
			continue
		}
		parsed = append(parsed, cells)
	}
	if len(parsed) == 0 {
		return ""
	}

	// Column count = max cells across rows.
	cols := 0
	for _, r := range parsed {
		if r == nil {
			continue
		}
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return ""
	}

	// Normalize row lengths.
	for i, r := range parsed {
		if r == nil {
			continue
		}
		for len(parsed[i]) < cols {
			parsed[i] = append(parsed[i], "")
		}
	}

	// Measure plain-text widths (without ANSI) for layout.
	colW := make([]int, cols)
	for _, r := range parsed {
		if r == nil {
			continue
		}
		for c, cell := range r {
			// Measure after stripping markdown markers for width estimate.
			plain := stripInlineMarkers(cell)
			w := utf8.RuneCountInString(plain)
			if w > colW[c] {
				colW[c] = w
			}
		}
	}
	// Minimum width 3; cap total to terminal width.
	for c := range colW {
		if colW[c] < 3 {
			colW[c] = 3
		}
	}
	// Shrink columns if total exceeds width (leave room for " │ " separators).
	sepW := 3 // " │ "
	total := 0
	for _, w := range colW {
		total += w
	}
	total += sepW * (cols - 1)
	if total > width && width > 10 {
		// Proportionally shrink.
		budget := width - sepW*(cols-1)
		if budget < cols*3 {
			budget = cols * 3
		}
		sum := 0
		for _, w := range colW {
			sum += w
		}
		if sum > 0 {
			for c := range colW {
				colW[c] = max(3, colW[c]*budget/sum)
			}
		}
	}

	var b strings.Builder
	headerDone := false
	for _, r := range parsed {
		if r == nil {
			// Separator under header
			var parts []string
			for c := 0; c < cols; c++ {
				parts = append(parts, st.Muted.Render(strings.Repeat("─", colW[c])))
			}
			b.WriteString(strings.Join(parts, st.Muted.Render("─┼─")))
			b.WriteByte('\n')
			headerDone = true
			continue
		}
		var cells []string
		for c := 0; c < cols; c++ {
			cell := ""
			if c < len(r) {
				cell = r[c]
			}
			// Truncate plain content to col width, then style.
			plain := stripInlineMarkers(cell)
			if utf8.RuneCountInString(plain) > colW[c] {
				runes := []rune(plain)
				if colW[c] > 1 {
					plain = string(runes[:colW[c]-1]) + "…"
				} else {
					plain = string(runes[:colW[c]])
				}
				// Re-apply as plain text when truncated (inline styles dropped).
				styled := st.Text.Render(padRightRunes(plain, colW[c]))
				// Header row emphasis
				if !headerDone {
					styled = st.Brand.Render(padRightRunes(plain, colW[c]))
				}
				cells = append(cells, styled)
				continue
			}
			// Keep inline markdown when it fits; pad with spaces after visible width.
			inner := inlineMarkdown(cell, st)
			if !headerDone {
				// Prefer bold-ish header look for first row until separator.
				inner = st.Brand.Render(stripInlineMarkers(cell))
			}
			pad := colW[c] - lipgloss.Width(inner)
			if pad < 0 {
				pad = 0
			}
			cells = append(cells, inner+st.Background.Render(strings.Repeat(" ", pad)))
		}
		b.WriteString(strings.Join(cells, st.Muted.Render(" │ ")))
		b.WriteByte('\n')
		// If no separator row in source, still draw one after first row.
		if !headerDone {
			// peek: if next non-nil handling will set headerDone on nil marker
			// only auto-separator when no nil markers exist at all
		}
	}

	// If table had no separator line, insert one after first data row for readability.
	if !headerDone && len(parsed) > 1 {
		// Rebuild with synthetic separator after first row — simpler second pass:
		return renderTableWithAutoSep(parsed, colW, width, st)
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderTableWithAutoSep(parsed [][]string, colW []int, width int, st Styles) string {
	cols := len(colW)
	var b strings.Builder
	for i, r := range parsed {
		if r == nil {
			continue
		}
		var cells []string
		for c := 0; c < cols; c++ {
			cell := ""
			if c < len(r) {
				cell = r[c]
			}
			plain := stripInlineMarkers(cell)
			if utf8.RuneCountInString(plain) > colW[c] {
				runes := []rune(plain)
				if colW[c] > 1 {
					plain = string(runes[:colW[c]-1]) + "…"
				} else {
					plain = string(runes[:colW[c]])
				}
			}
			var styled string
			if i == 0 {
				styled = st.Brand.Render(padRightRunes(plain, colW[c]))
			} else {
				// Prefer inline styles when short enough.
				inner := inlineMarkdown(cell, st)
				if lipgloss.Width(inner) > colW[c] {
					inner = st.Text.Render(padRightRunes(plain, colW[c]))
				} else {
					pad := colW[c] - lipgloss.Width(inner)
					if pad < 0 {
						pad = 0
					}
					inner += st.Background.Render(strings.Repeat(" ", pad))
				}
				styled = inner
			}
			// Ensure brand header is padded.
			if i == 0 {
				// already padded via padRightRunes inside Brand — Brand may change width
				pad := colW[c] - lipgloss.Width(styled)
				if pad > 0 {
					styled += st.Background.Render(strings.Repeat(" ", pad))
				}
			}
			cells = append(cells, styled)
		}
		b.WriteString(strings.Join(cells, st.Muted.Render(" │ ")))
		b.WriteByte('\n')
		if i == 0 {
			var parts []string
			for c := 0; c < cols; c++ {
				parts = append(parts, st.Muted.Render(strings.Repeat("─", colW[c])))
			}
			b.WriteString(strings.Join(parts, st.Muted.Render("─┼─")))
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func stripInlineMarkers(s string) string {
	s = reCode.ReplaceAllString(s, "$1")
	s = reBold.ReplaceAllString(s, "$1$2")
	return s
}

func padRightRunes(s string, w int) string {
	n := utf8.RuneCountInString(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

var (
	reBold = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	reCode = regexp.MustCompile("`([^`]+)`")
)

// inlineMarkdown applies simple emphasis. Order: code → bold.
func inlineMarkdown(s string, st Styles) string {
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		inner := reCode.FindStringSubmatch(m)
		if len(inner) < 2 {
			return m
		}
		return st.Number.Render(inner[1])
	})
	s = reBold.ReplaceAllStringFunc(s, func(m string) string {
		sub := reBold.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		inner := sub[1]
		if inner == "" {
			inner = sub[2]
		}
		return st.Bold.Render(inner)
	})
	return s
}

// wrapStyledPlain wraps plain markdown text before styling. Styling after wrap
// avoids lipgloss filling soft-wrap padding with the terminal's default
// background, which can leak as blue bars in some terminals.
func wrapStyledPlain(s string, width int, base lipgloss.Style, st Styles, inline bool) string {
	if width < 1 {
		if inline {
			return base.Render(inlineMarkdown(s, st))
		}
		return base.Render(s)
	}
	wrapped := lipgloss.NewStyle().Width(width).Render(s)
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, " ")
		if inline {
			line = inlineMarkdown(line, st)
		}
		lines[i] = base.Render(line)
	}
	return strings.Join(lines, "\n")
}

// RuneLen is a tiny helper for tests.
func RuneLen(s string) int { return utf8.RuneCountInString(s) }
