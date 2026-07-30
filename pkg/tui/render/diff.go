package render

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// DiffRow is one line in a dual-gutter diff editor view.
type DiffRow struct {
	Kind string // " " | "-" | "+"
	Text string
	OldN int // 0 = no old line number
	NewN int // 0 = no new line number
}

// DiffOpts controls dual-gutter rendering from raw old/new snippets.
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
}

// FileDiffEditor renders an editor-style dual-gutter diff:
//
//	◆ path  +N −M
//	 old  new
//	 167  167 │   context
//	 169      │ − removed
//	      169 │ + added
func FileDiffEditor(opts DiffOpts, st Styles) string {
	rows := rowsFromOldNew(opts.Old, opts.New, opts.StartLine)
	return renderDiffEditor(displayPath(opts.Path), rows, opts.Note, opts.Width, opts.Expanded, opts.MaxLines, st)
}

// FileDiff is an alias for FileDiffEditor (args-based fallback).
func FileDiff(opts DiffOpts, st Styles) string {
	return FileDiffEditor(opts, st)
}

// DiffFromToolStep builds a dual-gutter diff for edit_file / write_file.
// Prefers result.verification_context; falls back to args.
func DiffFromToolStep(name string, args map[string]any, result any, width int, expanded bool, st Styles) string {
	path := pathFromArgs(args)
	if path == "" {
		path = pathFromResult(result)
	}
	if vc := ExtractVerificationContext(result); vc != "" {
		if out := RenderVerificationDiff(vc, path, width, expanded, st); out != "" {
			return out
		}
	}
	return DiffFromToolArgs(name, args, width, expanded, st)
}

// DiffFromToolArgs builds a dual-gutter preview from tool args only.
func DiffFromToolArgs(name string, args map[string]any, width int, expanded bool, st Styles) string {
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
			Path: path, Old: oldS, New: newS, Width: width, Expanded: expanded, MaxLines: 40,
		}, st)
	case "write_file":
		content, _ := args["content"].(string)
		if content == "" {
			return ""
		}
		return FileDiffEditor(DiffOpts{
			Path: path, Old: "", New: content, Width: width, Expanded: expanded, MaxLines: 40,
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

// RenderVerificationDiff colorizes verification_context as a dual-gutter editor view.
func RenderVerificationDiff(vc, fallbackPath string, width int, expanded bool, st Styles) string {
	rows, path, note := parseVerificationContext(vc, fallbackPath)
	if len(rows) == 0 {
		return ""
	}
	return renderDiffEditor(path, rows, note, width, expanded, 40, st)
}

// parseVerificationContext turns tool verification_context into DiffRows.
func parseVerificationContext(vc, fallbackPath string) (rows []DiffRow, path, note string) {
	vc = strings.TrimSpace(vc)
	if vc == "" {
		return nil, displayPath(fallbackPath), ""
	}
	path = displayPath(fallbackPath)
	raw := strings.Split(vc, "\n")
	bodyStart := 0
	if len(raw) > 0 {
		if m := verificationHeaderRe.FindStringSubmatch(strings.TrimSpace(raw[0])); m != nil {
			if m[1] != "" {
				path = displayPath(m[1])
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
	var rows []DiffRow
	switch {
	case old == "" && new != "":
		n := startLine
		for _, ln := range newLines {
			rows = append(rows, DiffRow{Kind: "+", Text: ln, NewN: n})
			n++
		}
	case new == "" && old != "":
		n := startLine
		for _, ln := range oldLines {
			rows = append(rows, DiffRow{Kind: "-", Text: ln, OldN: n})
			n++
		}
	default:
		o, n := startLine, startLine
		for _, ln := range oldLines {
			rows = append(rows, DiffRow{Kind: "-", Text: ln, OldN: o})
			o++
		}
		for _, ln := range newLines {
			rows = append(rows, DiffRow{Kind: "+", Text: ln, NewN: n})
			n++
		}
	}
	return rows
}

func renderDiffEditor(path string, rows []DiffRow, note string, width int, expanded bool, maxLines int, st Styles) string {
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

	var b strings.Builder
	header := st.Brand.Render("◆ "+path) + "  " + FormatStats(ActivityStats{
		Kind: "diff", Added: added, Removed: removed,
	}, st)
	if note != "" {
		header += st.Muted.Render("  ("+note+")")
	}
	b.WriteString(header)
	b.WriteByte('\n')
	// Column labels
	b.WriteString(st.CodeGutter.Render(fmt.Sprintf("%*s %*s", gutterW, "old", gutterW, "new")))
	b.WriteString(st.CodeGutter.Render(" │"))
	b.WriteByte('\n')

	// prefix: " old  new │ ± "
	prefixW := gutterW*2 + 1 + 3 + 2 // two gutters + space + " │ " + marker + space
	textW := width - prefixW
	if textW < 10 {
		textW = 10
	}

	for _, r := range rows {
		if r.Kind == "…" {
			b.WriteString(st.Muted.Render(strings.Repeat(" ", gutterW*2+1) + " │ …"))
			b.WriteByte('\n')
			continue
		}
		oldG := strings.Repeat(" ", gutterW)
		newG := strings.Repeat(" ", gutterW)
		if r.OldN > 0 {
			oldG = fmt.Sprintf("%*d", gutterW, r.OldN)
		}
		if r.NewN > 0 {
			newG = fmt.Sprintf("%*d", gutterW, r.NewN)
		}
		marker := " "
		var lineStyle lipgloss.Style
		switch r.Kind {
		case "-":
			marker = "−"
			lineStyle = st.Danger
		case "+":
			marker = "+"
			lineStyle = st.Success
		default:
			lineStyle = st.Text
		}
		gutter := st.CodeGutter.Render(oldG+" "+newG) + st.CodeGutter.Render(" │ ") + lineStyle.Render(marker) + " "
		wrapped := lineStyle.Width(textW).Render(r.Text)
		parts := strings.Split(wrapped, "\n")
		for i, p := range parts {
			if i == 0 {
				b.WriteString(gutter)
				b.WriteString(p)
			} else {
				b.WriteByte('\n')
				cont := st.CodeGutter.Render(strings.Repeat(" ", gutterW*2+1)+" │ ") + lineStyle.Render("  ")
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

func displayPath(path string) string {
	if path == "" {
		return "file"
	}
	base := filepath.Base(path)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return path
	}
	return base
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
	if c, ok := args["new_string"].(string); ok {
		added += countLines(c)
	}
	if c, ok := args["old_string"].(string); ok {
		removed += countLines(c)
	}
	return added, removed
}
