package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"google.golang.org/adk/tool"

	"github.com/SAP/astonish/pkg/tools/ripgrep"
)

// grepSearchTimeout bounds how long a single grep_search may run. Without it, a
// recursive search over a huge tree outside the working directory (e.g. the Go
// module cache ~/go/pkg/mod, node_modules) can hang the agent turn for minutes
// with no way to recover except a manual cancel. On timeout the search is
// killed and a clear, actionable error is returned.
const grepSearchTimeout = 25 * time.Second

// errGrepTimeout signals that the search exceeded grepSearchTimeout.
var errGrepTimeout = errors.New("grep_search timed out")

// GrepSearchArgs defines arguments for the grep_search tool
type GrepSearchArgs struct {
	Pattern       string   `json:"pattern" jsonschema:"The search pattern (literal string by default, or regex if regex=true)"`
	SearchPath    string   `json:"search_path,omitempty" jsonschema:"Directory or file to search (default: current dir)"`
	IncludeGlobs  []string `json:"include_globs,omitempty" jsonschema:"File patterns to include (e.g., '*.go', '*.js')"`
	CaseSensitive bool     `json:"case_sensitive,omitempty" jsonschema:"Case-sensitive search (default: false)"`
	MaxResults    int      `json:"max_results,omitempty" jsonschema:"Maximum total results to return (default: 50)"`
	Regex         bool     `json:"regex,omitempty" jsonschema:"Treat pattern as a regular expression (default: false, literal search)"`
	Glob          string   `json:"glob,omitempty" jsonschema:"Single glob filter for file paths (e.g., '*.ts', 'src/**/*.go')"`
	Type          string   `json:"type,omitempty" jsonschema:"Ripgrep type filter (e.g., 'go', 'ts', 'py'). Requires ripgrep."`
	Context       int      `json:"context,omitempty" jsonschema:"Number of context lines before and after each match (symmetric)"`
	BeforeContext int      `json:"before_context,omitempty" jsonschema:"Number of context lines before each match"`
	AfterContext  int      `json:"after_context,omitempty" jsonschema:"Number of context lines after each match"`
	Multiline     bool     `json:"multiline,omitempty" jsonschema:"Enable multiline matching (dot matches newline). Requires ripgrep."`
	HeadLimit     int      `json:"head_limit,omitempty" jsonschema:"Stop reading output after this many bytes (default: 5MB). Prevents huge outputs."`
}

// GrepMatch represents a single search match or context line
type GrepMatch struct {
	File       string `json:"file"`
	LineNumber int    `json:"line_number"`
	Content    string `json:"content"`
	Kind       string `json:"kind"` // "match" or "context"
}

// GrepSearchResult is the result returned by the grep_search tool
type GrepSearchResult struct {
	Matches         []GrepMatch `json:"matches"`
	Total           int         `json:"total"`
	Capped          bool        `json:"capped"`
	SearchIn        string      `json:"search_in"`
	TruncatedReason string      `json:"truncated_reason,omitempty"`
	PatternMode     string      `json:"pattern_mode"`
	DurationMs      int64       `json:"duration_ms"`
}

// ripgrepMatch represents a match from ripgrep's JSON output
type ripgrepMatch struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

// GrepSearch searches for text patterns in files using ripgrep.
func GrepSearch(ctx tool.Context, args GrepSearchArgs) (GrepSearchResult, error) {
	start := time.Now()

	// Set defaults
	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}

	headLimit := args.HeadLimit
	if headLimit <= 0 {
		headLimit = 5 * 1024 * 1024 // 5MB
	}

	searchPath := args.SearchPath
	if searchPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return GrepSearchResult{}, err
		}
		searchPath = cwd
	}

	// Make path absolute
	absPath, err := filepath.Abs(expandPath(searchPath))
	if err != nil {
		return GrepSearchResult{}, err
	}

	patternMode := "literal"
	if args.Regex {
		patternMode = "regex"
	}

	// Validate regex pattern early
	if args.Regex {
		if _, err := regexp.Compile(args.Pattern); err != nil {
			return GrepSearchResult{}, fmt.Errorf("invalid regex pattern: %w", err)
		}
	}

	// Bound the total search time so a runaway walk (e.g. over ~/go/pkg/mod)
	// cannot hang the turn.
	runCtx, cancel := context.WithTimeout(context.Background(), grepSearchTimeout)
	defer cancel()

	// Ripgrep is the only backend: it is gitignore-aware, fast, and supports
	// type filters, multiline, and context lines. It is guaranteed available by
	// pkg/tools/ripgrep (system rg or the pinned auto-provisioned build), so
	// there is no pure-Go fallback.
	matches, truncatedReason, err := tryRipgrep(runCtx, args, absPath, maxResults, headLimit)
	if err != nil {
		if errors.Is(err, errGrepTimeout) {
			return GrepSearchResult{}, grepTimeoutError(absPath)
		}
		return GrepSearchResult{}, err
	}

	capped := len(matches) >= maxResults
	if capped && truncatedReason == "" {
		truncatedReason = fmt.Sprintf("result limit reached (%d)", maxResults)
	}

	elapsed := time.Since(start).Milliseconds()

	return GrepSearchResult{
		Matches:         matches,
		Total:           len(matches),
		Capped:          capped,
		SearchIn:        absPath,
		TruncatedReason: truncatedReason,
		PatternMode:     patternMode,
		DurationMs:      elapsed,
	}, nil
}

// grepTimeoutError builds an actionable error for a timed-out search.
func grepTimeoutError(searchPath string) error {
	return fmt.Errorf("grep_search timed out after %s while scanning %q. "+
		"Narrow the search: set search_path to a specific directory or file, add include_globs/type to limit files, "+
		"or avoid scanning large trees outside the project (e.g. ~/go/pkg/mod, node_modules)",
		grepSearchTimeout, searchPath)
}

// tryRipgrep attempts to use ripgrep for searching
func tryRipgrep(ctx context.Context, args GrepSearchArgs, searchPath string, maxResults, headLimit int) ([]GrepMatch, string, error) {
	// Resolve rg: an installed rg on PATH, else the managed (auto-provisioned)
	// copy. This makes ripgrep effectively always available for code search.
	rgPath, err := ripgrep.ResolvePath()
	if err != nil {
		return nil, "", fmt.Errorf("ripgrep not found: %w", err)
	}

	// Build rg command
	rgArgs := []string{
		"--json",
		"--no-heading",
		"--max-filesize", "5M",
	}

	// Case sensitivity
	if !args.CaseSensitive {
		rgArgs = append(rgArgs, "--ignore-case")
	}

	// Pattern mode: literal (fixed strings) or regex
	if !args.Regex {
		rgArgs = append(rgArgs, "--fixed-strings")
	}

	// Multiline
	if args.Multiline {
		rgArgs = append(rgArgs, "--multiline", "--multiline-dotall")
	}

	// Type filter
	if args.Type != "" {
		rgArgs = append(rgArgs, "--type", args.Type)
	}

	// Context lines
	if args.Context > 0 {
		rgArgs = append(rgArgs, fmt.Sprintf("-C%d", args.Context))
	} else {
		if args.BeforeContext > 0 {
			rgArgs = append(rgArgs, fmt.Sprintf("-B%d", args.BeforeContext))
		}
		if args.AfterContext > 0 {
			rgArgs = append(rgArgs, fmt.Sprintf("-A%d", args.AfterContext))
		}
	}

	// Include globs (from both IncludeGlobs and Glob field)
	for _, glob := range args.IncludeGlobs {
		rgArgs = append(rgArgs, "--glob", glob)
	}
	if args.Glob != "" {
		rgArgs = append(rgArgs, "--glob", args.Glob)
	}

	// Add pattern and path
	rgArgs = append(rgArgs, args.Pattern, searchPath)

	cmd := exec.CommandContext(ctx, rgPath, rgArgs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", fmt.Errorf("ripgrep pipe setup failed: %w", err)
	}
	cmd.Stderr = nil // we'll check exit code instead

	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("ripgrep start failed: %w", err)
	}

	// Read up to headLimit bytes from stdout
	truncatedReason := ""
	limitedReader := &io.LimitedReader{R: stdout, N: int64(headLimit) + 1}
	output, _ := io.ReadAll(limitedReader)
	if len(output) > headLimit {
		output = output[:headLimit]
		truncatedReason = fmt.Sprintf("output truncated at %d bytes", headLimit)
	}

	// We must wait for the command to finish, but we can ignore errors
	// caused by the broken pipe from our early close
	waitErr := cmd.Wait()
	// If the context fired (deadline or cancel), CommandContext killed rg.
	// Surface a clear timeout so the agent narrows its search.
	if ctx.Err() != nil {
		return nil, "", errGrepTimeout
	}
	if waitErr != nil && truncatedReason == "" {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			switch exitErr.ExitCode() {
			case 1:
				// Exit code 1 = no matches found (not an error)
				return []GrepMatch{}, "", nil
			case 2:
				// Exit code 2 = error (invalid type, bad pattern, etc.)
				return nil, "", fmt.Errorf("ripgrep error (exit 2): check type/pattern validity")
			}
		}
		// If we truncated, the broken pipe is expected — don't error
		return nil, "", fmt.Errorf("ripgrep execution failed: %w", waitErr)
	}

	matches, err := parseRipgrepOutput(output, maxResults)
	return matches, truncatedReason, err
}

// parseRipgrepOutput parses ripgrep's JSON output, returning both match and context lines
func parseRipgrepOutput(output []byte, maxResults int) ([]GrepMatch, error) {
	var matches []GrepMatch
	matchCount := 0
	scanner := bufio.NewScanner(strings.NewReader(string(output)))

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var rg ripgrepMatch
		if err := json.Unmarshal([]byte(line), &rg); err != nil {
			continue
		}

		var kind string
		switch rg.Type {
		case "match":
			kind = "match"
			matchCount++
		case "context":
			kind = "context"
		default:
			continue
		}

		match := GrepMatch{
			File:       rg.Data.Path.Text,
			LineNumber: rg.Data.LineNumber,
			Content:    strings.TrimRight(rg.Data.Lines.Text, "\n\r"),
			Kind:       kind,
		}
		matches = append(matches, match)

		// Cap based on match count (context lines are free)
		if matchCount >= maxResults {
			break
		}
	}

	return matches, nil
}
