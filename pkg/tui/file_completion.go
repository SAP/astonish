package tui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	maxFileCompletionCandidates = 80
	maxMentionFileBytes         = 120 * 1024
	maxMentionContextBytes      = 240 * 1024
)

var skippedCompletionDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"coverage":     true,
	"tmp":          true,
	".next":        true,
	".turbo":       true,
}

// fileCandidate is one local file mention completion candidate.
type fileCandidate struct {
	Path string
}

// fileCompletion holds the active @file completion popup state.
type fileCompletion struct {
	active  bool
	query   string
	matches []fileCandidate
	cursor  int
}

func (f fileCompletion) selectedFile() (fileCandidate, bool) {
	if !f.active || len(f.matches) == 0 {
		return fileCandidate{}, false
	}
	if f.cursor < 0 || f.cursor >= len(f.matches) {
		return fileCandidate{}, false
	}
	return f.matches[f.cursor], true
}

// parseFileMentionInput returns the active @ token at the end of the composer.
// This intentionally completes the token being typed, not arbitrary older @mentions.
func parseFileMentionInput(value string) (bool, string) {
	if strings.Contains(value, "\n") {
		// Keep multiline editing predictable for this first pass.
		return false, ""
	}
	if value == "" {
		return false, ""
	}
	lastSpace := -1
	for i, r := range value {
		if unicode.IsSpace(r) {
			lastSpace = i
		}
	}
	token := value[lastSpace+1:]
	if !strings.HasPrefix(token, "@") || strings.Contains(token, "@@") {
		return false, ""
	}
	query := strings.TrimPrefix(token, "@")
	if strings.ContainsAny(query, "\"'`{}[]()") {
		return false, ""
	}
	return true, query
}

func replaceActiveFileMention(value, path string) string {
	ok, _ := parseFileMentionInput(value)
	if !ok {
		return value
	}
	lastSpace := -1
	for i, r := range value {
		if unicode.IsSpace(r) {
			lastSpace = i
		}
	}
	prefix := value[:lastSpace+1]
	return prefix + "@" + path + " "
}

func listFileCandidates(root, query string) []fileCandidate {
	root = filepath.Clean(root)
	query = filepath.ToSlash(strings.TrimSpace(query))
	var out []fileCandidate
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || skippedCompletionDirs[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip hidden files (e.g. .env, .DS_Store, .gitignore).
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if len(out) >= maxFileCompletionCandidates {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if fileCandidateMatches(rel, query) {
			out = append(out, fileCandidate{Path: rel})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		ai, aj := candidateRank(out[i].Path, query), candidateRank(out[j].Path, query)
		if ai != aj {
			return ai < aj
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func fileCandidateMatches(path, query string) bool {
	if query == "" {
		return true
	}
	p := strings.ToLower(path)
	q := strings.ToLower(filepath.ToSlash(query))
	if strings.Contains(p, q) {
		return true
	}
	parts := strings.Split(q, "/")
	pos := 0
	for _, part := range parts {
		if part == "" {
			continue
		}
		idx := strings.Index(p[pos:], part)
		if idx < 0 {
			return false
		}
		pos += idx + len(part)
	}
	return true
}

func candidateRank(path, query string) int {
	p := strings.ToLower(path)
	q := strings.ToLower(filepath.ToSlash(query))
	switch {
	case q == "":
		return strings.Count(path, "/")
	case strings.HasPrefix(p, q):
		return 0
	case strings.HasPrefix(filepath.Base(p), q):
		return 1
	case strings.Contains(p, q):
		return 2
	default:
		return 3
	}
}

func expandFileMentions(message, root string) (string, error) {
	mentions := extractFileMentions(message)
	if len(mentions) == 0 {
		return message, nil
	}

	seen := map[string]bool{}
	var blocks []string
	total := 0
	for _, mention := range mentions {
		if seen[mention] {
			continue
		}
		seen[mention] = true
		content, err := readMentionFile(root, mention)
		if err != nil {
			return "", err
		}
		if total+len(content) > maxMentionContextBytes {
			return "", fmt.Errorf("@%s exceeds the @file context limit", mention)
		}
		total += len(content)
		blocks = append(blocks, fmt.Sprintf("File: %s\n```\n%s\n```", mention, strings.TrimRight(content, "\n")))
	}
	return strings.TrimSpace(message) + "\n\n<context from @file mentions>\n" + strings.Join(blocks, "\n\n") + "\n</context>", nil
}

func extractFileMentions(message string) []string {
	fields := strings.Fields(message)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if !strings.HasPrefix(field, "@") || strings.HasPrefix(field, "@@") {
			continue
		}
		path := strings.TrimPrefix(field, "@")
		path = strings.TrimRight(path, ".,;:!?)]}")
		if path == "" || strings.Contains(path, "://") {
			continue
		}
		out = append(out, path)
	}
	return out
}

func readMentionFile(root, mention string) (string, error) {
	if filepath.IsAbs(mention) {
		return "", fmt.Errorf("@file paths must be relative: %s", mention)
	}
	clean := filepath.Clean(filepath.FromSlash(mention))
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("@file path escapes the workspace: %s", mention)
	}
	path := filepath.Join(root, clean)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read @%s: %w", mention, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("@%s is a directory; choose a file", mention)
	}
	if info.Size() > maxMentionFileBytes {
		return "", fmt.Errorf("@%s is too large for inline context", mention)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read @%s: %w", mention, err)
	}
	return string(data), nil
}
