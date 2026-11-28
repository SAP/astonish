package gitutil

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCodeWorkspaceNestedCreationPreservesOriginal(t *testing.T) {
	ctx := context.Background()
	dir, m := workspaceTestRepo(t)
	first, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: dir})
	if e != nil {
		t.Fatal(e)
	}
	workspaceTestWrite(t, first.Root, "file.txt", []byte("private dirty"))
	second, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: filepath.Join(first.Root, "sub"), CopyMode: WorkspaceCopyCurrentWork})
	if e != nil {
		t.Fatal(e)
	}
	if second.Project.OriginalRoot != dir || second.SourceRoot != first.Root || second.InvocationDir != filepath.Join(second.Root, "sub") {
		t.Fatal("original/source identity lost", second)
	}
	p, e := m.ResolveProject(ctx, second.Root)
	if e != nil || p.OriginalRoot != dir {
		t.Fatal("canonical original lost", p, e)
	}
}
