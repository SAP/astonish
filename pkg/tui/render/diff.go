package render

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// DiffOpts controls unified diff rendering.
type DiffOpts struct {
	Path     string
	Old      string
	New      string
	// StartLine is 1-based line number of Old/New in the full file (optional).
	// When 0, line numbers start at 1 for the snippet.
	StartLine int
	MaxLines  int // 0 = default 40
	Width     int
	Expanded  bool
}

// FileDiff renders a compact unified diff with dual-gutter line numbers.
// Designed for edit_file (old_string/new_string) and write_file (create) previews.
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

	// Simple LCS-free display: if both non-empty and not equal, show -old then +new.
	// Good enough for edit_file replace snippets.
	type row struct {
		kind string // " " | "-" | "+"
		text string
		oldN int // 0 = none
		newN int
	}
	var rows []row
	if opts.Old == "" && opts.New != "" {
		// Create / full write
		n := start
		for _, ln := range newLines {
			rows = append(rows, row{kind: "+", text: ln, newN: n})
			n++
		}
	} else if opts.New == "" && opts.Old != "" {
		n := start
		for _, ln := range oldLines {
			rows = append(rows, row{kind: "-", text: ln, oldN: n})
			n++
		}
	} else {
		// Show removal then addition blocks (mini hunk).
		o, n := start, start
		for _, ln := range oldLines {
			rows = append(rows, row{kind: "-", text: ln, oldN: o})
			o++
		}
		for _, ln := range newLines {
			rows = append(rows, row{kind: "+", text: ln, newN: n})
			n++
		}
	}

	truncated := false
	omitted := 0
	if !opts.Expanded && len(rows) > opts.MaxLines {
		omitted = len(rows) - opts.MaxLines
		rows = rows[:opts.MaxLines]
		truncated = true
	}

	path := opts.Path
	if path == "" {
		path = "file"
	}
	header := st.Brand.Render("◆ "+path) + "  " + FormatStats(ActivityStats{
		Kind: "diff", Added: countLines(opts.New), Removed: countLines(opts.Old),
	}, st)

	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')

	// Dual gutter: old | new | marker | text
	gutterW := 4
	for _, r := range rows {
		oldG := strings.Repeat(" ", gutterW)
		newG := strings.Repeat(" ", gutterW)
		if r.oldN > 0 {
			oldG = fmt.Sprintf("%*d", gutterW, r.oldN)
		}
		if r.newN > 0 {
			newG = fmt.Sprintf("%*d", gutterW, r.newN)
		}
		marker := " "
		var lineStyle lipgloss.Style
		switch r.kind {
		case "-":
			marker = "−"
			lineStyle = st.Danger
		case "+":
			marker = "+"
			lineStyle = st.Success
		default:
			lineStyle = st.Text
		}
		gutter := st.CodeGutter.Render(oldG+" "+newG) + " " + lineStyle.Render(marker) + " "
		// Wrap long lines under the gutter.
		textW := opts.Width - lipgloss.Width(gutter)
		if textW < 10 {
			textW = 10
		}
		text := lineStyle.Width(textW).Render(r.text)
		parts := strings.Split(text, "\n")
		for i, p := range parts {
			if i == 0 {
				b.WriteString(gutter)
				b.WriteString(p)
			} else {
				b.WriteByte('\n')
				b.WriteString(st.CodeGutter.Render(strings.Repeat(" ", gutterW*2+1)) + "   ")
				b.WriteString(p)
			}
		}
		b.WriteByte('\n')
	}
	if truncated && omitted > 0 {
		b.WriteString(st.Muted.Render(fmt.Sprintf("  … %d lines omitted (expand activity)", omitted)))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// DiffFromToolArgs builds a FileDiff preview from edit_file / write_file args.
func DiffFromToolArgs(name string, args map[string]any, width int, expanded bool, st Styles) string {
	if args == nil {
		return ""
	}
	path := ""
	for _, k := range []string{"path", "file_path"} {
		if v, ok := args[k].(string); ok && v != "" {
			path = v
			break
		}
	}
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
		return FileDiff(DiffOpts{
			Path: path, Old: "", New: content, Width: width, Expanded: expanded, MaxLines: 24,
		}, st)
	default:
		return ""
	}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}
