package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/codeintel"
	"google.golang.org/adk/tool"
)

// EditFileArgs are the arguments for the edit_file tool.
type EditFileArgs struct {
	Path       string `json:"path" jsonschema:"Absolute path to the file to edit"`
	OldString  string `json:"old_string" jsonschema:"Text to find in the file. Exact string match by default or a regex pattern if regex is true"`
	NewString  string `json:"new_string" jsonschema:"Replacement text. For regex mode use $1 etc. to reference capture groups"`
	Regex      bool   `json:"regex,omitempty" jsonschema:"Treat old_string as a regular expression pattern (default false)"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"Replace all occurrences instead of just the first (default false)"`
}

// EditFileResult is the result of the edit_file tool.
type EditFileResult struct {
	Success             bool   `json:"success"`
	Path                string `json:"path"`
	Replacements        int    `json:"replacements"`
	Message             string `json:"message"`
	VerificationContext string `json:"verification_context,omitempty"`
}

// EditFile performs a find-and-replace operation on a file.
func EditFile(ctx tool.Context, args EditFileArgs) (EditFileResult, error) {
	if args.Path == "" {
		return EditFileResult{}, fmt.Errorf("path is required")
	}
	if args.OldString == "" {
		return EditFileResult{}, fmt.Errorf("old_string is required")
	}

	args.Path = expandPath(args.Path)

	// Must-read-before-edit guard: if the cache is active (has any read entries),
	// verify that this specific file has been read before allowing edits.
	// This prevents hallucinated edits where the LLM guesses file content.
	// The guard is lenient when no cache exists (e.g., in tests or first-time use).
	cache := LoadFileReadCache()
	if cache != nil && cache.HasAnyReadEntries() && !cache.HasReadEntry(args.Path) {
		return EditFileResult{
			Success: false,
			Path:    args.Path,
			Message: "You must read this file before editing it. Use read_file first.",
		}, nil
	}

	// Read the file
	data, err := os.ReadFile(args.Path)
	if err != nil {
		return EditFileResult{}, fmt.Errorf("failed to read file: %w", err)
	}
	content := string(data)

	var newContent string
	var replacements int

	if args.Regex {
		newContent, replacements, err = editFileRegex(content, args.OldString, args.NewString, args.ReplaceAll)
		if err != nil {
			return EditFileResult{}, err
		}
	} else {
		newContent, replacements, err = editFileExact(content, args.OldString, args.NewString, args.ReplaceAll)
		if err != nil {
			return EditFileResult{}, err
		}
	}

	// Write the modified content back
	if err := os.WriteFile(args.Path, []byte(newContent), 0644); err != nil {
		return EditFileResult{}, fmt.Errorf("failed to write file: %w", err)
	}

	// Build a unified-style hunk around the first edit site (pre-edit location).
	verificationCtx, editLine := buildVerificationContext(
		args.Path, content, newContent, args.OldString, args.NewString, args.Regex,
	)

	// Invalidate cache entries for this path
	if cache != nil {
		cache.InvalidatePath(args.Path)
		// Update with new mtime, source="edit"
		info, statErr := os.Stat(args.Path)
		if statErr == nil {
			lines := strings.Split(newContent, "\n")
			totalLines := len(lines)
			if totalLines > 0 && lines[totalLines-1] == "" {
				totalLines--
			}
			cache.Set(buildCacheKey(args.Path, 1, 0), CacheEntry{
				MtimeNs:    info.ModTime().UnixNano(),
				TotalLines: totalLines,
				Offset:     1,
				Limit:      0,
				Source:     "edit",
				Verified:   true,
			})
			cache.Save()
		}
	}
	if root, err := os.Getwd(); err == nil {
		codeintel.Invalidate(root)
	}

	msg := fmt.Sprintf("Replaced %d occurrence(s) in %s", replacements, args.Path)
	if editLine > 0 {
		msg = fmt.Sprintf("Replaced %d occurrence(s) in %s (line %d)", replacements, args.Path, editLine)
	}
	return EditFileResult{
		Success:             true,
		Path:                args.Path,
		Replacements:        replacements,
		Message:             msg,
		VerificationContext: verificationCtx,
	}, nil
}

// buildVerificationContext builds a compact unified-style hunk around the first
// replacement site. Location is always derived from the pre-edit file so
// deletions (empty newString) and short replacements that also appear earlier
// in the file still point at the correct region.
//
// Returns the hunk text and the 1-based start line of the edit (0 if unknown).
func buildVerificationContext(path, oldContent, newContent, oldString, newString string, isRegex bool) (string, int) {
	matchStart, removedText, addedText, ok := locateFirstEdit(oldContent, oldString, newString, isRegex)
	if !ok {
		return "", 0
	}

	oldLines := splitFileLines(oldContent)
	newLines := splitFileLines(newContent)
	removedLines := splitMatchLines(removedText)
	addedLines := splitMatchLines(addedText)

	// 0-based line index of the first character of the match.
	startLine0 := 0
	if matchStart > 0 {
		startLine0 = strings.Count(oldContent[:matchStart], "\n")
	}
	startLine1 := startLine0 + 1 // 1-based for display / header

	const contextRadius = 3
	const maxBodyLines = 30

	beforeStart := startLine0 - contextRadius
	if beforeStart < 0 {
		beforeStart = 0
	}
	// After-context in the new file begins just past the inserted lines.
	afterStartNew := startLine0 + len(addedLines)
	afterEndNew := afterStartNew + contextRadius
	if afterEndNew > len(newLines) {
		afterEndNew = len(newLines)
	}

	// Budget: prefer showing the change itself; trim context if needed.
	changeLines := len(removedLines) + len(addedLines)
	beforeBudget := contextRadius
	afterBudget := contextRadius
	if changeLines+beforeBudget+afterBudget > maxBodyLines {
		// Give leftover to context evenly; always keep the change when possible.
		remaining := maxBodyLines - changeLines
		if remaining < 0 {
			// Extremely large replacement: truncate change blocks below.
			beforeBudget = 0
			afterBudget = 0
		} else {
			beforeBudget = remaining / 2
			afterBudget = remaining - beforeBudget
		}
	}
	if startLine0-beforeStart > beforeBudget {
		beforeStart = startLine0 - beforeBudget
	}
	if afterEndNew-afterStartNew > afterBudget {
		afterEndNew = afterStartNew + afterBudget
	}

	name := filepath.Base(path)
	if name == "" || name == "." {
		name = path
	}

	var sb strings.Builder
	sb.WriteString("@@ ")
	sb.WriteString(name)
	sb.WriteByte(':')
	sb.WriteString(strconv.Itoa(startLine1))
	sb.WriteByte('\n')

	// Context before (old line numbers; unchanged lines share the same numbers).
	for i := beforeStart; i < startLine0 && i < len(oldLines); i++ {
		writeHunkLine(&sb, ' ', i+1, oldLines[i])
	}

	// Removed lines (old numbering).
	removedShown := removedLines
	addedShown := addedLines
	if changeLines > maxBodyLines {
		// Truncate large removals/additions while keeping a balanced sample.
		maxEach := maxBodyLines / 2
		if maxEach < 1 {
			maxEach = 1
		}
		if len(removedShown) > maxEach {
			removedShown = append(append([]string{}, removedShown[:maxEach]...), "…")
		}
		if len(addedShown) > maxEach {
			addedShown = append(append([]string{}, addedShown[:maxEach]...), "…")
		}
	}
	for i, line := range removedShown {
		if line == "…" {
			sb.WriteString("  …\n")
			continue
		}
		writeHunkLine(&sb, '-', startLine1+i, line)
	}

	// Added lines (new numbering starts at the same line).
	for i, line := range addedShown {
		if line == "…" {
			sb.WriteString("  …\n")
			continue
		}
		writeHunkLine(&sb, '+', startLine1+i, line)
	}

	// Context after (new line numbers post-edit).
	for i := afterStartNew; i < afterEndNew; i++ {
		writeHunkLine(&sb, ' ', i+1, newLines[i])
	}

	return strings.TrimRight(sb.String(), "\n"), startLine1
}

// locateFirstEdit finds the first replacement site in oldContent and returns
// the byte offset, the removed text, and the text that replaces it.
func locateFirstEdit(oldContent, oldString, newString string, isRegex bool) (matchStart int, removed, added string, ok bool) {
	if isRegex {
		re, err := regexp.Compile(oldString)
		if err != nil {
			return 0, "", "", false
		}
		loc := re.FindStringIndex(oldContent)
		if loc == nil {
			return 0, "", "", false
		}
		matched := oldContent[loc[0]:loc[1]]
		return loc[0], matched, re.ReplaceAllString(matched, newString), true
	}
	idx := strings.Index(oldContent, oldString)
	if idx < 0 {
		return 0, "", "", false
	}
	return idx, oldString, newString, true
}

func splitFileLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// splitMatchLines splits a match/replacement fragment into display lines.
// An empty string means no lines (pure insertion or pure deletion).
func splitMatchLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func writeHunkLine(sb *strings.Builder, marker rune, lineNum int, text string) {
	sb.WriteRune(marker)
	sb.WriteByte(' ')
	sb.WriteString(strconv.Itoa(lineNum))
	sb.WriteString("| ")
	sb.WriteString(text)
	sb.WriteByte('\n')
}

const verificationMaxBodyLines = 30

// buildWriteVerificationContext builds a compact unified-style hunk for write_file.
// Create: all '+' lines. Overwrite: '-' old lines then '+' new lines (both capped).
func buildWriteVerificationContext(path, oldContent, newContent string, existed bool) string {
	name := filepath.Base(path)
	if name == "" || name == "." {
		name = path
	}
	oldLines := splitFileLines(oldContent)
	newLines := splitFileLines(newContent)

	var sb strings.Builder
	if !existed || len(oldLines) == 0 {
		sb.WriteString("@@ ")
		sb.WriteString(name)
		sb.WriteString(":1 (created)\n")
		writeTruncatedHunkLines(&sb, '+', newLines, verificationMaxBodyLines)
		return strings.TrimRight(sb.String(), "\n")
	}

	sb.WriteString("@@ ")
	sb.WriteString(name)
	sb.WriteString(":1 (overwritten, ")
	sb.WriteString(strconv.Itoa(len(oldLines)))
	sb.WriteString("→")
	sb.WriteString(strconv.Itoa(len(newLines)))
	sb.WriteString(" lines)\n")

	// Split budget evenly between removals and additions.
	maxEach := verificationMaxBodyLines / 2
	if maxEach < 1 {
		maxEach = 1
	}
	writeTruncatedHunkLines(&sb, '-', oldLines, maxEach)
	writeTruncatedHunkLines(&sb, '+', newLines, maxEach)
	return strings.TrimRight(sb.String(), "\n")
}

// writeTruncatedHunkLines emits marker lines with 1-based numbering, appending
// a "…" row when lines exceeds maxLines.
func writeTruncatedHunkLines(sb *strings.Builder, marker rune, lines []string, maxLines int) {
	if maxLines < 1 {
		maxLines = 1
	}
	n := len(lines)
	show := n
	truncated := false
	if show > maxLines {
		show = maxLines
		truncated = true
	}
	for i := 0; i < show; i++ {
		writeHunkLine(sb, marker, i+1, lines[i])
	}
	if truncated {
		sb.WriteString("  …\n")
	}
}

// editFileExact performs exact string matching and replacement.
func editFileExact(content, oldString, newString string, replaceAll bool) (string, int, error) {
	count := strings.Count(content, oldString)
	if count == 0 {
		return "", 0, fmt.Errorf("old_string not found in file")
	}

	if count > 1 && !replaceAll {
		return "", 0, fmt.Errorf("found %d matches for old_string; set replace_all=true to replace all, or provide more context to match uniquely", count)
	}

	if replaceAll {
		return strings.ReplaceAll(content, oldString, newString), count, nil
	}

	// Replace first occurrence only
	result := strings.Replace(content, oldString, newString, 1)
	return result, 1, nil
}

// editFileRegex performs regex-based matching and replacement.
func editFileRegex(content, pattern, replacement string, replaceAll bool) (string, int, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", 0, fmt.Errorf("invalid regex pattern: %w", err)
	}

	matches := re.FindAllStringIndex(content, -1)
	count := len(matches)
	if count == 0 {
		return "", 0, fmt.Errorf("regex pattern matched no occurrences in file")
	}

	if count > 1 && !replaceAll {
		return "", 0, fmt.Errorf("regex matched %d occurrences; set replace_all=true to replace all, or refine the pattern", count)
	}

	if replaceAll {
		result := re.ReplaceAllString(content, replacement)
		return result, count, nil
	}

	// Replace first occurrence only
	firstMatch := re.FindStringIndex(content)
	if firstMatch == nil {
		return "", 0, fmt.Errorf("regex pattern matched no occurrences in file")
	}
	matched := content[firstMatch[0]:firstMatch[1]]
	replaced := re.ReplaceAllString(matched, replacement)
	result := content[:firstMatch[0]] + replaced + content[firstMatch[1]:]
	return result, 1, nil
}
