package gitutil

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestCodeWorkspaceSourceChangesReject(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"working", "index", "head", "untracked"} {
		t.Run(kind, func(t *testing.T) {
			dir, _ := workspaceTestRepo(t)
			s, e := captureWorkspaceSnapshot(ctx, dir)
			if e != nil {
				t.Fatal(e)
			}
			defer s.close()
			switch kind {
			case "working":
				workspaceTestWrite(t, dir, "file.txt", []byte("changed"))
			case "index":
				workspaceTestWrite(t, dir, "file.txt", []byte("changed"))
				workspaceTestGit(t, dir, "add", "file.txt")
			case "head":
				workspaceTestGit(t, dir, "commit", "--allow-empty", "-m", "advance")
			case "untracked":
				workspaceTestWrite(t, dir, "new", []byte("new"))
			}
			if e = s.verify(ctx, dir); !errors.Is(e, ErrWorkspaceStale) {
				t.Fatal("accepted changed source", e)
			}
		})
	}
}
func TestCodeWorkspaceSnapshotDeletedAndSplitIndex(t *testing.T) {
	ctx := context.Background()
	dir, m := workspaceTestRepo(t)
	workspaceTestGit(t, dir, "update-index", "--split-index")
	if e := os.Remove(dir + "/file.txt"); e != nil {
		t.Fatal(e)
	}
	r, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: dir, CopyMode: WorkspaceCopyCurrentWork})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(r.Root + "/file.txt"); !os.IsNotExist(e) {
		t.Fatal("deletion lost", e)
	}
	if workspaceTestGit(t, r.Root, "show", ":file.txt") != "base" {
		t.Fatal("staged deletion incorrectly applied")
	}
}
