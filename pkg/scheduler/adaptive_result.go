package scheduler

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/genai"
)

// reportMarkerFenceRe matches ```astonish-report fences (same shape as pkg/api).
var reportMarkerFenceRe = regexp.MustCompile("(?s)```astonish-report\\s*\\n(.*?)\\n```")

// extractUserFacingText returns non-thought text from a complete model turn.
// Function call/response parts are ignored.
func extractUserFacingText(parts []*genai.Part) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if part == nil {
			continue
		}
		if part.FunctionCall != nil || part.FunctionResponse != nil {
			continue
		}
		if part.Thought {
			continue
		}
		if part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// applyLastWinsTurn replaces prior delivery text when the new turn has
// non-empty user-facing content (interactive email batch semantics).
func applyLastWinsTurn(prior, turnText string) string {
	if strings.TrimSpace(turnText) == "" {
		return prior
	}
	return turnText
}

// captureWriteFileContent records write_file tool args for later delivery.
// Keys are both the raw path and its base name for fence matching.
func captureWriteFileContent(written map[string]string, args map[string]any) {
	if written == nil || args == nil {
		return
	}
	path, _ := args["file_path"].(string)
	content, _ := args["content"].(string)
	path = strings.TrimSpace(path)
	if path == "" || content == "" {
		return
	}
	written[path] = content
	if base := filepath.Base(path); base != "" && base != path {
		written[base] = content
	}
}

type reportMarker struct {
	Path  string
	Title string
}

func parseReportMarkerFrontmatter(body string) (reportMarker, bool) {
	var info reportMarker
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		value := strings.Trim(strings.TrimSpace(line[colon+1:]), `"'`)
		switch key {
		case "path":
			info.Path = value
		case "title":
			info.Title = value
		}
	}
	if info.Path == "" {
		return reportMarker{}, false
	}
	return info, true
}

func firstReportMarkerPath(text string) string {
	matches := reportMarkerFenceRe.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		if info, ok := parseReportMarkerFrontmatter(m[1]); ok {
			return info.Path
		}
	}
	return ""
}

func stripReportMarkerFences(text string) string {
	return strings.TrimSpace(reportMarkerFenceRe.ReplaceAllString(text, ""))
}

// preferDeliveryBody chooses what adaptive delivery should send:
// report file content (from write_file args or ReadSessionFile) over a bare
// fence / mid-run narration. Falls back to fence-stripped last-wins text.
func preferDeliveryBody(
	lastWins string,
	written map[string]string,
	drained []agent.FileArtifact,
	readFile func(path string) ([]byte, error),
) string {
	reportPath := firstReportMarkerPath(lastWins)
	if body := lookupWritten(written, reportPath); body != "" {
		return body
	}
	if reportPath != "" && readFile != nil {
		if data, err := readFile(reportPath); err == nil && len(bytesTrimSpace(data)) > 0 {
			return string(data)
		}
	}
	// Fence missing but a drained markdown write exists — use the last .md write.
	if body := lastMarkdownWrite(written, drained); body != "" && reportPath == "" {
		// Only auto-pick drained markdown when the last-wins text is empty or
		// fence-only after strip (no real prose).
		if stripReportMarkerFences(lastWins) == "" {
			return body
		}
	}
	if reportPath != "" {
		// Matched a report path via drain list; try each drained path.
		for _, f := range drained {
			if pathsMatch(f.Path, reportPath) {
				if body := lookupWritten(written, f.Path); body != "" {
					return body
				}
				if readFile != nil {
					if data, err := readFile(f.Path); err == nil && len(bytesTrimSpace(data)) > 0 {
						return string(data)
					}
				}
			}
		}
	}

	stripped := stripReportMarkerFences(lastWins)
	if stripped != "" {
		return stripped
	}
	if body := lastMarkdownWrite(written, drained); body != "" {
		return body
	}
	return strings.TrimSpace(lastWins)
}

func lookupWritten(written map[string]string, path string) string {
	if written == nil || path == "" {
		return ""
	}
	if c := strings.TrimSpace(written[path]); c != "" {
		return written[path]
	}
	if c := strings.TrimSpace(written[filepath.Base(path)]); c != "" {
		return written[filepath.Base(path)]
	}
	for k, v := range written {
		if pathsMatch(k, path) && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func lastMarkdownWrite(written map[string]string, drained []agent.FileArtifact) string {
	for i := len(drained) - 1; i >= 0; i-- {
		p := drained[i].Path
		if !isMarkdownPath(p) {
			continue
		}
		if body := lookupWritten(written, p); body != "" {
			return body
		}
	}
	// No drain order — pick any markdown from written map (stable-ish: prefer longer paths).
	var best string
	for k, v := range written {
		if !isMarkdownPath(k) || strings.TrimSpace(v) == "" {
			continue
		}
		if best == "" || len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return written[best]
	}
	return ""
}

func isMarkdownPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".markdown"
}

func pathsMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return filepath.Base(a) == filepath.Base(b) || filepath.Clean(a) == filepath.Clean(b)
}

// ResolveAdaptiveReportBody reuses adaptive marker resolution with captured
// report bodies only. It deliberately has no host-filesystem fallback.
func ResolveAdaptiveReportBody(text string, bodies map[string]string) string {
	return preferDeliveryBody(text, bodies, nil, nil)
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
