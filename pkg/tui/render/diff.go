package render

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// DiffRow is one line in a single-gutter diff editor view.
type DiffRow struct {
	Kind string // " " | "-" | "+"
	Text string
	OldN int // 0 = no old line number
	NewN int // 0 = no new line number
}

// DiffOpts controls single-gutter rendering from raw old/new snippets.
type DiffOpts struct {
	Path string
	Old  string
	New  string
	// StartLine is 1-based line number of Old/New in the full file (optional).
	StartLine int
	MaxLines  int // 0 = default 40
	Width     int
	Expanded  bool
	Note      string // optional header note e.g. "created"
	// Root is the workspace/project root. When set, the header shows the file
	// path relative to Root instead of just the basename.
	Root string
}

// FileDiffEditor renders an editor-style single-gutter diff. Each row shows one
// line number, colored to match its content: neutral for unchanged context, red
// for a removed line, green for an added line.
//
//	◆ path  +N −M
//	 167 │   context
//	 169 │ − removed
//	 169 │ + added
func FileDiffEditor(opts DiffOpts, st Styles) string {
	rows := rowsFromOldNew(opts.Old, opts.New, opts.StartLine)
	return renderDiffEditor(displayPath(opts.Path, opts.Root), opts.Path, rows, opts.Note, opts.Width, opts.Expanded, opts.MaxLines, st)
}

// FileDiff is an alias for FileDiffEditor (args-based fallback).
func FileDiff(opts DiffOpts, st Styles) string {
	return FileDiffEditor(opts, st)
}

// DiffFromToolStep builds a single-gutter diff for edit_file / write_file.
// Prefers result.verification_context; falls back to args. root is the
// workspace root used to render project-relative file paths (may be empty).
func DiffFromToolStep(name string, args map[string]any, result any, width int, expanded bool, root string, st Styles) string {
	path := pathFromArgs(args)
	if path == "" {
		path = pathFromResult(result)
	}
	if vc := ExtractVerificationContext(result); vc != "" {
		if out := RenderVerificationDiff(vc, path, width, expanded, root, st); out != "" {
			return out
		}
	}
	return DiffFromToolArgs(name, args, width, expanded, root, st)
}

// DiffFromToolArgs builds a single-gutter preview from tool args only. root is
// the workspace root used to render project-relative file paths (may be empty).
func DiffFromToolArgs(name string, args map[string]any, width int, expanded bool, root string, st Styles) string {
	if args == nil {
		return ""
	}
	path := pathFromArgs(args)
	switch strings.ToLower(name) {
	case "edit_file":
		oldS, _ := args["old_string"].(string)
		newS, _ := args["new_string"].(string)
		if oldS == "" && newS == "" {
			return ""
		}
		return FileDiffEditor(DiffOpts{
			Path: path, Old: oldS, New: newS, Width: width, Expanded: expanded, MaxLines: 40, Root: root,
		}, st)
	case "write_file":
		content, _ := args["content"].(string)
		if content == "" {
			return ""
		}
		return FileDiffEditor(DiffOpts{
			Path: path, Old: "", New: content, Width: width, Expanded: expanded, MaxLines: 40, Root: root,
			Note: "created",
		}, st)
	default:
		return ""
	}
}

// verificationLineRe matches tool verification_context body lines:
//
//	"  166| context"
//	"- 169| removed"
//	"+ 169| added"
var verificationLineRe = regexp.MustCompile(`^([+\- ])\s+(\d+)\|\s?(.*)$`)

// verificationHeaderRe matches "@@ basename:169" or "@@ basename:1 (created)".
var verificationHeaderRe = regexp.MustCompile(`^@@\s+([^:]+)(?::(\d+))?(?:\s+\(([^)]*)\))?\s*$`)

// RenderVerificationDiff colorizes verification_context as a single-gutter editor view.
// root is the workspace root used to render project-relative file paths (may be empty).
func RenderVerificationDiff(vc, fallbackPath string, width int, expanded bool, root string, st Styles) string {
	rows, path, note := parseVerificationContext(vc, fallbackPath, root)
	if len(rows) == 0 {
		return ""
	}
	return renderDiffEditor(path, fallbackPath, rows, note, width, expanded, 40, st)
}

// parseVerificationContext turns tool verification_context into DiffRows. The
// verification_context header (@@ basename:NN) only carries a basename, so when
// a full fallbackPath is available it is preferred for the display header so the
// project-relative path is shown instead of just the filename.
func parseVerificationContext(vc, fallbackPath, root string) (rows []DiffRow, path, note string) {
	vc = strings.TrimSpace(vc)
	if vc == "" {
		return nil, displayPath(fallbackPath, root), ""
	}
	path = displayPath(fallbackPath, root)
	raw := strings.Split(vc, "\n")
	bodyStart := 0
	if len(raw) > 0 {
		if m := verificationHeaderRe.FindStringSubmatch(strings.TrimSpace(raw[0])); m != nil {
			// Only fall back to the header's (basename-only) path when no full
			// fallbackPath was provided; otherwise keep the project-relative path.
			if m[1] != "" && strings.TrimSpace(fallbackPath) == "" {
				path = displayPath(m[1], root)
			}
			note = m[3]
			bodyStart = 1
		}
	}
	for _, line := range raw[bodyStart:] {
		if line == "  …" || line == "…" {
			rows = append(rows, DiffRow{Kind: "…", Text: "…"})
			continue
		}
		m := verificationLineRe.FindStringSubmatch(line)
		if m == nil {
			if strings.TrimSpace(line) != "" {
				rows = append(rows, DiffRow{Kind: " ", Text: line})
			}
			continue
		}
		kind := m[1]
		num, _ := strconv.Atoi(m[2])
		text := m[3]
		r := DiffRow{Kind: kind, Text: text}
		switch kind {
		case "-":
			r.OldN = num
		case "+":
			r.NewN = num
		default:
			// Context lines may use old or new numbering; fill both when present.
			r.OldN = num
			r.NewN = num
		}
		rows = append(rows, r)
	}
	return rows, path, note
}

func rowsFromOldNew(old, new string, startLine int) []DiffRow {
	if startLine < 1 {
		startLine = 1
	}
	oldLines := splitLines(old)
	newLines := splitLines(new)
	switch {
	case old == "" && new != "":
		rows := make([]DiffRow, 0, len(newLines))
		n := startLine
		for _, ln := range newLines {
			rows = append(rows, DiffRow{Kind: "+", Text: ln, NewN: n})
			n++
		}
		return rows
	case new == "" && old != "":
		rows := make([]DiffRow, 0, len(oldLines))
		n := startLine
		for _, ln := range oldLines {
			rows = append(rows, DiffRow{Kind: "-", Text: ln, OldN: n})
			n++
		}
		return rows
	default:
		// Real line-level diff: only the changed lines are shown as ±, with a
		// few unchanged lines of context around each hunk (git-style). Long
		// unchanged runs collapse to a "…" gap row.
		return diffRowsWithContext(oldLines, newLines, startLine, defaultDiffContext)
	}
}

// defaultDiffContext is the number of unchanged lines kept around each hunk.
const defaultDiffContext = 3

// diffRowsWithContext computes a line-level diff of old→new (LCS) and returns
// single-gutter rows, keeping ctx unchanged lines around each change and
// collapsing longer unchanged runs into a single "…" gap row.
func diffRowsWithContext(oldLines, newLines []string, startLine, ctx int) []DiffRow {
	if ctx < 0 {
		ctx = 0
	}
	full := diffOps(oldLines, newLines, startLine)
	if len(full) == 0 {
		return nil
	}
	// Mark which rows are "near" a change (a ± row itself, or within ctx of one).
	changed := make([]bool, len(full))
	for i, r := range full {
		if r.Kind == "-" || r.Kind == "+" {
			changed[i] = true
		}
	}
	keep := make([]bool, len(full))
	for i, isChange := range changed {
		if !isChange {
			continue
		}
		lo := i - ctx
		if lo < 0 {
			lo = 0
		}
		hi := i + ctx
		if hi > len(full)-1 {
			hi = len(full) - 1
		}
		for j := lo; j <= hi; j++ {
			keep[j] = true
		}
	}
	// Emit kept rows; collapse dropped runs into a single gap row.
	var rows []DiffRow
	gapPending := false
	for i, r := range full {
		if keep[i] {
			if gapPending {
				rows = append(rows, DiffRow{Kind: "…", Text: "…"})
				gapPending = false
			}
			rows = append(rows, r)
			continue
		}
		gapPending = true
	}
	// Trailing dropped run: no gap row needed (nothing to elide toward).
	return rows
}

// diffOps returns the full ordered list of diff rows (equal/delete/insert) for
// old→new using a longest-common-subsequence backtrace, assigning 1-based old
// and new line numbers offset by startLine.
func diffOps(oldLines, newLines []string, startLine int) []DiffRow {
	m, n := len(oldLines), len(newLines)
	// LCS length table.
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var rows []DiffRow
	i, j := 0, 0
	oldN, newN := startLine, startLine
	for i < m && j < n {
		switch {
		case oldLines[i] == newLines[j]:
			rows = append(rows, DiffRow{Kind: " ", Text: oldLines[i], OldN: oldN, NewN: newN})
			i, j, oldN, newN = i+1, j+1, oldN+1, newN+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			rows = append(rows, DiffRow{Kind: "-", Text: oldLines[i], OldN: oldN})
			i, oldN = i+1, oldN+1
		default:
			rows = append(rows, DiffRow{Kind: "+", Text: newLines[j], NewN: newN})
			j, newN = j+1, newN+1
		}
	}
	for ; i < m; i++ {
		rows = append(rows, DiffRow{Kind: "-", Text: oldLines[i], OldN: oldN})
		oldN++
	}
	for ; j < n; j++ {
		rows = append(rows, DiffRow{Kind: "+", Text: newLines[j], NewN: newN})
		newN++
	}
	return rows
}

func renderDiffEditor(path, rawPath string, rows []DiffRow, note string, width int, expanded bool, maxLines int, st Styles) string {
	if width < 20 {
		width = 20
	}
	if maxLines <= 0 {
		maxLines = 40
	}
	if path == "" {
		path = "file"
	}

	added, removed := 0, 0
	for _, r := range rows {
		switch r.Kind {
		case "+":
			added++
		case "-":
			removed++
		}
	}

	truncated := false
	omitted := 0
	if !expanded && len(rows) > maxLines {
		omitted = len(rows) - maxLines
		rows = rows[:maxLines]
		truncated = true
	}

	// Gutter width from largest line number.
	maxN := 1
	for _, r := range rows {
		if r.OldN > maxN {
			maxN = r.OldN
		}
		if r.NewN > maxN {
			maxN = r.NewN
		}
	}
	gutterW := len(strconv.Itoa(maxN))
	if gutterW < 3 {
		gutterW = 3
	}

	// Detect language from file path for syntax highlighting.
	lang := langFromPath(rawPath)

	var b strings.Builder
	header := st.Brand.Render("◆ "+path) + "  " + FormatStats(ActivityStats{
		Kind: "diff", Added: added, Removed: removed,
	}, st)
	if note != "" {
		header += st.Muted.Render("  ("+note+")")
	}
	b.WriteString(header)
	b.WriteByte('\n')

	// prefix: " N │ ± "
	prefixW := gutterW + 3 + 2 // one gutter + " │ " + marker + space
	textW := width - prefixW
	if textW < 10 {
		textW = 10
	}

	for _, r := range rows {
		if r.Kind == "…" {
			b.WriteString(st.Muted.Render(strings.Repeat(" ", gutterW) + " │ …"))
			b.WriteByte('\n')
			continue
		}
		marker := " "
		var bgStyle lipgloss.Style
		var markerStyle lipgloss.Style
		// num is the single line number shown for this row: the new number for
		// context/added lines, the old number for removed lines.
		num := r.NewN
		switch r.Kind {
		case "-":
			marker = "−"
			bgStyle = st.DiffRemovedBg
			markerStyle = st.Danger
			num = r.OldN
		case "+":
			marker = "+"
			bgStyle = st.DiffAddedBg
			markerStyle = st.Success
		default:
			bgStyle = st.Text
			markerStyle = st.Text
			if num == 0 {
				num = r.OldN
			}
		}
		numCol := strings.Repeat(" ", gutterW)
		if num > 0 {
			numCol = fmt.Sprintf("%*d", gutterW, num)
		}

		// Syntax-highlight the line content.
		content := r.Text
		// Expand tabs to spaces so width calculations match the terminal's
		// rendering (lipgloss.Width counts tabs as 0, but terminals render
		// them as multiple cells).
		content = expandTabs(content, 4)
		if !st.NoColor && lang != "" {
			if hl := highlightCode(content, lang, false); hl != "" {
				content = hl
			}
		}

		// Truncate to textW so long lines don't overflow the terminal and
		// cause ugly soft-wraps.
		if lipgloss.Width(content) > textW {
			content = ansi.Truncate(content, textW, "…")
		}

		// Build the gutter: line number (colored with background), separator,
		// and diff marker. For changed lines the number and marker use the
		// background band; context lines stay neutral.
		var gutter string
		if r.Kind == "-" || r.Kind == "+" {
			gutter = bgStyle.Render(numCol) + st.CodeGutter.Render(" │ ") + markerStyle.Render(marker) + " "
		} else {
			gutter = st.CodeGutter.Render(numCol) + st.CodeGutter.Render(" │ ") + markerStyle.Render(marker) + " "
		}

		// For changed lines, render the text with the diff background band
		// padded to full width; context lines render without padding to avoid
		// a visible background stripe that differs from the terminal default.
		var rendered string
		if r.Kind == "-" || r.Kind == "+" {
			rendered = padWithBg(content, textW, bgStyle, st.NoColor)
		} else {
			rendered = content
		}

		parts := strings.Split(rendered, "\n")
		for i, p := range parts {
			if i == 0 {
				b.WriteString(gutter)
				b.WriteString(p)
			} else {
				b.WriteByte('\n')
				cont := st.CodeGutter.Render(strings.Repeat(" ", gutterW)+" │ ") + markerStyle.Render("  ")
				b.WriteString(cont)
				b.WriteString(p)
			}
		}
		b.WriteByte('\n')
	}
	if truncated && omitted > 0 {
		b.WriteString(st.Muted.Render(fmt.Sprintf("  … %d lines omitted", omitted)))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// langFromPath extracts the language name from a file path for syntax
// highlighting. Returns "" when the language cannot be determined.
func langFromPath(path string) string {
	if path == "" {
		return ""
	}
	l := lexers.Match(filepath.Base(path))
	if l == nil {
		return ""
	}
	cfg := l.Config()
	if cfg == nil {
		return ""
	}
	return cfg.Name
}

// padWithBg renders content with a background color band, padding to width so
// the colored band extends the full line. For syntax-highlighted content (that
// already contains ANSI sequences with resets), it inserts the background ANSI
// code after every reset so the background persists across tokens.
func padWithBg(content string, width int, bg lipgloss.Style, noColor bool) string {
	if noColor {
		// Pad with spaces to fill the line width (no wrapping).
		visW := lipgloss.Width(content)
		if visW < width {
			content += strings.Repeat(" ", width-visW)
		}
		return content
	}
	// Extract the raw background ANSI code from the style.
	bgCode := extractBgCode(bg)
	if bgCode == "" {
		// Fallback: no background extractable, pad manually with the style.
		visW := lipgloss.Width(content)
		pad := ""
		if visW < width {
			pad = strings.Repeat(" ", width-visW)
		}
		return bg.Render(content + pad)
	}
	// Compute visible width of the content to determine padding needed.
	visW := lipgloss.Width(content)
	pad := ""
	if visW < width {
		pad = strings.Repeat(" ", width-visW)
	}
	// Insert the background code after every ANSI reset (\x1b[0m) so the
	// background persists through syntax-highlighted token boundaries.
	patched := strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+bgCode)
	return bgCode + patched + pad + "\x1b[0m"
}

// extractBgCode extracts the ANSI background escape sequence from a lipgloss
// style. Lipgloss combines all SGR params into one sequence (e.g.,
// \x1b[38;5;252;48;2;26;51;32m), so we search for "48;2;" or "48;5;" within
// the rendered output and extract the background portion as a standalone
// escape sequence.
func extractBgCode(st lipgloss.Style) string {
	rendered := st.Render("X")
	return extractBgFromRendered(rendered)
}

// extractBgFromRendered parses an ANSI-formatted string and extracts the
// background color escape sequence (48;2;R;G;B or 48;5;N) as a standalone
// escape. Returns "" if no background is found.
func extractBgFromRendered(rendered string) string {
	// Look for 48;2; (true-color) or 48;5; (256-color) within the output.
	for _, marker := range []string{"48;2;", "48;5;"} {
		idx := strings.Index(rendered, marker)
		if idx < 0 {
			continue
		}
		// From the marker, scan forward collecting digits and semicolons to
		// form the complete background parameter string.
		end := idx + len(marker)
		for end < len(rendered) && (rendered[end] >= '0' && rendered[end] <= '9' || rendered[end] == ';') {
			end++
		}
		params := rendered[idx:end]
		// Trim trailing semicolon (separating from next param in compound SGR).
		params = strings.TrimRight(params, ";")
		if len(params) > len(marker) {
			return "\x1b[" + params + "m"
		}
	}
	return ""
}

func pathFromArgs(args map[string]any) string {
	if args == nil {
		return ""
	}
	for _, k := range []string{"path", "file_path"} {
		if v, ok := args[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func pathFromResult(result any) string {
	if result == nil {
		return ""
	}
	m, ok := result.(map[string]any)
	if !ok {
		return ""
	}
	if s, ok := m["path"].(string); ok && s != "" {
		return s
	}
	return ""
}

// displayPath returns the path to show in a diff header. When root is a
// non-empty directory that contains path, the project-relative path is returned
// (e.g. "pkg/tui/render/diff.go"). Otherwise the original path is returned as-is
// so absolute or already-relative paths still carry their directory context.
func displayPath(path, root string) string {
	if path == "" {
		return "file"
	}
	if rel := relativeToRoot(path, root); rel != "" {
		return rel
	}
	// No usable root: keep the path as-is (relative paths already carry
	// project context; absolute paths are shown in full rather than truncated).
	return filepath.ToSlash(path)
}

// relativeToRoot returns path relative to root using forward slashes, or "" when
// root is empty, path is not under root, or the relation cannot be computed.
func relativeToRoot(path, root string) string {
	root = strings.TrimSpace(root)
	if root == "" || path == "" {
		return ""
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	// Reject paths that escape the root ("../...") — show the original instead.
	if rel == "." || rel == "" || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

// ExtractVerificationContext pulls verification_context from a tool result map.
func ExtractVerificationContext(result any) string {
	if result == nil {
		return ""
	}
	switch v := result.(type) {
	case map[string]any:
		if s, ok := v["verification_context"].(string); ok {
			return strings.TrimSpace(s)
		}
	case map[string]string:
		if s, ok := v["verification_context"]; ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// extractVerificationContext is used by activity stats (same package).
func extractVerificationContext(result any) string {
	return ExtractVerificationContext(result)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// expandTabs replaces each tab character with spaces to align to the given tab
// stop width. This ensures width calculations match terminal rendering.
func expandTabs(s string, tabWidth int) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, ch := range s {
		if ch == '\t' {
			spaces := tabWidth - (col % tabWidth)
			b.WriteString(strings.Repeat(" ", spaces))
			col += spaces
		} else {
			b.WriteRune(ch)
			col++
		}
	}
	return b.String()
}

// CountDiffStats returns +added/−removed from a verification_context or args-like old/new.
func CountDiffStats(result any, args map[string]any) (added, removed int) {
	if a, r, ok := statsFromVerification(result); ok {
		return a, r
	}
	if args == nil {
		return 0, 0
	}
	if c, ok := args["content"].(string); ok {
		added += countLines(c)
	}
	// For edit_file, count only the lines that actually changed (line-level
	// diff), not every old/new line, so the +N/−M badge matches the rendered
	// git-style diff.
	oldS, hasOld := args["old_string"].(string)
	newS, hasNew := args["new_string"].(string)
	if hasOld && hasNew && (oldS != "" || newS != "") {
		a, r := diffLineStats(oldS, newS)
		return added + a, removed + r
	}
	if hasNew {
		added += countLines(newS)
	}
	if hasOld {
		removed += countLines(oldS)
	}
	return added, removed
}

// diffLineStats returns the number of added and removed lines from a line-level
// diff of old→new (only the lines that actually changed).
func diffLineStats(old, new string) (added, removed int) {
	for _, r := range diffOps(splitLines(old), splitLines(new), 1) {
		switch r.Kind {
		case "+":
			added++
		case "-":
			removed++
		}
	}
	return added, removed
}
