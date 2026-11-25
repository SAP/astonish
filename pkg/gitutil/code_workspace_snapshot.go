package gitutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// workspaceSnapshot uses private indexes. No command in this file may write the source index.
type workspaceSnapshot struct{ dir, head, indexHash, staged, full string }

func (s *workspaceSnapshot) close() { _ = os.RemoveAll(s.dir) }
func sourceWorkspaceIndex(ctx context.Context, root string) (string, []byte, error) {
	path, e := workspaceGit(ctx, root, nil, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if e != nil {
		return "", nil, e
	}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return path, nil, nil
	}
	return path, b, e
}
func captureWorkspaceSnapshot(ctx context.Context, root string) (*workspaceSnapshot, error) {
	dir, e := os.MkdirTemp("", "astonish-workspace-index-*")
	if e != nil {
		return nil, e
	}
	s := &workspaceSnapshot{dir: dir}
	fail := func(e error) (*workspaceSnapshot, error) { s.close(); return nil, e }
	s.head, e = workspaceGit(ctx, root, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if e != nil {
		return fail(e)
	}
	_, b, e := sourceWorkspaceIndex(ctx, root)
	if e != nil {
		return fail(e)
	}
	s.indexHash = workspaceDigest(b)
	// read-tree reconstructs the logical staged tree, expanding split indexes safely.
	stagedIndex := filepath.Join(dir, "staged")
	if len(b) > 0 {
		if e = os.WriteFile(stagedIndex, b, 0600); e != nil {
			return fail(e)
		}
	} else {
		if _, e = workspaceGit(ctx, root, []string{"GIT_INDEX_FILE=" + stagedIndex}, "read-tree", "--empty"); e != nil {
			return fail(e)
		}
	}
	env := []string{"GIT_INDEX_FILE=" + stagedIndex, "GIT_OPTIONAL_LOCKS=0"}
	s.staged, e = workspaceGit(ctx, root, env, "write-tree")
	if e != nil {
		return fail(e)
	}
	s.full, e = s.workingTree(ctx, root, "full")
	if e != nil {
		return fail(e)
	}
	if e = s.verify(ctx, root); e != nil {
		return fail(e)
	}
	return s, nil
}

// Exclude common secrets/build artifacts even when an untracked file was not ignored.
// Tracked files remain tracked and are never silently excluded from a snapshot.
func excludedWorkspaceUntracked(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		switch part {
		case "node_modules", "vendor", ".venv", "venv", "__pycache__", ".cache", "dist", "build", "target", ".env":
			return true
		}
		if strings.HasPrefix(part, ".env.") || strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") {
			return true
		}
	}
	return false
}
func (s *workspaceSnapshot) workingTree(ctx context.Context, root, name string) (string, error) {
	path := filepath.Join(s.dir, name)
	_ = os.Remove(path)
	env := []string{"GIT_INDEX_FILE=" + path, "GIT_OPTIONAL_LOCKS=0"}
	if _, e := workspaceGit(ctx, root, env, "read-tree", s.staged); e != nil {
		return "", e
	}
	if _, e := workspaceGit(ctx, root, env, "add", "-u", "--", "."); e != nil {
		return "", e
	}
	untracked, e := workspaceGit(ctx, root, env, "ls-files", "--others", "--exclude-standard", "-z")
	if e != nil {
		return "", e
	}
	// Literal pathspecs handle whitespace/newlines, brackets, leading dashes and binary content.
	paths := []string{}
	for _, p := range strings.Split(untracked, "\x00") {
		if p != "" && !excludedWorkspaceUntracked(p) {
			paths = append(paths, p)
		}
	}
	for start := 0; start < len(paths); start += 100 {
		end := start + 100
		if end > len(paths) {
			end = len(paths)
		}
		args := append([]string{"--literal-pathspecs", "add", "--"}, paths[start:end]...)
		if _, e = workspaceGit(ctx, root, env, args...); e != nil {
			return "", e
		}
	}
	return workspaceGit(ctx, root, env, "write-tree")
}
func (s *workspaceSnapshot) verify(ctx context.Context, root string) error {
	head, e := workspaceGit(ctx, root, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if e != nil {
		return e
	}
	_, b, e := sourceWorkspaceIndex(ctx, root)
	if e != nil {
		return e
	}
	if head != s.head || workspaceDigest(b) != s.indexHash {
		return ErrWorkspaceStale
	}
	tree, e := s.workingTree(ctx, root, "verify")
	if e != nil {
		return fmt.Errorf("%w: %v", ErrWorkspaceStale, e)
	}
	if tree != s.full {
		return ErrWorkspaceStale
	}
	head, e = workspaceGit(ctx, root, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if e != nil {
		return e
	}
	_, b, e = sourceWorkspaceIndex(ctx, root)
	if e != nil {
		return e
	}
	if head != s.head || workspaceDigest(b) != s.indexHash {
		return ErrWorkspaceStale
	}
	return nil
}
