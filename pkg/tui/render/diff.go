package render

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// DiffOpts controls unified diff rendering from raw old/new snippets.
type DiffOpts struct {
	Path string
	Old  string
	New  string
	// StartLine is 1-based line number of Old/New in the full file (optional).
	// When 0, line numbers start at 1 for the snippet.
	StartLine int
	MaxLines  int // 0 = default 40
	Width     int
	Expanded  bool
	// HeaderNote is optional extra text after the @@ range (e.g. "created").
	HeaderNote string
}

// FileDiff renders a classic unified diff (--- / +++ / @@ / ± body).
// Designed for edit_file and write_file previews when only args are available.
func FileDiff(opts DiffOpts, st Styles) string {
	if opts.Width < 20 {
		opts.Width = 20
	}
	if opts.MaxLines <= 0 {
		opts.MaxLines = 40
	}
	start := opts.StartLine
	if start < 1 {
		start = 1
	}

	oldLines := splitLines(opts.Old)
	newLines := splitLines(opts.New)

	type row struct {
		kind string // " " | "-" | "+"
		text string
	}
	var rows []row
	switch {
	case opts.Old == "" && opts.New != "":
		for _, ln := range newLines {
			rows = append(rows, row{kind: "+", text: ln})
		}
	case opts.New == "" && opts.Old != "":
		for _, ln := range oldLines {
			rows = append(rows, row{kind: "-", text: ln})
		}
	default:
		for _, ln := range oldLines {
			rows = append(rows, row{kind: "-", text: ln})
		}
		for _, ln := range newLines {
			rows = append(rows, row{kind: "+", text: ln})
		}
	}

	truncated := false
	omitted := 0
	if !opts.Expanded && len(rows) > opts.MaxLines {
		omitted = len(rows) - opts.MaxLines
		rows = rows[:opts.MaxLines]
		truncated = true
	}

	path := displayPath(opts.Path)
	added, removed := 0, 0
	for _, r := range rows {
		switch r.kind {
		case "+":
			added++
		case "-":
			removed++
		}
	}

	var b strings.Builder
	writeClassicHeader(&b, path, added, removed, st)
	b.WriteString(st.Muted.Render("--- a/"+path) + "\n")
	b.WriteString(st.Muted.Render("+++ b/"+path) + "\n")

	oldCount := len(oldLines)
	newCount := len(newLines)
	if oldCount < 1 && removed == 0 && opts.Old == "" {
		oldCount = 0
	}
	if newCount < 1 && added == 0 && opts.New == "" {
		newCount = 0
	}
	// When we only have the change block (no context), range starts at start.
	hunk := fmt.Sprintf("@@ -%d,%d +%d,%d @@", start, max(oldCount, 0), start, max(newCount, 0))
	if opts.HeaderNote != "" {
		hunk += " " + opts.HeaderNote
	}
	b.WriteString(st.Brand.Render(hunk) + "\n")

	for _, r := range rows {
		writeClassicBodyLine(&b, r.kind, r.text, opts.Width, st)
	}
	if truncated && omitted > 0 {
		b.WriteString(st.Muted.Render(fmt.Sprintf("  … %d lines omitted (expand activity)", omitted)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// DiffFromToolStep builds a classic unified diff for edit_file / write_file.
// Prefers result.verification_context (real line numbers + surrounding context)
// when the tool has completed; falls back to args while the tool is still running.
func DiffFromToolStep(name string, args map[string]any, result any, width int, expanded bool, st Styles) string {
	path := pathFromArgs(args)
	if vc := extractVerificationContext(result); vc != "" {
		if out := RenderVerificationDiff(vc, path, width, expanded, st); out != "" {
			return out
		}
	}
	return DiffFromToolArgs(name, args, width, expanded, st)
}

// DiffFromToolArgs builds a classic FileDiff preview from edit_file / write_file args.
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
		return FileDiff(DiffOpts{
			Path: path, Old: oldS, New: newS, Width: width, Expanded: expanded, MaxLines: 24,
		}, st)
	case "write_file":
		content, _ := args["content"].(string)
		if content == "" {
			return ""
		}
		// Without a result we only know the new content (create-style preview).
		return FileDiff(DiffOpts{
			Path: path, Old: "", New: content, Width: width, Expanded: expanded, MaxLines: 24,
			HeaderNote: "create",
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

// RenderVerificationDiff colorizes a tool verification_context string as a
// classic unified diff with ---/+++ headers and ± body lines.
func RenderVerificationDiff(vc, fallbackPath string, width int, expanded bool, st Styles) string {
	vc = strings.TrimSpace(vc)
	if vc == "" {
		return ""
	}
	if width < 20 {
		width = 20
	}

	raw := strings.Split(vc, "\n")
	path := displayPath(fallbackPath)
	startLine := 1
	headerNote := ""
	bodyStart := 0
	if len(raw) > 0 {
		if m := verificationHeaderRe.FindStringSubmatch(strings.TrimSpace(raw[0])); m != nil {
			if m[1] != "" {
				path = displayPath(m[1])
			}
			if m[2] != "" {
				if n, err := strconv.Atoi(m[2]); err == nil && n > 0 {
					startLine = n
				}
			}
			headerNote = m[3]
			bodyStart = 1
		}
	}

	type row struct {
		kind string
		text string
		num  int
	}
	var rows []row
	added, removed := 0, 0
	for _, line := range raw[bodyStart:] {
		if line == "  …" || line == "…" {
			rows = append(rows, row{kind: "…", text: "…"})
			continue
		}
		m := verificationLineRe.FindStringSubmatch(line)
		if m == nil {
			// Unknown line — keep as muted context text.
			if strings.TrimSpace(line) != "" {
				rows = append(rows, row{kind: " ", text: line})
			}
			continue
		}
		kind := m[1]
		num, _ := strconv.Atoi(m[2])
		text := m[3]
		switch kind {
		case "+":
			added++
		case "-":
			removed++
		}
		rows = append(rows, row{kind: kind, text: text, num: num})
	}
	if len(rows) == 0 {
		return ""
	}

	const maxLines = 40
	truncated := false
	omitted := 0
	if !expanded && len(rows) > maxLines {
		omitted = len(rows) - maxLines
		rows = rows[:maxLines]
		truncated = true
	}

	// Compute classic @@ -oldStart,oldCount +newStart,newCount from body.
	oldCount, newCount := 0, 0
	oldStart, newStart := startLine, startLine
	firstOld, firstNew := true, true
	for _, r := range rows {
		switch r.kind {
		case " ":
			oldCount++
			newCount++
			if firstOld && r.num > 0 {
				oldStart = r.num
				firstOld = false
			}
			if firstNew && r.num > 0 {
				newStart = r.num
				firstNew = false
			}
		case "-":
			oldCount++
			if firstOld && r.num > 0 {
				oldStart = r.num
				firstOld = false
			}
		case "+":
			newCount++
			if firstNew && r.num > 0 {
				newStart = r.num
				firstNew = false
			}
		}
	}
	if oldCount == 0 {
		oldStart = startLine
	}
	if newCount == 0 {
		newStart = startLine
	}

	var b strings.Builder
	writeClassicHeader(&b, path, added, removed, st)
	b.WriteString(st.Muted.Render("--- a/"+path) + "\n")
	b.WriteString(st.Muted.Render("+++ b/"+path) + "\n")
	hunk := fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)
	if headerNote != "" {
		hunk += " " + headerNote
	}
	b.WriteString(st.Brand.Render(hunk) + "\n")

	for _, r := range rows {
		if r.kind == "…" {
			b.WriteString(st.Muted.Render("  …") + "\n")
			continue
		}
		writeClassicBodyLine(&b, r.kind, r.text, width, st)
	}
	if truncated && omitted > 0 {
		b.WriteString(st.Muted.Render(fmt.Sprintf("  … %d lines omitted (expand activity)", omitted)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeClassicHeader(b *strings.Builder, path string, added, removed int, st Styles) {
	b.WriteString(st.Brand.Render("◆ "+path) + "  " + FormatStats(ActivityStats{
		Kind: "diff", Added: added, Removed: removed,
	}, st))
	b.WriteByte('\n')
}

func writeClassicBodyLine(b *strings.Builder, kind, text string, width int, st Styles) {
	var marker string
	var lineStyle lipgloss.Style
	switch kind {
	case "-":
		marker = "-"
		lineStyle = st.Danger
	case "+":
		marker = "+"
		lineStyle = st.Success
	default:
		marker = " "
		lineStyle = st.Text
	}
	prefix := lineStyle.Render(marker + " ")
	// Keep room for the ± prefix.
	textW := width - 2
	if textW < 10 {
		textW = 10
	}
	// Soft-wrap long lines under the marker column.
	wrapped := lineStyle.Width(textW).Render(text)
	parts := strings.Split(wrapped, "\n")
	for i, p := range parts {
		if i == 0 {
			b.WriteString(prefix)
			b.WriteString(p)
		} else {
			b.WriteByte('\n')
			b.WriteString(lineStyle.Render("  "))
			b.WriteString(p)
		}
	}
	b.WriteByte('\n')
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

// extractVerificationContext pulls verification_context from a tool result map.
func extractVerificationContext(result any) string {
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

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}
