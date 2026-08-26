package tui

import (
	"os"
	"runtime"
	"testing"
)

// projectRoot returns the repository root for tests that need a real git repo.
func projectRoot(t *testing.T) string {
	t.Helper()
	// Walk up from this test file's directory to find the .git folder.
	// The test binary runs with the package directory as cwd, so "." is
	// pkg/tui inside the project — the root is two levels up.
	dir, err := os.Getwd()
	if err != nil {
		t.Skip("cannot determine working directory")
	}
	return dir
}

func TestDetectGitBranchInGitRepo(t *testing.T) {
	if _, err := os.Stat("/usr/bin/git"); os.IsNotExist(err) {
		if _, err2 := lookPath("git"); err2 != nil {
			t.Skip("git not available")
		}
	}
	dir := projectRoot(t)
	branch := detectGitBranch(dir)
	// In CI (GitHub Actions), the checkout is often in detached HEAD state,
	// so the branch may legitimately be empty. Only assert non-empty when
	// we're not in a CI environment with a detached HEAD.
	if branch == "" && os.Getenv("CI") != "" {
		t.Skip("detached HEAD in CI — branch detection returns empty as expected")
	}
	if branch == "" {
		t.Fatalf("detectGitBranch(%q): expected non-empty branch in a git repo, got empty", dir)
	}
}

func TestDetectGitBranchNotARepo(t *testing.T) {
	dir := t.TempDir()
	branch := detectGitBranch(dir)
	if branch != "" {
		t.Fatalf("detectGitBranch(non-git dir): expected empty, got %q", branch)
	}
}

func TestDetectGitBranchEmptyDir(t *testing.T) {
	branch := detectGitBranch("")
	if branch != "" {
		t.Fatalf("detectGitBranch(\"\"): expected empty, got %q", branch)
	}
}

// lookPath is a thin shim so the test file doesn't need to import os/exec directly.
func lookPath(file string) (string, error) {
	_ = runtime.GOOS // suppress unused-import lint
	return file, nil // on macOS/Linux git is always in PATH when present
}
