package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/codeintel"
	"github.com/SAP/astonish/pkg/config"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// --- Credential store file access protection ---

// protectedFileNames are filenames within the config directory that must never
// be accessed by LLM tools. These contain the encryption key and encrypted secrets.
var protectedFileNames = []string{".store_key", "credentials.enc"}

// isProtectedPath returns true if the given file path resolves to a protected
// credential store file inside the config directory.
func isProtectedPath(filePath string) bool {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return false
	}

	// Resolve to absolute and follow symlinks to prevent bypass via relative paths or links
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		resolvedPath = absPath
	}

	for _, name := range protectedFileNames {
		protectedPath := filepath.Join(configDir, name)
		resolvedProtected, err := filepath.EvalSymlinks(protectedPath)
		if err != nil {
			resolvedProtected = protectedPath
		}
		if resolvedPath == resolvedProtected || absPath == protectedPath {
			return true
		}
	}
	return false
}

// expandPath resolves ~ to the user's home directory. Go's os and filepath
// packages do not expand ~ (it's a shell feature), so LLM-provided paths
// like "~/snake/main.py" would fail without this. Only the leading "~/" or
// bare "~" is expanded; ~user syntax is not supported.
func expandPath(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// commandReferencesProtectedFile checks if a shell command string references
// protected credential store files. Returns true and the matched filename
// if found. This is a best-effort check for defense-in-depth — it catches
// common patterns like cat, cp, xxd, base64, etc.
func commandReferencesProtectedFile(command string) (bool, string) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return false, ""
	}

	for _, name := range protectedFileNames {
		// Full path match
		fullPath := filepath.Join(configDir, name)
		if strings.Contains(command, fullPath) {
			return true, name
		}
	}

	// Also check for the bare filename — catches cases where the command
	// uses cd or relative paths to reach the config dir
	for _, name := range protectedFileNames {
		if strings.Contains(command, name) {
			return true, name
		}
	}

	return false, ""
}

// --- Read File Tool ---

type ReadFileArgs struct {
	Path   string `json:"path" jsonschema:"Absolute path to the file to read"`
	Offset *int   `json:"offset,omitempty" jsonschema:"Line number to start reading from (1-indexed). Omit to start from line 1."`
	Limit  *int   `json:"limit,omitempty" jsonschema:"Maximum number of lines to read. Omit to read to end of file (large files are auto-capped; page with offset/limit)."`
	Force  bool   `json:"force,omitempty" jsonschema:"Bypass the read cache and always return file content even if unchanged since last read. Use when you no longer have the file content in your conversation history."`
}

// readFileSoftCapLines bounds how many lines an unbounded read_file returns for
// a large file. Reading whole large files repeatedly is the dominant cause of
// context bloat (and thus slow inference) in agentic coding loops, so an
// omitted Limit yields a paged window rather than the entire file. Callers can
// always page with offset/limit or pass an explicit large Limit.
const readFileSoftCapLines = 400

type ReadFileResult struct {
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	Range      string `json:"range,omitempty"`
	Unchanged  bool   `json:"unchanged,omitempty"`
}

func ReadFile(ctx tool.Context, args ReadFileArgs) (ReadFileResult, error) {
	args.Path = expandPath(args.Path)
	if isProtectedPath(args.Path) {
		return ReadFileResult{}, fmt.Errorf("access denied: this file is part of the credential store and cannot be read")
	}

	// Determine offset and limit
	offset := 1
	if args.Offset != nil && *args.Offset > 0 {
		offset = *args.Offset
	}
	limit := 0 // 0 means "read to end"
	if args.Limit != nil && *args.Limit > 0 {
		limit = *args.Limit
	}

	// Soft cap: when the caller did NOT request an explicit line limit, avoid
	// dumping an entire large file into the model's context (which bloats the
	// prompt and slows inference). Read a bounded window instead and let the
	// Range notice guide the model to page (offset/limit) or jump with
	// grep_search / code_definition. An explicit Limit always wins; a caller
	// that truly wants the whole file can pass a large Limit.
	softCapped := false
	if args.Limit == nil {
		if info, statErr := os.Stat(args.Path); statErr == nil {
			// Cheap size gate first (avoids line-counting tiny files); ~120 chars/line
			// heuristic keeps the check O(1) before the real line count below.
			if info.Size() > int64(readFileSoftCapLines*120) {
				limit = readFileSoftCapLines
				softCapped = true
			}
		}
	}

	// Check the read cache (mtime-based dedup)
	if !args.Force {
		cache := LoadFileReadCache()
		if cache != nil {
			cacheKey := buildCacheKey(args.Path, offset, limit)
			if entry, ok := cache.Get(cacheKey); ok && entry.Source == "read" {
				// stat the file to compare mtime
				info, err := os.Stat(args.Path)
				if err == nil {
					currentMtime := info.ModTime().UnixNano()
					if currentMtime == entry.MtimeNs {
						// Cache hit — file unchanged
						slog.Info("file_read_cache", "hit", true, "path", args.Path, "offset", offset, "limit", limit)
						rangeStr := ""
						if entry.TotalLines > 0 {
							if limit > 0 {
								end := offset + limit - 1
								if end > entry.TotalLines {
									end = entry.TotalLines
								}
								rangeStr = fmt.Sprintf("lines %d-%d of %d", offset, end, entry.TotalLines)
							} else {
								rangeStr = fmt.Sprintf("lines %d-%d of %d", offset, entry.TotalLines, entry.TotalLines)
							}
						}
						return ReadFileResult{
							Unchanged:  true,
							TotalLines: entry.TotalLines,
							Range:      rangeStr,
						}, nil
					}
					slog.Info("file_read_cache", "hit", false, "path", args.Path, "reason", "mtime_changed")
				}
			}
		}
	}

	// Read the file
	data, err := os.ReadFile(args.Path)
	if err != nil {
		return ReadFileResult{}, fmt.Errorf("failed to read file: %w", err)
	}
	content := string(data)

	// Split into lines
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	// If the file ends with a newline, the last element is empty — don't count it as a line
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		totalLines = len(lines) - 1
		lines = lines[:totalLines]
	}

	// Apply range
	startIdx := offset - 1 // convert to 0-indexed
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx > totalLines {
		startIdx = totalLines
	}
	endIdx := totalLines
	if limit > 0 {
		endIdx = startIdx + limit
		if endIdx > totalLines {
			endIdx = totalLines
		}
	}

	selectedLines := lines[startIdx:endIdx]

	// Add line numbers
	var sb strings.Builder
	for i, line := range selectedLines {
		lineNum := startIdx + i + 1 // 1-indexed
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(strconv.Itoa(lineNum))
		sb.WriteString(": ")
		sb.WriteString(line)
	}

	// Build range string
	rangeStr := ""
	if limit > 0 || offset > 1 {
		rangeStr = fmt.Sprintf("lines %d-%d of %d", startIdx+1, endIdx, totalLines)
	}
	// When we auto-capped an unbounded read, tell the model how to get more so
	// it pages or jumps instead of re-requesting the whole file.
	if softCapped && endIdx < totalLines {
		rangeStr = fmt.Sprintf("lines %d-%d of %d (auto-capped; this file is large. Read more with offset=%d, or use grep_search/code_definition to jump to the relevant lines instead of reading the whole file)",
			startIdx+1, endIdx, totalLines, endIdx+1)
	}

	// Update cache
	cache := LoadFileReadCache()
	if cache != nil {
		info, err := os.Stat(args.Path)
		if err == nil {
			cacheKey := buildCacheKey(args.Path, offset, limit)
			cache.Set(cacheKey, CacheEntry{
				MtimeNs:    info.ModTime().UnixNano(),
				TotalLines: totalLines,
				Offset:     offset,
				Limit:      limit,
				Source:     "read",
				Verified:   true,
			})
			cache.Save()
		}
	}

	return ReadFileResult{
		Content:    sb.String(),
		TotalLines: totalLines,
		Range:      rangeStr,
	}, nil
}

// --- Write File Tool ---

type WriteFileArgs struct {
	FilePath string `json:"file_path" jsonschema:"The path to the file where content will be written."`
	Content  string `json:"content" jsonschema:"The content to write to the file."`
}

type WriteFileResult struct {
	Message             string `json:"message"`
	Path                string `json:"path,omitempty"`
	Created             bool   `json:"created,omitempty"`
	VerificationContext string `json:"verification_context,omitempty"`
}

// tryExtractStdout attempts to extract content from an 'stdout' key if the input
// is a JSON or dict-like string (e.g., from shell_command output).
func tryExtractStdout(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", false
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return "", false
	}

	if stdout, ok := parsed["stdout"]; ok {
		switch v := stdout.(type) {
		case string:
			return v, true
		case []interface{}:
			var lines []string
			for _, item := range v {
				lines = append(lines, fmt.Sprintf("%v", item))
			}
			return strings.Join(lines, "\n"), true
		}
	}
	return "", false
}

func WriteFile(ctx tool.Context, args WriteFileArgs) (WriteFileResult, error) {
	args.FilePath = expandPath(args.FilePath)
	if isProtectedPath(args.FilePath) {
		return WriteFileResult{}, fmt.Errorf("access denied: this file is part of the credential store and cannot be modified")
	}

	// Try to extract stdout from JSON (for shell_command output)
	var finalContent string
	if extracted, ok := tryExtractStdout(args.Content); ok {
		finalContent = extracted
	} else {
		finalContent = args.Content
	}

	// Capture pre-write content so overwrite can return a unified-style hunk.
	var oldContent string
	existed := false
	if data, err := os.ReadFile(args.FilePath); err == nil {
		existed = true
		oldContent = string(data)
	} else if !os.IsNotExist(err) {
		// Permission or other read errors: still allow write, but treat as create for the hunk.
		existed = false
	}

	// Ensure parent directories exist
	if dir := filepath.Dir(args.FilePath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return WriteFileResult{}, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Write to file
	if err := os.WriteFile(args.FilePath, []byte(finalContent), 0644); err != nil {
		return WriteFileResult{}, fmt.Errorf("failed to write to file %s: %w", args.FilePath, err)
	}

	// Invalidate cache entries for this path and update with source="write"
	cache := LoadFileReadCache()
	if cache != nil {
		cache.InvalidatePath(args.FilePath)
		info, statErr := os.Stat(args.FilePath)
		if statErr == nil {
			lines := strings.Split(finalContent, "\n")
			totalLines := len(lines)
			if totalLines > 0 && lines[totalLines-1] == "" {
				totalLines--
			}
			cache.Set(buildCacheKey(args.FilePath, 1, 0), CacheEntry{
				MtimeNs:    info.ModTime().UnixNano(),
				TotalLines: totalLines,
				Offset:     1,
				Limit:      0,
				Source:     "write",
				Verified:   true,
			})
			cache.Save()
		}
	}
	if root, err := os.Getwd(); err == nil {
		codeintel.Invalidate(root)
	}

	created := !existed
	verificationCtx := buildWriteVerificationContext(args.FilePath, oldContent, finalContent, existed)
	msg := fmt.Sprintf("Overwrote %s", args.FilePath)
	if created {
		msg = fmt.Sprintf("Created %s", args.FilePath)
	}
	return WriteFileResult{
		Message:             msg,
		Path:                args.FilePath,
		Created:             created,
		VerificationContext: verificationCtx,
	}, nil
}

type ShellCommandArgs struct {
	Command    string `json:"command" jsonschema:"The shell command to execute"`
	Timeout    int    `json:"timeout,omitempty" jsonschema:"Timeout in seconds. Default 120. Max 3600."`
	WorkingDir string `json:"working_dir,omitempty" jsonschema:"Working directory for the command. Defaults to the current directory."`
	Background bool   `json:"background,omitempty" jsonschema:"If true, start the command in the background and return a session_id immediately. Use process_read/process_write/process_kill to interact with it."`
}

type ShellCommandResult struct {
	Stdout          string `json:"stdout"`
	TimedOut        bool   `json:"timed_out,omitempty"`
	WaitingForInput bool   `json:"waiting_for_input,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	ExitCode        *int   `json:"exit_code,omitempty"`
}

func ShellCommand(ctx tool.Context, args ShellCommandArgs) (ShellCommandResult, error) {
	// Block commands that reference credential store files
	if matched, fileName := commandReferencesProtectedFile(args.Command); matched {
		return ShellCommandResult{}, fmt.Errorf("access denied: command references credential store file '%s' which cannot be accessed", fileName)
	}

	// Mark all cache entries as unverified — shell commands may modify files.
	// The next read_file will re-stat to confirm before returning cached results.
	if cache := LoadFileReadCache(); cache != nil {
		cache.MarkAllUnverified()
		cache.Save()
	}

	// Apply timeout defaults and bounds
	timeout := args.Timeout
	if timeout <= 0 {
		timeout = 120
	}
	if timeout > 3600 {
		timeout = 3600
	}

	pm := GetProcessManager()

	// Start process with PTY
	sess, err := pm.Start(args.Command, args.WorkingDir, 24, 80)
	if err != nil {
		return ShellCommandResult{}, fmt.Errorf("failed to start command: %w", err)
	}

	// Background mode: return immediately with session ID
	if args.Background {
		// Wait briefly for initial output
		time.Sleep(300 * time.Millisecond)
		data := sess.Output.Bytes()
		return ShellCommandResult{
			Stdout:    string(data),
			SessionID: sess.ID,
		}, nil
	}

	// One-shot mode: wait for completion, cancellation, timeout, or interactive
	// input detection.
	var waitCtx context.Context
	if ctx != nil {
		// tool.Context embeds context.Context; carry it through for cancellation.
		waitCtx = ctx
	}
	return waitForShellSession(waitCtx, pm, sess, timeout)
}

// waitForShellSession blocks until the shell session exits, the context is
// cancelled, the timeout elapses, or the process is detected waiting for input.
// It is separated from ShellCommand so the wait/cancel behavior is unit-testable
// with a plain context.Context (ShellCommand's tool.Context embeds one).
func waitForShellSession(ctx context.Context, pm *ProcessManager, sess *ProcessSession, timeout int) (ShellCommandResult, error) {
	deadline := time.After(time.Duration(timeout) * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	// A nil context yields a nil channel; a nil channel in select blocks forever,
	// which correctly disables the cancellation case when no context is provided.
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}

	for {
		select {
		case <-ctxDone:
			// The turn was cancelled (e.g. the user pressed Esc). Kill the child
			// process so a stuck/long command does not keep running after the
			// turn ends, and return promptly.
			_ = pm.Kill(sess.ID)
			data := sess.Output.Bytes()
			err := context.Canceled
			if ctx != nil && ctx.Err() != nil {
				err = ctx.Err()
			}
			return ShellCommandResult{
				Stdout:    string(data),
				SessionID: sess.ID,
			}, err

		case <-sess.done:
			// Process exited — return output
			data := sess.Output.Bytes()
			result := ShellCommandResult{
				Stdout:   string(data),
				ExitCode: sess.ExitCode,
			}
			return result, nil

		case <-deadline:
			// Timeout — kill process and return what we have
			_ = pm.Kill(sess.ID)
			data := sess.Output.Bytes()
			return ShellCommandResult{
				Stdout:    string(data),
				TimedOut:  true,
				SessionID: sess.ID,
			}, nil

		case <-ticker.C:
			// Check if process is waiting for input (alive but idle)
			if sess.IsRunning() && sess.IdleDuration() >= idleThreshold {
				data := sess.Output.Bytes()
				if len(data) > 0 && looksLikePrompt(string(data)) {
					return ShellCommandResult{
						Stdout:          string(data),
						WaitingForInput: true,
						SessionID:       sess.ID,
					}, nil
				}
			}
		}
	}
}

// looksLikePrompt checks if the output ends with something that looks like
// an interactive prompt waiting for user input.
func looksLikePrompt(output string) bool {
	trimmed := strings.TrimRight(output, " \t")
	if len(trimmed) == 0 {
		return false
	}

	// Check common prompt endings (check both with and without trailing whitespace)
	promptSuffixes := []string{
		"?", ":", ">", "$", "#", "%",
		"(yes/no)", "(y/n)", "(Y/n)", "(y/N)",
		"[yes/no]", "[y/n]", "[Y/n]", "[y/N]",
		"(yes/no/[fingerprint])",
		"password:", "Password:", "PASSWORD:",
		"passphrase:", "Passphrase:",
	}

	lower := strings.ToLower(trimmed)
	for _, suffix := range promptSuffixes {
		if strings.HasSuffix(trimmed, suffix) || strings.HasSuffix(lower, strings.ToLower(suffix)) {
			return true
		}
	}

	// Check if last line is short (< 200 chars) and ends with a prompt character
	lines := strings.Split(trimmed, "\n")
	lastLine := strings.TrimSpace(lines[len(lines)-1])
	if len(lastLine) > 0 && len(lastLine) < 200 {
		lastChar := lastLine[len(lastLine)-1]
		if lastChar == '?' || lastChar == ':' || lastChar == '>' || lastChar == '$' || lastChar == '#' || lastChar == '%' {
			return true
		}
	}

	return false
}

// --- Filter JSON Tool ---

type FilterJsonArgs struct {
	JsonData        string   `json:"json_data" jsonschema:"The JSON data to filter, as a JSON string."`
	FieldsToExtract []string `json:"fields_to_extract" jsonschema:"A list of fields to extract. Use dot notation for nested fields."`
}

type FilterJsonResult struct {
	Result interface{} `json:"result"`
}

func getNestedValue(data interface{}, path []string) interface{} {
	current := data
	for _, key := range path {
		if m, ok := current.(map[string]interface{}); ok {
			if val, exists := m[key]; exists {
				current = val
			} else {
				return nil
			}
		} else if l, ok := current.([]interface{}); ok {
			// Try to parse key as index
			if idx, err := strconv.Atoi(key); err == nil {
				if idx >= 0 && idx < len(l) {
					current = l[idx]
				} else {
					return nil
				}
			} else {
				return nil
			}
		} else {
			return nil
		}
	}
	return current
}

func setNestedValue(data map[string]interface{}, path []string, value interface{}) {
	current := data
	for i := 0; i < len(path)-1; i++ {
		key := path[i]
		if _, exists := current[key]; !exists {
			current[key] = make(map[string]interface{})
		}
		if nextMap, ok := current[key].(map[string]interface{}); ok {
			current = nextMap
		} else {
			// Conflict: trying to treat a non-map as a map. Overwrite or abort?
			// Python implementation uses setdefault, which implies it expects a dict.
			// If it's not a dict, we can't proceed down this path.
			// For simplicity, we'll overwrite if it's not a map, matching Python's likely behavior of "last write wins" or structure enforcement.
			newMap := make(map[string]interface{})
			current[key] = newMap
			current = newMap
		}
	}
	current[path[len(path)-1]] = value
}

func filterItem(item interface{}, fields []string) interface{} {
	if m, ok := item.(map[string]interface{}); ok {
		result := make(map[string]interface{})
		for _, field := range fields {
			path := strings.Split(field, ".")
			value := getNestedValue(m, path)
			if value != nil {
				setNestedValue(result, path, value)
			}
		}
		return result
	} else if l, ok := item.([]interface{}); ok {
		resultList := make([]interface{}, 0, len(l))
		for _, subItem := range l {
			if _, isDict := subItem.(map[string]interface{}); isDict {
				resultList = append(resultList, filterItem(subItem, fields))
			}
		}
		return resultList
	}
	return item
}

func FilterJson(ctx tool.Context, args FilterJsonArgs) (FilterJsonResult, error) {
	var data interface{}

	// 1. Parse the JSON string
	var parsed interface{}
	if err := json.Unmarshal([]byte(args.JsonData), &parsed); err != nil {
		return FilterJsonResult{Result: fmt.Sprintf("Error: Invalid JSON input - %v", err)}, nil
	}

	// Check for 'stdout' wrapping
	if m, ok := parsed.(map[string]interface{}); ok {
		if stdout, exists := m["stdout"]; exists {
			// If stdout is a string, try to parse IT as JSON
			if stdoutStr, ok := stdout.(string); ok {
				var innerParsed interface{}
				if err := json.Unmarshal([]byte(stdoutStr), &innerParsed); err == nil {
					data = innerParsed
				} else {
					data = stdoutStr
				}
			} else {
				data = stdout
			}
		} else {
			data = parsed
		}
	} else {
		data = parsed
	}

	// 2. Validate data type
	if _, isMap := data.(map[string]interface{}); !isMap {
		if _, isList := data.([]interface{}); !isList {
			return FilterJsonResult{Result: "Error: Parsed data must be a JSON object or a list of JSON objects."}, nil
		}
	}

	// 3. Filter
	var result interface{}
	if l, ok := data.([]interface{}); ok {
		// Filter list of dicts
		filteredList := make([]interface{}, 0)
		for _, item := range l {
			if _, isDict := item.(map[string]interface{}); isDict {
				filteredList = append(filteredList, filterItem(item, args.FieldsToExtract))
			}
		}
		result = filteredList
	} else {
		// Filter single dict
		result = filterItem(data, args.FieldsToExtract)
	}

	return FilterJsonResult{Result: result}, nil
}

// --- Get Pull Request Files Tool ---

func GetInternalTools() ([]tool.Tool, error) {
	readFileTool, err := functiontool.New(functiontool.Config{
		Name:        "read_file",
		Description: "Read file contents with line numbers. Large files are auto-capped to a bounded window when no limit is given — page with offset/limit, or (preferred) use grep_search/code_definition to jump straight to the relevant lines instead of reading whole files.",
	}, ReadFile)
	if err != nil {
		return nil, err
	}

	writeFileTool, err := functiontool.New(functiontool.Config{
		Name:        "write_file",
		Description: "Write content to a file with intelligent 'stdout' extraction. If the content is the output from shell_command (containing 'stdout' key), it will extract and write only the stdout value.",
	}, WriteFile)
	if err != nil {
		return nil, err
	}

	shellCommandTool, err := functiontool.New(functiontool.Config{
		Name:        "shell_command",
		Description: `Execute a shell command with PTY support. Use for builds, tests, linters, git, package managers, CLIs, servers, and scripts. Do NOT use for browsing or searching source code — use file_tree, find_files, grep_search, or read_file instead. Returns stdout, exit_code. If the command waits for input, returns waiting_for_input=true with a session_id — use process_write to respond. Set background=true to start without waiting.`,
	}, ShellCommand)
	if err != nil {
		return nil, err
	}

	filterJsonTool, err := functiontool.New(functiontool.Config{
		Name:        "filter_json",
		Description: "Filters JSON data (either a single object or a list of objects) to include only a specified set of fields, supporting nested fields via dot notation.",
	}, FilterJson)
	if err != nil {
		return nil, err
	}

	gitDiffAddLineNumbersTool, err := functiontool.New(functiontool.Config{
		Name:        "git_diff_add_line_numbers",
		Description: "Parses a PR diff string or a patch snippet and adds line numbers to each line of change, returning the formatted result as a single string.",
	}, GitDiffAddLineNumbers)
	if err != nil {
		return nil, err
	}

	// Search tools
	fileTreeTool, err := functiontool.New(functiontool.Config{
		Name:        "file_tree",
		Description: "Get a budgeted directory tree for project orientation. Lists immediate children first, then recurses with budget limits. Large subtrees are automatically summarized with extension counts. Use max_depth, max_entries, max_output_chars to control output size. Returns JSON with paths, names, sizes, and optional subtree summaries.",
	}, FileTree)
	if err != nil {
		return nil, err
	}

	grepSearchTool, err := functiontool.New(functiontool.Config{
		Name:        "grep_search",
		Description: "Search for text/regex patterns in files. Default is literal search; set regex=true for regex. Supports context lines (context, before_context, after_context), file type filters (type='go'), glob filters, multiline mode, and case sensitivity. Returns file paths, line numbers, content, and match kind ('match' or 'context'). Uses ripgrep when available.",
	}, GrepSearch)
	if err != nil {
		return nil, err
	}

	findFilesTool, err := functiontool.New(functiontool.Config{
		Name:        "find_files",
		Description: "Find files by name/path pattern using glob matching (e.g., '*.go', 'src/**/*.ts'). Respects .gitignore when ripgrep is available. Supports sort_by='mtime' for newest-first ordering. Returns matching file paths with sizes.",
	}, FindFiles)
	if err != nil {
		return nil, err
	}

	editFileTool, err := functiontool.New(functiontool.Config{
		Name:        "edit_file",
		Description: "Edit a file by finding and replacing text. Supports exact string matching or regex patterns. Returns an error if old_string matches multiple locations without replace_all=true, so you can refine the match.",
	}, EditFile)
	if err != nil {
		return nil, err
	}

	codeIntelEnabled := true
	if appCfg, cfgErr := config.LoadAppConfig(); cfgErr == nil && appCfg != nil {
		codeIntelEnabled = appCfg.CodeIntel.IsEnabled()
		if appCfg.CodeIntel.LibraryPath != "" {
			// Prefer configured path over the hard-coded default; the loader
			// in pkg/codeintel reads ASTONISH_TREESITTER_LIB.
			_ = os.Setenv("ASTONISH_TREESITTER_LIB", appCfg.CodeIntel.LibraryPath)
		}
	}

	var codeIntelTools []tool.Tool
	if codeIntelEnabled {
		repoMapTool, err := functiontool.New(functiontool.Config{
			Name:        "repo_map",
			Description: "Build a structural map of supported source files using tree-sitter definitions and a reference graph. Use for orientation in unfamiliar repositories before broad edits. Supports Go, TypeScript/TSX, JavaScript/JSX, and Python.",
		}, codeintel.RepoMap)
		if err != nil {
			return nil, err
		}

		codeDefinitionTool, err := functiontool.New(functiontool.Config{
			Name:        "code_definition",
			Description: "Find structural definitions of a symbol in supported languages using tree-sitter. Prefer this over grep_search when locating a symbol declaration. Supports Go, TypeScript/TSX, JavaScript/JSX, and Python.",
		}, codeintel.CodeDefinition)
		if err != nil {
			return nil, err
		}

		codeReferencesTool, err := functiontool.New(functiontool.Config{
			Name:        "code_references",
			Description: "Find structural references to a symbol in supported languages using tree-sitter. Prefer this over grep_search before refactoring a symbol. Supports Go, TypeScript/TSX, JavaScript/JSX, and Python.",
		}, codeintel.CodeReferences)
		if err != nil {
			return nil, err
		}
		codeIntelTools = []tool.Tool{repoMapTool, codeDefinitionTool, codeReferencesTool}
	}

	webFetchTool, err := functiontool.New(functiontool.Config{
		Name:        "web_fetch",
		Description: "Fetch a URL and extract content as markdown, text, or HTML. Preferred tool for fetching specific URLs — try this first. Fast, free, no API key. If it returns empty content (JS-heavy pages), retry with browser tools or the configured MCP extract tool.",
	}, WebFetch)
	if err != nil {
		return nil, err
	}

	readPDFTool, err := functiontool.New(functiontool.Config{
		Name:        "read_pdf",
		Description: "Extract text content from a PDF file. Accepts a local file path or an HTTP/HTTPS URL. Returns plain text with page markers. Use this for reading PDF documents, reports, papers, etc.",
	}, ReadPDF)
	if err != nil {
		return nil, err
	}

	httpRequestTool, err := functiontool.New(functiontool.Config{
		Name:        "http_request",
		Description: `Make an HTTP request (GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS). Set 'credential' to a stored credential name for automatic auth header injection. JSON Content-Type is set automatically when body starts with { or [.`,
	}, HttpRequest)
	if err != nil {
		return nil, err
	}

	out := []tool.Tool{
		readFileTool, writeFileTool, shellCommandTool, filterJsonTool, gitDiffAddLineNumbersTool,
		fileTreeTool, grepSearchTool, findFilesTool, editFileTool,
	}
	out = append(out, codeIntelTools...)
	out = append(out, webFetchTool, readPDFTool, httpRequestTool)
	return out, nil
}

func ExecuteTool(ctx context.Context, name string, args map[string]interface{}, caller string) (any, error) {
	// Set the current caller for cache scoping. Safe because astonish node
	// dispatches sequentially (one tool call at a time).
	currentCaller = caller

	// Helper to marshal map to struct
	toStruct := func(input map[string]interface{}, target interface{}) error {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, target)
	}

	switch name {
	case "read_file":
		var toolArgs ReadFileArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for read_file: %w", err)
		}
		// We need a tool.Context. For now, we can pass a dummy or the real one if available.
		// But ReadFile expects tool.Context.
		// We can cast the passed context if it implements it, or create a wrapper.
		// Since we are calling from handleToolNode, we have a context.Context.
		// But ReadFile needs tool.Context.
		// Let's assume for now we can pass nil or a basic wrapper if the tool doesn't use it heavily.
		// ReadFile doesn't use ctx.
		// ShellCommand doesn't use ctx.
		return ReadFile(nil, toolArgs)

	case "write_file":
		var toolArgs WriteFileArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for write_file: %w", err)
		}
		return WriteFile(nil, toolArgs)

	case "shell_command":
		var toolArgs ShellCommandArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for shell_command: %w", err)
		}
		return ShellCommand(nil, toolArgs)

	case "filter_json":
		var toolArgs FilterJsonArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for filter_json: %w", err)
		}
		return FilterJson(nil, toolArgs)

	case "git_diff_add_line_numbers":
		var toolArgs GitDiffAddLineNumbersArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for git_diff_add_line_numbers: %w", err)
		}
		return GitDiffAddLineNumbers(nil, toolArgs)

	case "file_tree":
		var toolArgs FileTreeArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for file_tree: %w", err)
		}
		return FileTree(nil, toolArgs)

	case "grep_search":
		var toolArgs GrepSearchArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for grep_search: %w", err)
		}
		return GrepSearch(nil, toolArgs)

	case "find_files":
		var toolArgs FindFilesArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for find_files: %w", err)
		}
		return FindFiles(nil, toolArgs)

	case "edit_file":
		var toolArgs EditFileArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for edit_file: %w", err)
		}
		return EditFile(nil, toolArgs)

	case "repo_map":
		var toolArgs codeintel.RepoMapArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for repo_map: %w", err)
		}
		return codeintel.RepoMap(nil, toolArgs)

	case "code_definition":
		var toolArgs codeintel.CodeDefinitionArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for code_definition: %w", err)
		}
		return codeintel.CodeDefinition(nil, toolArgs)

	case "code_references":
		var toolArgs codeintel.CodeReferencesArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for code_references: %w", err)
		}
		return codeintel.CodeReferences(nil, toolArgs)

	case "web_fetch":
		var toolArgs WebFetchArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for web_fetch: %w", err)
		}
		return WebFetch(nil, toolArgs)

	case "read_pdf":
		var toolArgs ReadPDFArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for read_pdf: %w", err)
		}
		return ReadPDF(nil, toolArgs)

	case "process_read":
		var toolArgs ProcessReadArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for process_read: %w", err)
		}
		return processRead(nil, toolArgs)

	case "process_write":
		var toolArgs ProcessWriteArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for process_write: %w", err)
		}
		return processWrite(nil, toolArgs)

	case "process_list":
		var toolArgs ProcessListArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for process_list: %w", err)
		}
		return processList(nil, toolArgs)

	case "process_kill":
		var toolArgs ProcessKillArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for process_kill: %w", err)
		}
		return processKill(nil, toolArgs)

	case "http_request":
		var toolArgs HttpRequestArgs
		if err := toStruct(args, &toolArgs); err != nil {
			return nil, fmt.Errorf("invalid args for http_request: %w", err)
		}
		return httpRequest(ctx, toolArgs)

	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}
