package render

import (
	"fmt"
	"strings"
)

// ToolStep is a display-oriented tool call/result pair.
type ToolStep struct {
	Name   string
	Args   map[string]any
	Result any
	Status string // running | complete | error
}

// ActivityStats is either a diff metric or a generic badge count.
type ActivityStats struct {
	Kind    string // "diff" | "badge"
	Added   int
	Removed int
	Count   int
}

// ActivitySummary builds a Cursor/Studio-style collapsed summary.
func ActivitySummary(steps []ToolStep, streaming bool) string {
	if len(steps) == 0 {
		return "Tools"
	}
	if streaming {
		for i := len(steps) - 1; i >= 0; i-- {
			if steps[i].Status == "running" {
				return liveHint(steps[i])
			}
		}
	}
	body := categorizedBody(steps)
	failed := 0
	for _, s := range steps {
		if s.Status == "error" {
			failed++
		}
	}
	if failed == 1 {
		for _, s := range steps {
			if s.Status == "error" {
				return body + " · " + s.Name + " failed"
			}
		}
	}
	if failed > 1 {
		return fmt.Sprintf("%s · %d failed", body, failed)
	}
	return body
}

// StatsFromSteps computes +added/−removed for edit tools, else a badge count.
func StatsFromSteps(steps []ToolStep) ActivityStats {
	added, removed := 0, 0
	for _, s := range steps {
		name := strings.ToLower(s.Name)
		if name != "write_file" && name != "edit_file" {
			continue
		}
		if s.Args == nil {
			continue
		}
		if c, ok := s.Args["content"].(string); ok {
			added += countLines(c)
		}
		if c, ok := s.Args["new_string"].(string); ok {
			added += countLines(c)
		}
		if c, ok := s.Args["old_string"].(string); ok {
			removed += countLines(c)
		}
	}
	if added > 0 || removed > 0 {
		return ActivityStats{Kind: "diff", Added: added, Removed: removed}
	}
	n := len(steps)
	if n < 1 {
		n = 1
	}
	return ActivityStats{Kind: "badge", Count: n}
}

// FormatStats renders +N −M or a count badge using styles.
func FormatStats(st ActivityStats, styles Styles) string {
	if st.Kind == "diff" {
		var parts []string
		if st.Added > 0 {
			parts = append(parts, styles.Success.Render(fmt.Sprintf("+%d", st.Added)))
		}
		if st.Removed > 0 {
			parts = append(parts, styles.Danger.Render(fmt.Sprintf("−%d", st.Removed)))
		}
		return strings.Join(parts, " ")
	}
	return styles.Number.Render(fmt.Sprintf("%d", st.Count))
}

func liveHint(s ToolStep) string {
	name := s.Name
	if name == "" {
		name = "tool"
	}
	path := pathHint(s.Args)
	switch strings.ToLower(name) {
	case "edit_file", "write_file":
		if path != "" {
			return fmt.Sprintf("Editing %s with %s", truncate(path, 40), name)
		}
		return "Editing with " + name
	case "read_file":
		if path != "" {
			return fmt.Sprintf("Reading %s", truncate(path, 40))
		}
		return "Reading"
	case "shell_command":
		if cmd, ok := s.Args["command"].(string); ok && cmd != "" {
			return fmt.Sprintf("Running `%s`", truncate(cmd, 40))
		}
		return "Running command"
	default:
		return "Running " + name + "…"
	}
}

type toolCat string

const (
	catEdit    toolCat = "edit"
	catExplore toolCat = "explore"
	catSearch  toolCat = "search"
	catCmd     toolCat = "command"
	catOther   toolCat = "other"
)

func categorizedBody(steps []ToolStep) string {
	order := []toolCat{catEdit, catExplore, catSearch, catCmd, catOther}
	counts := map[toolCat]int{}
	paths := map[toolCat]map[string]struct{}{}
	ensure := func(c toolCat) {
		if paths[c] == nil {
			paths[c] = map[string]struct{}{}
		}
	}
	for _, s := range steps {
		c := categorize(s.Name)
		ensure(c)
		if p := pathHint(s.Args); p != "" && (c == catEdit || c == catExplore) {
			paths[c][p] = struct{}{}
		} else {
			counts[c]++
		}
	}
	var parts []string
	for _, c := range order {
		n := counts[c] + len(paths[c])
		if n == 0 {
			continue
		}
		switch c {
		case catEdit:
			parts = append(parts, fmt.Sprintf("Edited %d %s", n, plural(n, "file", "files")))
		case catExplore:
			parts = append(parts, fmt.Sprintf("explored %d %s", n, plural(n, "file", "files")))
		case catSearch:
			parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "search", "searches")))
		case catCmd:
			parts = append(parts, fmt.Sprintf("ran %d %s", n, plural(n, "command", "commands")))
		default:
			parts = append(parts, fmt.Sprintf("used %d %s", n, plural(n, "tool", "tools")))
		}
	}
	if len(parts) == 0 {
		return "Done"
	}
	// Capitalize first phrase.
	parts[0] = strings.ToUpper(parts[0][:1]) + parts[0][1:]
	return strings.Join(parts, ", ")
}

func categorize(name string) toolCat {
	key := strings.ToLower(name)
	switch key {
	case "write_file", "edit_file":
		return catEdit
	case "read_file", "file_tree", "find_files", "repo_map", "read_pdf":
		return catExplore
	case "grep_search", "search_tools", "web_search":
		return catSearch
	case "shell_command", "process_read", "process_write", "process_list", "process_kill":
		return catCmd
	default:
		return catOther
	}
}

func pathHint(args map[string]any) string {
	if args == nil {
		return ""
	}
	for _, k := range []string{"path", "file_path", "file", "filename"} {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n") + 1
	if strings.HasSuffix(s, "\n") {
		n--
	}
	if n < 1 {
		return 1
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	if max < 2 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
