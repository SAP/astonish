// Package gitutil provides lightweight utilities for reading local git
// repository state without pulling in a full VCS library.
package gitutil

import (
	"os/exec"
	"strings"
)

// DetectBranch returns the active git branch name for dir, or an empty
// string when dir is not inside a git repository, git is not available,
// or the repository is in a detached-HEAD state.
func DetectBranch(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		// Detached HEAD — no named branch to display.
		return ""
	}
	return branch
}
