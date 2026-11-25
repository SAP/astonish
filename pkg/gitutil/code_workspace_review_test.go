package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCodeWorkspaceReviewInitialBaseline(t *testing.T) {
	root, m := workspaceTestRepo(t)
	workspaceTestWrite(t, root, "file.txt", []byte("initial dirty\n"))
	workspaceTestWrite(t, root, "untracked.bin", []byte{0, 255, 10, 10})
	r, e := m.Create(context.Background(), WorkspaceCreateOptions{SourceDir: root, CopyMode: WorkspaceCopyCurrentWork})
	if e != nil {
		t.Fatal(e)
	}
	if v := integrationReview(t, m, r); len(v.Changes) != 0 {
		t.Fatal("baseline work treated as task changes", v.Changes)
	}
	workspaceTestWrite(t, r.Root, "file.txt", []byte("task\n"))
	workspaceTestGit(t, r.Root, "add", "file.txt")
	workspaceTestGit(t, r.Root, "commit", "-m", "task")
	v := integrationReview(t, m, r)
	if len(v.Changes) != 1 || string(v.Changes[0].Base.Data) != "initial dirty\n" || v.Changes[0].Conflict {
		t.Fatalf("wrong baseline %+v", v)
	}
	if e = integrationApply(m, r, v); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(root, "untracked.bin"))
	if e != nil || len(b) != 4 || b[1] != 255 || b[3] != 10 {
		t.Fatal("initial binary not retained", e)
	}
}
func TestCodeWorkspaceReviewUnsupportedSymlink(t *testing.T) {
	_, m, r := integrationFixture(t)
	if e := os.Remove(filepath.Join(r.Root, "file.txt")); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink("sub/guide.txt", filepath.Join(r.Root, "file.txt")); e != nil {
		t.Fatal(e)
	}
	v := integrationReview(t, m, r)
	if len(v.Changes) != 1 || v.Changes[0].Unsupported == "" {
		t.Fatal("missing unsupported explanation")
	}
	if e := integrationApply(m, r, v); e == nil {
		t.Fatal("applied symlink")
	}
}
func TestCodeWorkspaceReviewIgnoredInitialFile(t *testing.T) {
	root, m := workspaceTestRepo(t)
	workspaceTestWrite(t, root, "initial", []byte("baseline"))
	r, e := m.Create(context.Background(), WorkspaceCreateOptions{SourceDir: root, CopyMode: WorkspaceCopyCurrentWork})
	if e != nil {
		t.Fatal(e)
	}
	ignore := filepath.Join(workspaceTestDir(t), "ignore")
	if e = os.WriteFile(ignore, []byte("initial\n"), 0600); e != nil {
		t.Fatal(e)
	}
	workspaceTestGit(t, root, "config", "core.excludesFile", ignore)
	if v := integrationReview(t, m, r); len(v.Changes) != 0 {
		t.Fatal("ignored baseline treated as deletion", v.Changes)
	}
	before, e := inspectWorkspaceBaseline(context.Background(), r.Root, r.FullTree)
	if e != nil {
		t.Fatal(e)
	}
	workspaceTestWrite(t, r.Root, "initial", []byte("task"))
	after, e := inspectWorkspaceBaseline(context.Background(), r.Root, r.FullTree)
	if e != nil || workspaceJSONDigest(before) == workspaceJSONDigest(after) {
		t.Fatal("final apply inspection missed an ignored baseline edit", e)
	}
	v := integrationReview(t, m, r)
	if len(v.Changes) != 1 || v.Changes[0].Kind != WorkspaceChangeModify {
		t.Fatal(v)
	}
	if e = integrationApply(m, r, v); e != nil {
		t.Fatal(e)
	}
}

func TestCodeWorkspaceReviewLiteralPaths(t *testing.T) {
	root, m, r := integrationFixture(t)
	name := "weird\n[blob]"
	workspaceTestWrite(t, r.Root, name, []byte{0, 1, 255, 10, 10})
	v := integrationReview(t, m, r)
	if e := integrationApply(m, r, v); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(root, name))
	if e != nil || len(b) != 5 || b[2] != 255 {
		t.Fatal(e, b)
	}
}
