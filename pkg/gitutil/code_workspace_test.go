package gitutil

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func workspaceTestDir(t *testing.T) string {
	t.Helper()
	d, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func workspaceTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	s, e := workspaceGit(context.Background(), dir, nil, args...)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func workspaceTestWrite(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	p := filepath.Join(dir, name)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func workspaceTestRepo(t *testing.T) (string, *CodeWorkspaceManager) {
	t.Helper()
	dir := workspaceTestDir(t)
	workspaceTestGit(t, dir, "init")
	workspaceTestGit(t, dir, "config", "user.name", "Workspace Test")
	workspaceTestGit(t, dir, "config", "user.email", "workspace@example.invalid")
	workspaceTestWrite(t, dir, "file.txt", []byte("base\n"))
	workspaceTestWrite(t, dir, "sub/guide.txt", []byte("guide"))
	workspaceTestGit(t, dir, "add", ".")
	workspaceTestGit(t, dir, "commit", "-m", "base")
	m, e := NewCodeWorkspaceManager(filepath.Join(workspaceTestDir(t), "managed"))
	if e != nil {
		t.Fatal(e)
	}
	return dir, m
}
func TestCodeWorkspaceCreateCommitted(t *testing.T) {
	dir, m := workspaceTestRepo(t)
	ctx := context.Background()
	before := workspaceTestGit(t, dir, "rev-parse", "HEAD")
	a, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: filepath.Join(dir, "sub")})
	if e != nil {
		t.Fatal(e)
	}
	b, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: dir})
	if e != nil {
		t.Fatal(e)
	}
	if a.Root == b.Root || a.InvocationDir != filepath.Join(a.Root, "sub") {
		t.Fatal("paths not isolated")
	}
	if DetectBranch(a.Root) != "" {
		t.Fatal("not detached")
	}
	workspaceTestWrite(t, a.Root, "file.txt", []byte("a\n"))
	workspaceTestGit(t, a.Root, "add", ".")
	workspaceTestGit(t, a.Root, "commit", "-m", "private")
	for _, root := range []string{dir, b.Root} {
		data, e := os.ReadFile(filepath.Join(root, "file.txt"))
		if e != nil || string(data) != "base\n" {
			t.Fatal("checkout leaked")
		}
		if workspaceTestGit(t, root, "rev-parse", "HEAD") != before {
			t.Fatal("HEAD changed")
		}
	}
	p, e := ResolveCodeProject(ctx, a.Root)
	if e != nil || p.ID != a.Project.ID {
		t.Fatal("project identity diverged", e)
	}
	if e = m.Validate(ctx, *a); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"committed", "staged", "full"} {
		workspaceTestGit(t, dir, "rev-parse", "--verify", "refs/astonish/workspaces/"+a.ID+"/"+name)
	}
}
func TestCodeWorkspaceOwnershipAndUnsupportedRepo(t *testing.T) {
	ctx := context.Background()
	dir, m := workspaceTestRepo(t)
	if _, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: workspaceTestDir(t)}); !errors.Is(e, ErrWorkspaceUnsupported) {
		t.Fatal(e)
	}
	unborn := workspaceTestDir(t)
	workspaceTestGit(t, unborn, "init")
	if _, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: unborn}); !errors.Is(e, ErrWorkspaceUnsupported) {
		t.Fatal(e)
	}
	workspaceTestGit(t, dir, "config", "core.sparseCheckout", "true")
	if _, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: dir}); !errors.Is(e, ErrWorkspaceUnsupported) {
		t.Fatal(e)
	}
	workspaceTestGit(t, dir, "config", "core.sparseCheckout", "false")
	r, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: dir})
	if e != nil {
		t.Fatal(e)
	}
	foreign := workspaceTestDir(t)
	workspaceTestGit(t, foreign, "init")
	workspaceTestWrite(t, foreign, "x", []byte("foreign"))
	workspaceTestGit(t, foreign, "config", "user.name", "Test")
	workspaceTestGit(t, foreign, "config", "user.email", "test@example.invalid")
	workspaceTestGit(t, foreign, "add", ".")
	workspaceTestGit(t, foreign, "commit", "-m", "foreign")
	forged := *r
	forged.GitDir = filepath.Join(foreign, ".git")
	if e = m.Validate(ctx, forged); !errors.Is(e, ErrWorkspaceOwnership) {
		t.Fatal("foreign gitdir accepted", e)
	}
	old := r.Root + "-saved"
	if e = os.Rename(r.Root, old); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(dir, r.Root); e != nil {
		t.Fatal(e)
	}
	if e = m.Validate(ctx, *r); !errors.Is(e, ErrWorkspaceOwnership) {
		t.Fatal(e)
	}
	if _, e = NewCodeWorkspaceManager(r.Root); !errors.Is(e, ErrWorkspaceOwnership) {
		t.Fatal(e)
	}
}
func TestCodeWorkspacePreservesDirtyIndexAndUntracked(t *testing.T) {
	ctx := context.Background()
	dir, m := workspaceTestRepo(t)
	workspaceTestWrite(t, dir, "file.txt", []byte("staged\n"))
	workspaceTestGit(t, dir, "add", "file.txt")
	workspaceTestWrite(t, dir, "file.txt", []byte("unstaged\n"))
	binary := []byte{0, 1, 2, 255, 0, 3}
	workspaceTestWrite(t, dir, " untracked\n[bin] ", binary)
	workspaceTestWrite(t, dir, ".gitignore", []byte("ignored\n"))
	workspaceTestWrite(t, dir, "ignored", []byte("private"))
	workspaceTestWrite(t, dir, ".env", []byte("secret"))
	workspaceTestWrite(t, dir, "node_modules/file", []byte("dependency"))
	_, index, e := sourceWorkspaceIndex(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	head := workspaceTestGit(t, dir, "rev-parse", "HEAD")
	r, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: dir, CopyMode: WorkspaceCopyCurrentWork})
	if e != nil {
		t.Fatal(e)
	}
	_, after, e := sourceWorkspaceIndex(ctx, dir)
	if e != nil || !bytes.Equal(index, after) {
		t.Fatal("source real index changed", e)
	}
	if workspaceTestGit(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("HEAD changed")
	}
	if workspaceTestGit(t, r.Root, "show", ":file.txt") != "staged" {
		t.Fatal("staged content not preserved")
	}
	data, e := os.ReadFile(filepath.Join(r.Root, "file.txt"))
	if e != nil || string(data) != "unstaged\n" {
		t.Fatal("unstaged content not preserved")
	}
	data, e = os.ReadFile(filepath.Join(r.Root, " untracked\n[bin] "))
	if e != nil || !bytes.Equal(data, binary) {
		t.Fatal("binary not preserved", e)
	}
	for _, name := range []string{"ignored", ".env", "node_modules/file"} {
		if _, e = os.Stat(filepath.Join(r.Root, name)); !os.IsNotExist(e) {
			t.Fatalf("copied excluded %s: %v", name, e)
		}
	}
	if workspaceTestGit(t, r.Root, "ls-files", "--others", "--exclude-standard", "-z") == "" {
		t.Fatal("untracked file was staged")
	}
}
