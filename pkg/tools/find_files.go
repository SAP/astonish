package tools

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/adk/tool"

	"github.com/SAP/astonish/pkg/tools/ripgrep"
)

// FindFilesArgs defines arguments for the find_files tool
type FindFilesArgs struct {
	Pattern    string `json:"pattern" jsonschema:"Filename pattern to match (supports glob: *.go, test_*.py, src/**/*.ts)"`
	SearchPath string `json:"search_path,omitempty" jsonschema:"Directory to search from (default: current dir)"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"Maximum results to return (default: 100)"`
	SortBy     string `json:"sort_by,omitempty" jsonschema:"Sort order: 'path' (default) or 'mtime' (newest first)"`
	Recursive  bool   `json:"recursive,omitempty" jsonschema:"Descend into subdirectories (default false: list only the immediate children of search_path, like 'ls'). A pattern containing '/' or '**' forces recursion regardless of this flag."`
}

// FoundFile represents a matched file
type FoundFile struct {
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
	Size         int64  `json:"size"`
	IsDir        bool   `json:"is_dir"`
}

// FindFilesResult is the result returned by the find_files tool
type FindFilesResult struct {
	Files    []FoundFile `json:"files"`
	Total    int         `json:"total"`
	Capped   bool        `json:"capped"`
	SearchIn string      `json:"search_in"`
}

// FindFiles locates files by name pattern using glob matching
func FindFiles(ctx tool.Context, args FindFilesArgs) (FindFilesResult, error) {
	// Set defaults
	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}

	searchPath := args.SearchPath
	if searchPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return FindFilesResult{}, err
		}
		searchPath = cwd
	}

	// Make path absolute
	absPath, err := filepath.Abs(expandPath(searchPath))
	if err != nil {
		return FindFilesResult{}, err
	}

	// Verify path exists
	if _, err := os.Stat(absPath); err != nil {
		return FindFilesResult{}, err
	}

	// Determine effective recursion. Shallow (immediate-children) listing is the
	// default so a bare "list files in DIR" call behaves like `ls` and does not
	// flood with an entire subtree. A pattern that itself expresses depth ('/'
	// or '**') forces recursion regardless of the flag, since a shallow walk
	// could never satisfy it.
	patternImpliesRecursion := strings.Contains(args.Pattern, "/") || strings.Contains(args.Pattern, "**")
	recursive := args.Recursive || patternImpliesRecursion

	var files []FoundFile
	if !recursive {
		// Shallow: list only the immediate children (files AND dirs) of
		// searchPath whose basename matches the pattern.
		files, err = goListDir(args.Pattern, absPath, maxResults+1)
		if err != nil {
			return FindFilesResult{}, err
		}
	} else {
		// Recursive: try ripgrep --files first, fall back to the Go walk.
		files, err = tryRipgrepFiles(args.Pattern, absPath, maxResults+1)
		if err != nil {
			files, err = goFindFiles(args.Pattern, absPath, maxResults+1)
			if err != nil {
				return FindFilesResult{}, err
			}
		}
	}

	// Sort results
	if args.SortBy == "mtime" {
		sortFilesByMtime(files)
	} else {
		// Default: deterministic path sort
		sort.Slice(files, func(i, j int) bool {
			return files[i].Path < files[j].Path
		})
	}

	// Detect capping: we fetched max+1 to know if there are more
	capped := len(files) > maxResults
	if capped {
		files = files[:maxResults]
	}

	return FindFilesResult{
		Files:    files,
		Total:    len(files),
		Capped:   capped,
		SearchIn: absPath,
	}, nil
}

// tryRipgrepFiles uses rg --files with glob filtering for fast file finding
func tryRipgrepFiles(pattern, searchPath string, maxResults int) ([]FoundFile, error) {
	rgPath, err := ripgrep.ResolvePath()
	if err != nil {
		return nil, fmt.Errorf("ripgrep not found: %w", err)
	}

	// Build rg command: rg --files --glob <pattern> <path>
	args := []string{"--files"}

	// Convert pattern to glob.
	// - If it contains no path separators or **, wrap as **/<pattern> for basename matching
	// - If it contains a path separator (e.g., src/**/*.ts), use as-is but run from searchPath
	//   (ripgrep globs are relative to the working directory, not the path argument)
	globPattern := pattern
	hasPathSep := strings.Contains(pattern, "/")
	if !hasPathSep && !strings.Contains(pattern, "**") {
		globPattern = "**/" + pattern
	}
	args = append(args, "--glob", globPattern)

	// Add explicit exclusions for directories that rg might not exclude
	// (rg respects .gitignore but our defaultExclusions may go beyond that)
	for excl := range defaultExclusions {
		args = append(args, "--glob", "!"+excl+"/")
	}

	// If pattern contains path separators, run from the search directory
	// so ripgrep's relative glob matching works correctly
	var cmd *exec.Cmd
	if hasPathSep {
		args = append(args, ".")
		cmd = exec.Command(rgPath, args...)
		cmd.Dir = searchPath
	} else {
		args = append(args, searchPath)
		cmd = exec.Command(rgPath, args...)
	}

	output, err := cmd.Output()
	if err != nil {
		// Exit code 1 = no matches (not an error)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []FoundFile{}, nil
		}
		// Exit code 2 = error (e.g., bad glob)
		return nil, fmt.Errorf("rg --files failed: %w", err)
	}

	var files []FoundFile
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		p := scanner.Text()
		if p == "" {
			continue
		}

		// If we ran from searchPath with ".", paths are relative — make absolute
		if hasPathSep {
			p = filepath.Join(searchPath, p)
		}

		relPath, _ := filepath.Rel(searchPath, p)

		// Stat for size
		var size int64
		if info, err := os.Stat(p); err == nil {
			size = info.Size()
		}

		files = append(files, FoundFile{
			Path:         p,
			RelativePath: relPath,
			Size:         size,
			IsDir:        false, // rg --files only returns files
		})

		if len(files) >= maxResults {
			break
		}
	}

	return files, nil
}

// goFindFiles is the pure Go fallback for file finding
func goFindFiles(pattern, searchPath string, maxResults int) ([]FoundFile, error) {
	var files []FoundFile

	// Determine match strategy:
	// - Patterns with "**" or "/" need to match against relative paths
	// - Simple patterns (e.g., "*.go") match against basenames
	matchRelPath := strings.Contains(pattern, "/") || strings.Contains(pattern, "**")

	err := filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		// Skip excluded directories
		if info.IsDir() {
			name := info.Name()
			if defaultExclusions[name] {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if we've hit max results
		if len(files) >= maxResults {
			return filepath.SkipAll
		}

		var matched bool
		if matchRelPath {
			// Match against the relative path for patterns with ** or /
			relPath, _ := filepath.Rel(searchPath, path)
			matched = matchDoublestar(pattern, relPath)
		} else {
			// Match the pattern against the filename only
			name := info.Name()
			m, err := filepath.Match(pattern, name)
			if err != nil {
				// Invalid pattern, try case-insensitive match
				m, _ = filepath.Match(strings.ToLower(pattern), strings.ToLower(name))
			}
			matched = m
		}

		if matched {
			relPath, _ := filepath.Rel(searchPath, path)
			files = append(files, FoundFile{
				Path:         path,
				RelativePath: relPath,
				Size:         info.Size(),
				IsDir:        info.IsDir(),
			})
		}

		return nil
	})

	if err != nil && err != filepath.SkipAll {
		return nil, err
	}

	return files, nil
}

// goListDir lists only the immediate children (files AND directories) of dir
// whose basename matches pattern. This backs the shallow (non-recursive)
// default of find_files, giving it `ls`-like behavior for "what is in this
// directory" without descending into the whole subtree. Excluded directory
// names (defaultExclusions) are still filtered out. Matching is done against
// the basename with filepath.Match, falling back to a case-insensitive match
// for robustness (mirrors goFindFiles). An empty pattern matches everything.
func goListDir(pattern, dir string, maxResults int) ([]FoundFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var files []FoundFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() && defaultExclusions[name] {
			continue
		}

		if pattern != "" && pattern != "*" {
			m, matchErr := filepath.Match(pattern, name)
			if matchErr != nil {
				m, _ = filepath.Match(strings.ToLower(pattern), strings.ToLower(name))
			}
			if !m {
				continue
			}
		}

		full := filepath.Join(dir, name)
		var size int64
		if info, infoErr := entry.Info(); infoErr == nil {
			size = info.Size()
		}
		files = append(files, FoundFile{
			Path:         full,
			RelativePath: name,
			Size:         size,
			IsDir:        entry.IsDir(),
		})

		if len(files) >= maxResults {
			break
		}
	}

	return files, nil
}

// ** matches zero or more path segments. Each non-** segment is matched
// with filepath.Match semantics against the corresponding path segment.
func matchDoublestar(pattern, path string) bool {
	// Normalize separators
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	return doMatchDoublestar(pattern, path)
}

func doMatchDoublestar(pattern, path string) bool {
	for {
		if pattern == "" {
			return path == ""
		}
		if pattern == "**" || pattern == "**/" {
			// ** at end matches everything remaining
			return true
		}

		if strings.HasPrefix(pattern, "**/") {
			// ** matches zero or more path segments
			rest := pattern[3:]
			// Try matching rest against path at every segment boundary
			if doMatchDoublestar(rest, path) {
				return true
			}
			// Consume one path segment and retry
			slashIdx := strings.Index(path, "/")
			if slashIdx < 0 {
				// No more segments — try matching rest against the remaining path
				return doMatchDoublestar(rest, path)
			}
			path = path[slashIdx+1:]
			continue
		}

		// Extract the next pattern segment (up to next "/")
		var patSeg string
		nextSlash := strings.Index(pattern, "/")
		if nextSlash < 0 {
			// Last segment in pattern
			patSeg = pattern
			pattern = ""
		} else {
			patSeg = pattern[:nextSlash]
			pattern = pattern[nextSlash+1:]
		}

		// Extract the corresponding path segment
		pathSlash := strings.Index(path, "/")
		var pathSeg string
		if pathSlash < 0 {
			pathSeg = path
			path = ""
		} else {
			pathSeg = path[:pathSlash]
			path = path[pathSlash+1:]
		}

		// Match segments using filepath.Match (supports *, ?, [])
		m, err := filepath.Match(patSeg, pathSeg)
		if err != nil || !m {
			return false
		}
	}
}

// sortFilesByMtime sorts files by modification time, newest first
func sortFilesByMtime(files []FoundFile) {
	type fileWithMtime struct {
		file  FoundFile
		mtime int64
	}

	fwm := make([]fileWithMtime, len(files))
	for i, f := range files {
		var mtime int64
		if info, err := os.Stat(f.Path); err == nil {
			mtime = info.ModTime().UnixNano()
		}
		fwm[i] = fileWithMtime{file: f, mtime: mtime}
	}

	sort.Slice(fwm, func(i, j int) bool {
		return fwm[i].mtime > fwm[j].mtime // newest first
	})

	for i, f := range fwm {
		files[i] = f.file
	}
}
