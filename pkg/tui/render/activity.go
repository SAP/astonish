package render

import (
	"encoding/json"
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
// Prefers counting from verification_context in the tool result when present.
func StatsFromSteps(steps []ToolStep) ActivityStats {
	added, removed := 0, 0
	for _, s := range steps {
		name := strings.ToLower(s.Name)
		if name != "write_file" && name != "edit_file" {
			continue
		}
		if a, r, ok := statsFromVerification(s.Result); ok {
			added += a
			removed += r
			continue
		}
		if s.Args == nil {
			continue
		}
		if c, ok := s.Args["content"].(string); ok {
			added += countLines(c)
		}
		// edit_file: count only the changed lines (line-level diff) so the
		// +N/−M badge matches the git-style diff shown in the transcript.
		oldS, hasOld := s.Args["old_string"].(string)
		newS, hasNew := s.Args["new_string"].(string)
		if hasOld && hasNew && (oldS != "" || newS != "") {
			a, r := diffLineStats(oldS, newS)
			added += a
			removed += r
			continue
		}
		if hasNew {
			added += countLines(newS)
		}
		if hasOld {
			removed += countLines(oldS)
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

// statsFromVerification counts +/− body lines in a verification_context string.
func statsFromVerification(result any) (added, removed int, ok bool) {
	vc := extractVerificationContext(result)
	if vc == "" {
		return 0, 0, false
	}
	for _, line := range strings.Split(vc, "\n") {
		m := verificationLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch m[1] {
		case "+":
			added++
			ok = true
		case "-":
			removed++
			ok = true
		}
	}
	return added, removed, ok
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
	label := ToolDisplayName(s.Name)
	path := pathHint(s.Args)
	switch categorize(s.Name) {
	case catEdit:
		if path != "" {
			return fmt.Sprintf("Editing %s", truncate(path, 40))
		}
		return "Editing files"
	case catExplore:
		if path != "" {
			return fmt.Sprintf("Reading %s", truncate(path, 40))
		}
		return "Reading files"
	case catSearch:
		return "Searching"
	case catCmd:
		if cmd, ok := s.Args["command"].(string); ok && cmd != "" {
			return fmt.Sprintf("Running `%s`", truncate(cmd, 40))
		}
		return "Running command"
	default:
		return "Running " + label + "…"
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
	case "write_file", "edit_file", "search_replace":
		return catEdit
	case "read_file", "file_tree", "find_files", "repo_map", "read_pdf", "list_dir":
		return catExplore
	case "grep_search", "grep", "search_tools", "web_search", "web_fetch":
		return catSearch
	case "shell_command", "run_terminal_command", "process_read", "process_write", "process_list", "process_kill":
		return catCmd
	default:
		return catOther
	}
}

func pathHint(args map[string]any) string {
	if args == nil {
		return ""
	}
	for _, k := range []string{"path", "file_path", "target_file", "target_directory", "file", "filename"} {
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

// ToolDetailLine returns the primary expanded-row label for one tool step.
func ToolDetailLine(s ToolStep) string {
	label := ToolDisplayName(s.Name)
	status := ToolStatusLabel(s.Status)
	if subject := ToolSubject(s); subject != "" {
		return fmt.Sprintf("%s  %s  %s", status, label, subject)
	}
	return fmt.Sprintf("%s  %s", status, label)
}

// ToolDetailBody returns secondary detail lines for expanded tool details.
func ToolDetailBody(s ToolStep, width int) string {
	if width < 20 {
		width = 20
	}
	switch categorize(s.Name) {
	case catCmd:
		if cmd, ok := s.Args["command"].(string); ok && strings.TrimSpace(cmd) != "" {
			return wrapKeyValue("command", strings.TrimSpace(cmd), width)
		}
	case catSearch:
		if q := firstArg(s.Args, "query", "pattern"); q != "" {
			return wrapKeyValue("query", q, width)
		}
	case catEdit, catExplore:
		if p := pathHint(s.Args); p != "" {
			return wrapKeyValue("path", p, width)
		}
	}
	if s.Status == "error" {
		if msg := resultErrorMessage(s.Result); msg != "" {
			return wrapKeyValue("error", msg, width)
		}
	}
	return ""
}

// ToolArgsPreview returns a compact JSON/text view of the tool request args.
func ToolArgsPreview(s ToolStep, width int) string {
	if s.Args == nil || len(s.Args) == 0 {
		return ""
	}
	text := compactJSON(s.Args)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return wrapMultiline(text, 12, width)
}

// ToolResultPreview returns a wrapped result preview for expanded tool details
// (raw response). File diffs are rendered as main-thread ItemFileDiff items;
// for edit/write we show structured result fields without verification_context.
func ToolResultPreview(s ToolStep, width int) string {
	if s.Result == nil || s.Status == "running" {
		return ""
	}
	if s.Status == "error" {
		if msg := resultErrorMessage(s.Result); msg != "" {
			return wrapMultiline(msg, 8, width)
		}
	}
	var text string
	switch strings.ToLower(s.Name) {
	case "edit_file", "write_file":
		text = compactJSON(stripVerificationContext(s.Result))
	default:
		text = resultText(s.Result)
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return wrapMultiline(text, 8, width)
}

func compactJSON(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func stripVerificationContext(result any) any {
	m, ok := result.(map[string]any)
	if !ok {
		return result
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k == "verification_context" {
			continue
		}
		out[k] = v
	}
	return out
}

// ToolDisplayName maps low-level tool names to short labels for terminal UI.
func ToolDisplayName(name string) string {
	switch strings.ToLower(name) {
	case "read_file":
		return "Read file"
	case "list_dir", "file_tree":
		return "List files"
	case "find_files":
		return "Find files"
	case "grep", "grep_search":
		return "Search"
	case "web_search":
		return "Web search"
	case "web_fetch":
		return "Fetch page"
	case "edit_file", "search_replace":
		return "Edit file"
	case "write_file":
		return "Write file"
	case "run_terminal_command", "shell_command":
		return "Run command"
	case "process_read":
		return "Read process"
	case "process_write":
		return "Write process"
	case "process_list":
		return "List processes"
	case "process_kill":
		return "Kill process"
	case "":
		return "Tool"
	default:
		return strings.ReplaceAll(name, "_", " ")
	}
}

func ToolStatusLabel(status string) string {
	switch status {
	case "running":
		return "⏳"
	case "error":
		return "✖"
	default:
		return "✓"
	}
}

func ToolSubject(s ToolStep) string {
	if p := pathHint(s.Args); p != "" {
		return truncate(p, 48)
	}
	if cmd, ok := s.Args["command"].(string); ok && strings.TrimSpace(cmd) != "" {
		return "`" + truncate(strings.TrimSpace(cmd), 48) + "`"
	}
	if q := firstArg(s.Args, "query", "pattern"); q != "" {
		return truncate(q, 48)
	}
	return ""
}

func firstArg(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func resultErrorMessage(result any) string {
	switch v := result.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if e, ok := v["error"].(string); ok {
			return strings.TrimSpace(e)
		}
		if msg, ok := v["message"].(string); ok {
			return strings.TrimSpace(msg)
		}
	}
	return ""
}

func resultText(result any) string {
	switch v := result.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		for _, k := range []string{"output", "stdout", "text", "content", "message"} {
			if s, ok := v[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	b, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprint(result)
	}
	return string(b)
}

func wrapKeyValue(key, value string, width int) string {
	prefix := key + ": "
	bodyWidth := width - len(prefix)
	if bodyWidth < 10 {
		bodyWidth = width
		prefix = ""
	}
	wrapped := wrapMultiline(value, 8, bodyWidth)
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = prefix + line
			continue
		}
		lines[i] = strings.Repeat(" ", len(prefix)) + line
	}
	return strings.Join(lines, "\n")
}

// WrapMultiline wraps s to width, keeping at most maxLines lines. A "…" line
// is appended when the content is truncated. Used by the TUI approval overlay
// to display tool args without the 60-char single-line hard cut.
func WrapMultiline(s string, maxLines, width int) string {
	return wrapMultiline(s, maxLines, width)
}

func wrapMultiline(s string, maxLines, width int) string {
	if maxLines < 1 {
		maxLines = 1
	}
	if width < 1 {
		width = 1
	}
	var out []string
	for _, raw := range strings.Split(strings.TrimSpace(s), "\n") {
		for _, line := range wrapLine(raw, width) {
			out = append(out, line)
			if len(out) >= maxLines {
				out = append(out, "…")
				return strings.Join(out, "\n")
			}
		}
	}
	return strings.Join(out, "\n")
}

func wrapLine(s string, width int) []string {
	s = strings.TrimRight(s, "\r")
	if s == "" {
		return []string{""}
	}
	var lines []string
	for len([]rune(s)) > width {
		cut := width
		prefix := string([]rune(s)[:cut])
		if idx := strings.LastIndexAny(prefix, " \t-/"); idx > 0 {
			cut = len([]rune(prefix[:idx+1]))
		}
		runes := []rune(s)
		lines = append(lines, strings.TrimRight(string(runes[:cut]), " \t"))
		s = strings.TrimLeft(string(runes[cut:]), " \t")
	}
	lines = append(lines, s)
	return lines
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
