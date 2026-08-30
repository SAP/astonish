package tools

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

func TestFileTree(t *testing.T) {
	// Create a temp directory structure for testing
	tmpDir, err := os.MkdirTemp("", "filetree_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test structure
	os.MkdirAll(filepath.Join(tmpDir, "src", "pkg"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "node_modules", "dep"), 0755) // Should be excluded
	os.MkdirAll(filepath.Join(tmpDir, ".git", "objects"), 0755)     // Should be excluded
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.go"), []byte("package src"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "pkg", "util.go"), []byte("package pkg"), 0644)

	t.Run("BasicFileTree", func(t *testing.T) {
		result, err := FileTree(nil, FileTreeArgs{
			Path:     tmpDir,
			MaxDepth: 3,
		})
		if err != nil {
			t.Fatalf("FileTree failed: %v", err)
		}

		if result.Root != tmpDir {
			t.Errorf("Expected root %s, got %s", tmpDir, result.Root)
		}

		// Check that node_modules is excluded
		for _, entry := range result.Entries {
			if entry.Name == "node_modules" {
				t.Error("node_modules should be excluded from tree")
			}
		}

		if result.Total == 0 {
			t.Error("Expected some files in tree")
		}
	})

	t.Run("DepthLimiting", func(t *testing.T) {
		result, err := FileTree(nil, FileTreeArgs{
			Path:     tmpDir,
			MaxDepth: 1,
		})
		if err != nil {
			t.Fatalf("FileTree failed: %v", err)
		}

		// With max depth 1, all entries should have depth 0
		for _, entry := range result.Entries {
			if entry.Depth >= 1 {
				t.Errorf("Depth limiting not working - found entry at depth %d", entry.Depth)
			}
		}
	})
}

func TestGrepSearch(t *testing.T) {
	// Create temp directory with test files
	tmpDir, err := os.MkdirTemp("", "grep_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test files
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello world\")\n}"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "util.go"), []byte("package main\n\nfunc helper() {\n\tfmt.Println(\"helper function\")\n}"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello world\nthis is a test\nhello again"), 0644)

	t.Run("BasicSearch", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    "hello",
			SearchPath: tmpDir,
		})
		if err != nil {
			t.Fatalf("GrepSearch failed: %v", err)
		}

		if result.Total == 0 {
			t.Error("Expected to find matches for 'hello'")
		}
	})

	t.Run("CaseSensitiveSearch", func(t *testing.T) {
		resultInsensitive, _ := GrepSearch(nil, GrepSearchArgs{
			Pattern:       "HELLO",
			SearchPath:    tmpDir,
			CaseSensitive: false,
		})

		resultSensitive, _ := GrepSearch(nil, GrepSearchArgs{
			Pattern:       "HELLO",
			SearchPath:    tmpDir,
			CaseSensitive: true,
		})

		if resultSensitive.Total >= resultInsensitive.Total && resultInsensitive.Total > 0 {
			t.Error("Case sensitive search should return fewer results than insensitive")
		}
	})

	t.Run("IncludeGlobs", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:      "hello",
			SearchPath:   tmpDir,
			IncludeGlobs: []string{"*.go"},
		})
		if err != nil {
			t.Fatalf("GrepSearch failed: %v", err)
		}

		// Should only find matches in .go files
		for _, match := range result.Matches {
			if filepath.Ext(match.File) != ".go" {
				t.Errorf("Found match in non-.go file: %s", match.File)
			}
		}
	})

	t.Run("MaxResults", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    "hello",
			SearchPath: tmpDir,
			MaxResults: 1,
		})
		if err != nil {
			t.Fatalf("GrepSearch failed: %v", err)
		}

		if result.Total > 1 {
			t.Errorf("MaxResults not respected: got %d results", result.Total)
		}
	})
}

func TestFindFiles(t *testing.T) {
	// Create temp directory with test files
	tmpDir, err := os.MkdirTemp("", "findfiles_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test structure
	os.MkdirAll(filepath.Join(tmpDir, "src", "pkg"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "node_modules"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "main_test.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.go"), []byte("package src"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app_test.go"), []byte("package src"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "node_modules", "index.js"), []byte("module.exports = {}"), 0644)

	t.Run("GlobPattern", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.go",
			SearchPath: tmpDir,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}

		if result.Total == 0 {
			t.Error("Expected to find .go files")
		}

		for _, file := range result.Files {
			if filepath.Ext(file.Path) != ".go" {
				t.Errorf("Found non-.go file: %s", file.Path)
			}
		}
	})

	t.Run("TestFilePattern", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*_test.go",
			SearchPath: tmpDir,
			Recursive:  true,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}

		if result.Total != 2 {
			t.Errorf("Expected 2 test files, got %d", result.Total)
		}
	})

	t.Run("ExcludesNodeModules", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.js",
			SearchPath: tmpDir,
			Recursive:  true,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}

		// node_modules should be excluded, so no .js files should be found
		if result.Total > 0 {
			t.Error("Should not find files in node_modules")
		}
	})

	t.Run("MaxResults", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.go",
			SearchPath: tmpDir,
			MaxResults: 2,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}

		if result.Total > 2 {
			t.Errorf("MaxResults not respected: got %d", result.Total)
		}
	})
}

// --- Upgraded grep_search tests ---

func TestGrepSearch_Regex(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "grep_regex_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n\nfunc helper() {\n\treturn\n}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "util.go"), []byte("package main\n\nfunc doWork(x int) error {\n\treturn nil\n}\n"), 0644)

	t.Run("RegexFindsPattern", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    `func\s+\w+`,
			SearchPath: tmpDir,
			Regex:      true,
		})
		if err != nil {
			t.Fatalf("GrepSearch regex failed: %v", err)
		}
		// Should find func main, func helper, func doWork
		matchCount := 0
		for _, m := range result.Matches {
			if m.Kind == "match" {
				matchCount++
			}
		}
		if matchCount < 3 {
			t.Errorf("Expected at least 3 regex matches, got %d", matchCount)
		}
		if result.PatternMode != "regex" {
			t.Errorf("Expected pattern_mode 'regex', got %q", result.PatternMode)
		}
	})

	t.Run("LiteralDoesNotInterpretRegexMeta", func(t *testing.T) {
		// Searching for literal "func\s+" should find nothing (no actual backslash-s in files)
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    `func\s+`,
			SearchPath: tmpDir,
			Regex:      false, // literal
		})
		if err != nil {
			t.Fatalf("GrepSearch literal failed: %v", err)
		}
		if result.Total != 0 {
			t.Errorf("Literal search for 'func\\s+' should find nothing, got %d", result.Total)
		}
		if result.PatternMode != "literal" {
			t.Errorf("Expected pattern_mode 'literal', got %q", result.PatternMode)
		}
	})

	t.Run("InvalidRegexReturnsError", func(t *testing.T) {
		_, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    `[invalid`,
			SearchPath: tmpDir,
			Regex:      true,
		})
		if err == nil {
			t.Error("Expected error for invalid regex pattern")
		}
	})

	t.Run("NoMatchesReturnsEmpty", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    "zzz_nonexistent_zzz",
			SearchPath: tmpDir,
		})
		if err != nil {
			t.Fatalf("GrepSearch failed: %v", err)
		}
		if result.Total != 0 {
			t.Errorf("Expected 0 results, got %d", result.Total)
		}
	})

	t.Run("DurationMsReturned", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    "func",
			SearchPath: tmpDir,
		})
		if err != nil {
			t.Fatalf("GrepSearch failed: %v", err)
		}
		if result.DurationMs < 0 {
			t.Errorf("Expected non-negative duration_ms, got %d", result.DurationMs)
		}
	})
}

func requireRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) not installed")
	}
}

func TestGrepSearch_Context(t *testing.T) {
	requireRipgrep(t)
	tmpDir, err := os.MkdirTemp("", "grep_context_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a file with known content for context testing
	content := "line1\nline2\nline3\nTARGET\nline5\nline6\nline7\n"
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte(content), 0644)

	t.Run("ContextLinesReturned", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    "TARGET",
			SearchPath: tmpDir,
			Context:    2,
		})
		if err != nil {
			t.Fatalf("GrepSearch context failed: %v", err)
		}

		// Should have the match plus context lines
		hasMatch := false
		hasContext := false
		for _, m := range result.Matches {
			if m.Kind == "match" {
				hasMatch = true
			}
			if m.Kind == "context" {
				hasContext = true
			}
		}
		if !hasMatch {
			t.Error("Expected at least one 'match' kind entry")
		}
		if !hasContext {
			t.Error("Expected at least one 'context' kind entry")
		}
	})
}

func TestGrepSearch_GlobFilter(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "grep_glob_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("hello world"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello world"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "data.json"), []byte(`{"hello": "world"}`), 0644)

	t.Run("SingleGlobFilter", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    "hello",
			SearchPath: tmpDir,
			Glob:       "*.go",
		})
		if err != nil {
			t.Fatalf("GrepSearch glob failed: %v", err)
		}
		for _, m := range result.Matches {
			if m.Kind == "match" && filepath.Ext(m.File) != ".go" {
				t.Errorf("Glob filter not working: found match in %s", m.File)
			}
		}
	})

	t.Run("MaxResultsCapsTotal", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    "hello",
			SearchPath: tmpDir,
			MaxResults: 1,
		})
		if err != nil {
			t.Fatalf("GrepSearch failed: %v", err)
		}
		matchCount := 0
		for _, m := range result.Matches {
			if m.Kind == "match" {
				matchCount++
			}
		}
		if matchCount > 1 {
			t.Errorf("MaxResults should cap total matches to 1, got %d", matchCount)
		}
	})
}

// --- Upgraded find_files tests ---

func TestFindFiles_RecursiveGlob(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "findfiles_recursive_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	os.MkdirAll(filepath.Join(tmpDir, "src", "pkg"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "node_modules", "dep"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.go"), []byte("package src"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "pkg", "deep.go"), []byte("package pkg"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "style.css"), []byte("body{}"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "node_modules", "dep", "index.js"), []byte("module.exports={}"), 0644)

	t.Run("StarGo", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.go",
			SearchPath: tmpDir,
			Recursive:  true,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		// Should find main.go, app.go, deep.go (3 files)
		if result.Total < 3 {
			t.Errorf("Expected at least 3 .go files, got %d", result.Total)
		}
		for _, f := range result.Files {
			if filepath.Ext(f.Path) != ".go" {
				t.Errorf("Found non-.go file: %s", f.Path)
			}
		}
	})

	t.Run("ExcludesNodeModules", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.js",
			SearchPath: tmpDir,
			Recursive:  true,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		if result.Total > 0 {
			t.Errorf("Should not find files in node_modules, got %d", result.Total)
		}
	})

	t.Run("MaxResultsCaps", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.go",
			SearchPath: tmpDir,
			MaxResults: 2,
			Recursive:  true,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		if result.Total > 2 {
			t.Errorf("MaxResults not respected: got %d", result.Total)
		}
	})

	t.Run("DefaultMaxIs100", func(t *testing.T) {
		// Just ensure the default is > 50 (was 50, now 100)
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.go",
			SearchPath: tmpDir,
			Recursive:  true,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		// With 3 files and default max 100, should not be capped
		if result.Capped {
			t.Error("Should not be capped with only 3 files and max 100")
		}
	})

	t.Run("PatternWithDoublestarForcesRecursion", func(t *testing.T) {
		// A pattern containing ** must descend even without Recursive:true.
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "**/*.go",
			SearchPath: tmpDir,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		if result.Total < 3 {
			t.Errorf("Expected ** pattern to recurse and find >=3 .go files, got %d", result.Total)
		}
	})
}

// TestFindFiles_ShallowDefault verifies that find_files lists ONLY the
// immediate children of search_path by default (no recursion), so a bare "list
// this directory" call behaves like `ls` instead of flooding with the whole
// subtree. This is the fix for the recursion-by-default regression.
func TestFindFiles_ShallowDefault(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "findfiles_shallow_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	os.MkdirAll(filepath.Join(tmpDir, "src", "pkg"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# hi"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.go"), []byte("package src"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "pkg", "deep.go"), []byte("package pkg"), 0644)

	t.Run("StarListsImmediateChildrenOnly", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*",
			SearchPath: tmpDir,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		// Expect exactly the 3 top-level entries: main.go, README.md, src/ (dir).
		if result.Total != 3 {
			names := make([]string, len(result.Files))
			for i, f := range result.Files {
				names[i] = f.RelativePath
			}
			t.Fatalf("Expected 3 immediate children, got %d: %v", result.Total, names)
		}
		var sawDir bool
		for _, f := range result.Files {
			// No entry should be nested (relative path must be a bare basename).
			if strings.Contains(f.RelativePath, string(filepath.Separator)) {
				t.Errorf("Shallow listing returned nested entry: %s", f.RelativePath)
			}
			if f.RelativePath == "src" && f.IsDir {
				sawDir = true
			}
		}
		if !sawDir {
			t.Error("Shallow listing should include the immediate child directory 'src'")
		}
	})

	t.Run("ShallowStarGoTopLevelOnly", func(t *testing.T) {
		// *.go shallow should match only main.go at the top level, NOT the
		// nested app.go / deep.go.
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.go",
			SearchPath: tmpDir,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		if result.Total != 1 {
			t.Fatalf("Expected exactly 1 top-level .go file, got %d", result.Total)
		}
		if result.Files[0].RelativePath != "main.go" {
			t.Errorf("Expected main.go, got %s", result.Files[0].RelativePath)
		}
	})

	t.Run("RecursiveTrueDescends", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.go",
			SearchPath: tmpDir,
			Recursive:  true,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		if result.Total < 3 {
			t.Errorf("Recursive should find >=3 .go files, got %d", result.Total)
		}
	})
}

func TestFindFiles_SortByMtime(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "findfiles_mtime_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create files and set explicit mtimes to ensure deterministic ordering
	oldFile := filepath.Join(tmpDir, "old.go")
	newFile := filepath.Join(tmpDir, "new.go")
	os.WriteFile(oldFile, []byte("old"), 0644)
	os.WriteFile(newFile, []byte("new"), 0644)

	// Set old.go to be 10 seconds in the past
	oldTime := time.Now().Add(-10 * time.Second)
	os.Chtimes(oldFile, oldTime, oldTime)

	result, err := FindFiles(nil, FindFilesArgs{
		Pattern:    "*.go",
		SearchPath: tmpDir,
		SortBy:     "mtime",
	})
	if err != nil {
		t.Fatalf("FindFiles failed: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("Expected 2 files, got %d", result.Total)
	}
	// The "new.go" should be first (newest)
	if !strings.Contains(result.Files[0].Path, "new.go") {
		t.Errorf("Expected newest file first with mtime sort, got %s", result.Files[0].Path)
	}
}

// --- Upgraded file_tree tests ---

func TestFileTree_Budgeted(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filetree_budget_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a large subtree
	bigDir := filepath.Join(tmpDir, "bigdir")
	os.MkdirAll(bigDir, 0755)
	for i := 0; i < 100; i++ {
		os.WriteFile(filepath.Join(bigDir, fmt.Sprintf("file%03d.go", i)), []byte("package big"), 0644)
	}
	// Create a small sibling
	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Hello"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "small"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "small", "one.go"), []byte("package small"), 0644)

	t.Run("MaxEntriesCaps", func(t *testing.T) {
		result, err := FileTree(nil, FileTreeArgs{
			Path:       tmpDir,
			MaxDepth:   3,
			MaxEntries: 10,
		})
		if err != nil {
			t.Fatalf("FileTree failed: %v", err)
		}
		if result.Total > 10 {
			t.Errorf("MaxEntries not respected: got %d entries", result.Total)
		}
	})

	t.Run("SummarizeLargeSubtree", func(t *testing.T) {
		result, err := FileTree(nil, FileTreeArgs{
			Path:     tmpDir,
			MaxDepth: 3,
		})
		if err != nil {
			t.Fatalf("FileTree failed: %v", err)
		}
		// bigdir should be summarized because it has > 50 files
		foundSummary := false
		for _, entry := range result.Entries {
			if entry.Name == "bigdir" && entry.Truncated && entry.Summary != "" {
				foundSummary = true
				if entry.FileCount != 100 {
					t.Errorf("Expected FileCount=100, got %d", entry.FileCount)
				}
				if !strings.Contains(entry.Summary, ".go") {
					t.Errorf("Summary should mention .go extension, got %q", entry.Summary)
				}
				break
			}
		}
		if !foundSummary {
			t.Error("Expected bigdir to be summarized with extension counts")
		}
	})

	t.Run("ImmediateChildrenNotStarved", func(t *testing.T) {
		result, err := FileTree(nil, FileTreeArgs{
			Path:       tmpDir,
			MaxDepth:   3,
			MaxEntries: 500,
		})
		if err != nil {
			t.Fatalf("FileTree failed: %v", err)
		}
		// All immediate children (bigdir, README.md, small) should be present
		names := make(map[string]bool)
		for _, entry := range result.Entries {
			if entry.Depth == 0 {
				names[entry.Name] = true
			}
		}
		for _, expected := range []string{"bigdir", "README.md", "small"} {
			if !names[expected] {
				t.Errorf("Expected immediate child %q to be present", expected)
			}
		}
	})

	t.Run("MaxDepthCompatibility", func(t *testing.T) {
		// MaxDepth 1 means only depth 0 entries (same as before)
		result, err := FileTree(nil, FileTreeArgs{
			Path:     tmpDir,
			MaxDepth: 1,
		})
		if err != nil {
			t.Fatalf("FileTree failed: %v", err)
		}
		for _, entry := range result.Entries {
			if entry.Depth >= 1 {
				t.Errorf("MaxDepth=1 should only show depth 0, found depth %d", entry.Depth)
			}
		}
	})

	t.Run("SummarizeDisabled", func(t *testing.T) {
		noSummarize := false
		result, err := FileTree(nil, FileTreeArgs{
			Path:       tmpDir,
			MaxDepth:   3,
			MaxEntries: 500,
			Summarize:  &noSummarize,
		})
		if err != nil {
			t.Fatalf("FileTree failed: %v", err)
		}
		// With summarize disabled, bigdir's children should be listed
		for _, entry := range result.Entries {
			if entry.Truncated {
				t.Error("No entries should be truncated when summarize=false")
			}
		}
	})
}

func TestFileTree_MaxOutputChars(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filetree_chars_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create many files with long names
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("a_very_long_filename_for_testing_char_budget_%03d.go", i)
		os.WriteFile(filepath.Join(tmpDir, name), []byte("package x"), 0644)
	}

	result, err := FileTree(nil, FileTreeArgs{
		Path:           tmpDir,
		MaxDepth:       2,
		MaxOutputChars: 500, // very small budget
	})
	if err != nil {
		t.Fatalf("FileTree failed: %v", err)
	}
	// Should be truncated before listing all 50 files
	if result.Total >= 50 {
		t.Errorf("Expected char budget to cap output, got %d entries", result.Total)
	}
	if result.TruncatedReason == "" {
		t.Error("Expected TruncatedReason to be set when budget is exceeded")
	}
}

func TestGrepSearch_TypeFilter(t *testing.T) {
	requireRipgrep(t)
	tmpDir, err := os.MkdirTemp("", "grep_type_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("hello from go"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "app.py"), []byte("hello from python"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "style.css"), []byte("hello from css"), 0644)

	t.Run("TypeGoOnlyReturnsGoFiles", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    "hello",
			SearchPath: tmpDir,
			Type:       "go",
		})
		if err != nil {
			t.Fatalf("GrepSearch type filter failed: %v", err)
		}
		for _, m := range result.Matches {
			if m.Kind == "match" && filepath.Ext(m.File) != ".go" {
				t.Errorf("Type filter 'go' should only return .go files, got %s", m.File)
			}
		}
		if result.Total == 0 {
			t.Error("Expected at least one match with type=go")
		}
	})

	t.Run("TypePyOnlyReturnsPyFiles", func(t *testing.T) {
		result, err := GrepSearch(nil, GrepSearchArgs{
			Pattern:    "hello",
			SearchPath: tmpDir,
			Type:       "py",
		})
		if err != nil {
			t.Fatalf("GrepSearch type filter failed: %v", err)
		}
		for _, m := range result.Matches {
			if m.Kind == "match" && filepath.Ext(m.File) != ".py" {
				t.Errorf("Type filter 'py' should only return .py files, got %s", m.File)
			}
		}
		if result.Total == 0 {
			t.Error("Expected at least one match with type=py")
		}
	})
}

func TestFindFiles_GitignoreRespected(t *testing.T) {
	requireRipgrep(t)
	tmpDir, err := os.MkdirTemp("", "findfiles_gitignore_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize a real git repo so rg activates gitignore
	exec.Command("git", "init", tmpDir).Run()
	os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("ignored_dir/\nbuild_output/\n"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "ignored_dir"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "build_output"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "ignored_dir", "secret.go"), []byte("package secret"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "build_output", "bundle.go"), []byte("package bundle"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "app.go"), []byte("package main"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "src"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "src", "lib.go"), []byte("package src"), 0644)

	t.Run("GitignoredDirsExcluded", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.go",
			SearchPath: tmpDir,
			Recursive:  true,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		// Should find app.go and src/lib.go but NOT ignored_dir/secret.go or build_output/bundle.go
		for _, f := range result.Files {
			if strings.Contains(f.Path, "ignored_dir") {
				t.Errorf("File in .gitignored directory should not appear: %s", f.Path)
			}
			if strings.Contains(f.Path, "build_output") {
				t.Errorf("File in .gitignored directory should not appear: %s", f.Path)
			}
		}
		if result.Total < 2 {
			t.Errorf("Expected at least app.go and src/lib.go, got %d files", result.Total)
		}
	})
}

func TestFindFiles_NestedGlob(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "findfiles_nested_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	os.MkdirAll(filepath.Join(tmpDir, "src", "components"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "lib"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.ts"), []byte("export default {}"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "components", "Button.tsx"), []byte("export const Button = ()=>{}"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "lib", "util.ts"), []byte("export function f(){}"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Project"), 0644)

	t.Run("DoubleStarPattern", func(t *testing.T) {
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "src/**/*.ts",
			SearchPath: tmpDir,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		// Should find src/app.ts (*.ts matches .ts but not .tsx with this glob)
		found := false
		for _, f := range result.Files {
			if strings.Contains(f.RelativePath, "src") && strings.HasSuffix(f.Path, ".ts") {
				found = true
			}
			if strings.Contains(f.Path, "lib") {
				t.Errorf("Pattern src/**/*.ts should not match lib/: %s", f.Path)
			}
		}
		if !found {
			t.Error("Expected to find src/app.ts with pattern src/**/*.ts")
		}
	})
}

func TestFindFiles_GoFallbackDoublestar(t *testing.T) {
	// Tests the Go fallback (goFindFiles) directly with ** patterns.
	// This is the code path hit when rg is unavailable in the sandbox.
	tmpDir, err := os.MkdirTemp("", "findfiles_doublestar_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create nested structure
	os.MkdirAll(filepath.Join(tmpDir, "pkg", "tools"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "pkg", "agent"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "cmd"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "pkg", "tools", "grep_search.go"), []byte("package tools"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "pkg", "tools", "find_files.go"), []byte("package tools"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "pkg", "tools", "file_tree.go"), []byte("package tools"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "pkg", "agent", "agent.go"), []byte("package agent"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "cmd", "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# test"), 0644)

	tests := []struct {
		name      string
		pattern   string
		wantMin   int
		wantMatch string // at least one result must contain this substring
		wantNo    string // no result should contain this substring (empty = no check)
	}{
		{
			name:      "DoubleStarPrefix_SpecificFile",
			pattern:   "**/grep_search*.go",
			wantMin:   1,
			wantMatch: "grep_search.go",
		},
		{
			name:      "DoubleStarPrefix_AllGo",
			pattern:   "**/*.go",
			wantMin:   5, // all 5 .go files
			wantMatch: ".go",
		},
		{
			name:      "PathWithDoubleStar",
			pattern:   "pkg/**/*.go",
			wantMin:   4, // 3 in tools + 1 in agent
			wantMatch: "pkg/",
			wantNo:    "cmd/",
		},
		{
			name:      "PathSegmentGlob",
			pattern:   "pkg/tools/*.go",
			wantMin:   3,
			wantMatch: "pkg/tools/",
			wantNo:    "agent",
		},
		{
			name:      "DoubleStarPrefix_GlobSuffix",
			pattern:   "**/tools/*.go",
			wantMin:   3,
			wantMatch: "tools/",
			wantNo:    "agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := goFindFiles(tt.pattern, tmpDir, 100)
			if err != nil {
				t.Fatalf("goFindFiles(%q) error: %v", tt.pattern, err)
			}
			if len(result) < tt.wantMin {
				t.Errorf("goFindFiles(%q) returned %d results, want >= %d", tt.pattern, len(result), tt.wantMin)
				for _, f := range result {
					t.Logf("  got: %s", f.RelativePath)
				}
			}
			if tt.wantMatch != "" {
				found := false
				for _, f := range result {
					if strings.Contains(f.RelativePath, tt.wantMatch) || strings.Contains(f.Path, tt.wantMatch) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("goFindFiles(%q): no result contains %q", tt.pattern, tt.wantMatch)
				}
			}
			if tt.wantNo != "" {
				for _, f := range result {
					if strings.Contains(f.RelativePath, tt.wantNo) {
						t.Errorf("goFindFiles(%q): result %q should not contain %q", tt.pattern, f.RelativePath, tt.wantNo)
					}
				}
			}
		})
	}
}

// TestGrepTimeoutError_Actionable checks the user-facing timeout message guides
// the model to narrow its search.
func TestGrepTimeoutError_Actionable(t *testing.T) {
	msg := grepTimeoutError("/Users/x/go/pkg/mod").Error()
	for _, want := range []string{"timed out", "search_path", "include_globs"} {
		if !strings.Contains(msg, want) {
			t.Errorf("timeout error missing %q: %s", want, msg)
		}
	}
}

func TestMatchDoublestar(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.go", "pkg/tools/grep.go", true},
		{"**/*.go", "main.go", true},
		{"**/*.go", "deep/nested/path/file.go", true},
		{"**/*.go", "file.txt", false},
		{"**/grep*.go", "pkg/tools/grep_search.go", true},
		{"**/grep*.go", "grep.go", true},
		{"**/grep*.go", "pkg/tools/find_files.go", false},
		{"pkg/**/*.go", "pkg/tools/grep.go", true},
		{"pkg/**/*.go", "pkg/agent/agent.go", true},
		{"pkg/**/*.go", "cmd/main.go", false},
		{"pkg/tools/*.go", "pkg/tools/grep.go", true},
		{"pkg/tools/*.go", "pkg/agent/agent.go", false},
		{"**/tools/*.go", "pkg/tools/grep.go", true},
		{"**/tools/*.go", "a/b/tools/x.go", true},
		{"**/tools/*.go", "pkg/agent/agent.go", false},
		{"src/**/*.ts", "src/app.ts", true},
		{"src/**/*.ts", "src/components/Button.ts", true},
		{"src/**/*.ts", "lib/util.ts", false},
		{"**", "anything/at/all.txt", true},
		{"**", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.path, func(t *testing.T) {
			got := matchDoublestar(tt.pattern, tt.path)
			if got != tt.want {
				t.Errorf("matchDoublestar(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestFindFiles_DefaultPathSort(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "findfiles_sort_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create files with names that sort differently than creation order
	os.WriteFile(filepath.Join(tmpDir, "z_last.go"), []byte("package z"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "a_first.go"), []byte("package a"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "m_middle.go"), []byte("package m"), 0644)

	result, err := FindFiles(nil, FindFilesArgs{
		Pattern:    "*.go",
		SearchPath: tmpDir,
	})
	if err != nil {
		t.Fatalf("FindFiles failed: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("Expected 3 files, got %d", result.Total)
	}
	// Default sort should be by path (alphabetical)
	if !strings.Contains(result.Files[0].Path, "a_first") {
		t.Errorf("Expected a_first.go first in default path sort, got %s", result.Files[0].Path)
	}
	if !strings.Contains(result.Files[2].Path, "z_last") {
		t.Errorf("Expected z_last.go last in default path sort, got %s", result.Files[2].Path)
	}
}

func TestFindFiles_CappedAccurate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "findfiles_capped_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create exactly 3 files
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("b"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "c.go"), []byte("c"), 0644)

	t.Run("NotCappedWhenExact", func(t *testing.T) {
		// MaxResults=3, exactly 3 files — should NOT be capped
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.go",
			SearchPath: tmpDir,
			MaxResults: 3,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		if result.Capped {
			t.Error("Should not be capped when total equals max (no more results exist)")
		}
	})

	t.Run("CappedWhenMore", func(t *testing.T) {
		// MaxResults=2, 3 files exist — should be capped
		result, err := FindFiles(nil, FindFilesArgs{
			Pattern:    "*.go",
			SearchPath: tmpDir,
			MaxResults: 2,
		})
		if err != nil {
			t.Fatalf("FindFiles failed: %v", err)
		}
		if !result.Capped {
			t.Error("Should be capped when more results exist than max")
		}
		if result.Total != 2 {
			t.Errorf("Expected 2 results when capped, got %d", result.Total)
		}
	})
}

func TestFileTree_SummaryFormat(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filetree_summary_fmt_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a subtree with mixed extensions
	bigDir := filepath.Join(tmpDir, "mixed")
	os.MkdirAll(bigDir, 0755)
	for i := 0; i < 40; i++ {
		os.WriteFile(filepath.Join(bigDir, fmt.Sprintf("f%03d.go", i)), []byte("go"), 0644)
	}
	for i := 0; i < 20; i++ {
		os.WriteFile(filepath.Join(bigDir, fmt.Sprintf("f%03d.ts", i)), []byte("ts"), 0644)
	}
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(bigDir, fmt.Sprintf("f%03d.md", i)), []byte("md"), 0644)
	}

	result, err := FileTree(nil, FileTreeArgs{
		Path:     tmpDir,
		MaxDepth: 3,
	})
	if err != nil {
		t.Fatalf("FileTree failed: %v", err)
	}

	// Find the summary for the "mixed" dir
	var summary string
	for _, e := range result.Entries {
		if e.Name == "mixed" && e.Truncated {
			summary = e.Summary
			break
		}
	}
	if summary == "" {
		t.Fatal("Expected 'mixed' directory to have a summary")
	}

	// Check format: should contain file/dir counts and extension breakdown
	if !strings.HasPrefix(summary, "[") || !strings.HasSuffix(summary, "]") {
		t.Errorf("Summary should be bracketed, got %q", summary)
	}
	if !strings.Contains(summary, "65 files") {
		t.Errorf("Summary should mention '65 files', got %q", summary)
	}
	if !strings.Contains(summary, ".go") {
		t.Errorf("Summary should mention .go extension, got %q", summary)
	}
	if !strings.Contains(summary, ".ts") {
		t.Errorf("Summary should mention .ts extension, got %q", summary)
	}

	// Test determinism: run twice, same result
	result2, _ := FileTree(nil, FileTreeArgs{Path: tmpDir, MaxDepth: 3})
	var summary2 string
	for _, e := range result2.Entries {
		if e.Name == "mixed" && e.Truncated {
			summary2 = e.Summary
			break
		}
	}
	if summary != summary2 {
		t.Errorf("Summary should be deterministic across runs:\n  run1: %q\n  run2: %q", summary, summary2)
	}
}

// --- search_tools tests ---

// testToolEmbeddingFunc creates a bag-of-words embedding function for tests.
func testToolEmbeddingFunc() agent.EmbedFunc {
	return func(_ context.Context, text string) ([]float32, error) {
		vec := make([]float32, 384)
		words := testToolSplitWords(text)
		for _, word := range words {
			h := sha256.Sum256([]byte(word))
			for i := 0; i < 8; i++ {
				dim := int(binary.LittleEndian.Uint16(h[i*2:])) % 384
				vec[dim] += 1.0
			}
		}
		var norm float64
		for _, v := range vec {
			norm += float64(v) * float64(v)
		}
		norm = math.Sqrt(norm)
		if norm > 0 {
			for i := range vec {
				vec[i] = float32(float64(vec[i]) / norm)
			}
		}
		return vec, nil
	}
}

func testToolSplitWords(s string) []string {
	var words []string
	current := ""
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			current += string(r)
		} else {
			if current != "" {
				words = append(words, current)
				current = ""
			}
		}
	}
	if current != "" {
		words = append(words, current)
	}
	return words
}

type searchToolsMockTool struct {
	name string
	desc string
}

func (m searchToolsMockTool) Name() string        { return m.name }
func (m searchToolsMockTool) Description() string { return m.desc }
func (m searchToolsMockTool) IsLongRunning() bool { return false }

func testToolIndex(t *testing.T) *agent.ToolIndex {
	t.Helper()
	vs, err := agent.NewInMemoryToolVectorStore(testToolEmbeddingFunc())
	if err != nil {
		t.Fatalf("NewInMemoryToolVectorStore: %v", err)
	}
	idx, err := agent.NewToolIndex(vs, testToolEmbeddingFunc())
	if err != nil {
		t.Fatalf("NewToolIndex: %v", err)
	}

	mainTools := []tool.Tool{
		searchToolsMockTool{name: "read_file", desc: "Read contents of a file from disk"},
		searchToolsMockTool{name: "write_file", desc: "Write content to a file on disk"},
	}

	groups := []*agent.ToolGroup{
		{
			Name:        "browser",
			Description: "Web automation, screenshots, form filling",
			Tools: []tool.Tool{
				searchToolsMockTool{name: "browser_navigate", desc: "Navigate the browser to a URL"},
				searchToolsMockTool{name: "browser_take_screenshot", desc: "Capture a screenshot of the current browser page"},
			},
		},
		{
			Name:        "web",
			Description: "HTTP requests and web fetching",
			Tools: []tool.Tool{
				searchToolsMockTool{name: "http_request", desc: "Make an HTTP request to an API endpoint"},
			},
		},
	}

	if err := idx.SyncTools(context.Background(), mainTools, groups); err != nil {
		t.Fatalf("SyncTools: %v", err)
	}
	return idx
}

func TestSearchTools_EmptyQuery(t *testing.T) {
	idx := testToolIndex(t)
	fn := SearchTools(idx)

	_, err := fn(nil, SearchToolsArgs{Query: ""})
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestSearchTools_ReturnsResults(t *testing.T) {
	idx := testToolIndex(t)
	fn := SearchTools(idx)

	result, err := fn(nil, SearchToolsArgs{Query: "screenshot browser page", MaxResults: 5})
	if err != nil {
		t.Fatalf("SearchTools: %v", err)
	}

	if result.Count == 0 {
		t.Fatal("expected at least one match")
	}

	for _, m := range result.Matches {
		if m.ToolName == "" {
			t.Error("match has empty tool name")
		}
		if m.GroupName == "" {
			t.Error("match has empty group name")
		}
		if m.Access == "" {
			t.Error("match has empty access instructions")
		}
		if m.Score <= 0 {
			t.Errorf("match %s has non-positive score", m.ToolName)
		}
	}
}

func TestSearchTools_AccessField(t *testing.T) {
	idx := testToolIndex(t)
	fn := SearchTools(idx)

	result, err := fn(nil, SearchToolsArgs{Query: "file read write", MaxResults: 10})
	if err != nil {
		t.Fatalf("SearchTools: %v", err)
	}

	for _, m := range result.Matches {
		if m.IsMainTool {
			if !strings.Contains(m.Access, "always available (main thread tool)") {
				t.Errorf("main tool %s should have always available access, got: %s", m.ToolName, m.Access)
			}
		} else {
			if m.Access == "" || strings.HasPrefix(m.Access, "always available (main thread tool)") && !strings.Contains(m.Access, "call") {
				t.Errorf("injected tool %s should instruct direct call, got: %s", m.ToolName, m.Access)
			}
		}
	}
}

func TestSearchTools_NoResults(t *testing.T) {
	vs, vsErr := agent.NewInMemoryToolVectorStore(testToolEmbeddingFunc())
	if vsErr != nil {
		t.Fatalf("NewInMemoryToolVectorStore: %v", vsErr)
	}
	idx, err := agent.NewToolIndex(vs, testToolEmbeddingFunc())
	if err != nil {
		t.Fatalf("NewToolIndex: %v", err)
	}

	fn := SearchTools(idx)
	result, err := fn(nil, SearchToolsArgs{Query: "anything"})
	if err != nil {
		t.Fatalf("SearchTools: %v", err)
	}
	if result.Count != 0 {
		t.Errorf("expected 0 results, got %d", result.Count)
	}
	if result.Message == "" {
		t.Error("expected a message for no results")
	}
}

func TestNewSearchToolsTool(t *testing.T) {
	idx := testToolIndex(t)
	st, err := NewSearchToolsTool(idx)
	if err != nil {
		t.Fatalf("NewSearchToolsTool: %v", err)
	}
	if st.Name() != "search_tools" {
		t.Errorf("expected name 'search_tools', got %q", st.Name())
	}
	declared, ok := st.(interface {
		Declaration() *genai.FunctionDeclaration
	})
	if !ok {
		t.Fatal("search_tools does not expose a declaration")
	}
	description := declared.Declaration().Description
	if !strings.Contains(description, "does not add model-visible tools") ||
		!strings.Contains(description, "execute_tool") {
		t.Fatalf("search_tools description permits dynamic injection: %q", description)
	}
}

func TestSearchTools_ListAll(t *testing.T) {
	idx := testToolIndex(t)
	fn := SearchTools(idx)

	result, err := fn(nil, SearchToolsArgs{Query: "*"})
	if err != nil {
		t.Fatalf("SearchTools list all: %v", err)
	}

	// testToolIndex has 5 tools: 2 main + 2 browser + 1 web
	if result.Count != 5 {
		t.Errorf("expected 5 tools in full inventory, got %d", result.Count)
	}

	if result.Message == "" {
		t.Error("expected a summary message for list-all")
	}

	// Verify all tools have score 1.0 (inventory mode, not search)
	for _, m := range result.Matches {
		if m.Score != 1.0 {
			t.Errorf("list-all tool %s should have score 1.0, got %f", m.ToolName, m.Score)
		}
	}

	// Main tools remain direct; every catalog-only tool must use the fixed bridge.
	for _, m := range result.Matches {
		if m.IsMainTool {
			if !strings.Contains(m.Access, "always available (main thread tool)") {
				t.Errorf("main tool %s should have always available access, got: %s", m.ToolName, m.Access)
			}
			continue
		}
		if !strings.Contains(m.Access, "describe_tools") || !strings.Contains(m.Access, "execute_tool") {
			t.Errorf("deferred tool %s must use the fixed bridge, got: %s", m.ToolName, m.Access)
		}
		if strings.Contains(m.Access, "call directly") {
			t.Errorf("deferred tool %s must not be advertised as direct, got: %s", m.ToolName, m.Access)
		}
	}
}

func TestSearchTools_ListAllVariants(t *testing.T) {
	idx := testToolIndex(t)
	fn := SearchTools(idx)

	queries := []string{"*", "list all", "list all tools", "all", "all tools"}
	for _, q := range queries {
		result, err := fn(nil, SearchToolsArgs{Query: q})
		if err != nil {
			t.Fatalf("SearchTools(%q): %v", q, err)
		}
		if result.Count != 5 {
			t.Errorf("SearchTools(%q): expected 5 tools, got %d", q, result.Count)
		}
	}
}
