package gitutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func integrationFixture(t *testing.T) (string, *CodeWorkspaceManager, *WorkspaceRecord) {
	t.Helper()
	root, m := workspaceTestRepo(t)
	r, e := m.Create(context.Background(), WorkspaceCreateOptions{SourceDir: root})
	if e != nil {
		t.Fatal(e)
	}
	return root, m, r
}
func integrationReview(t *testing.T, m *CodeWorkspaceManager, r *WorkspaceRecord) *WorkspaceReview {
	t.Helper()
	v, e := m.Review(context.Background(), r.Project, r.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func integrationApply(m *CodeWorkspaceManager, r *WorkspaceRecord, v *WorkspaceReview) error {
	return m.Apply(context.Background(), r.Project, r.ID, WorkspaceApplyOptions{ReviewDigest: v.Digest})
}
func TestCodeWorkspaceApplyBinaryAndUnrelatedStaging(t *testing.T) {
	root, m, r := integrationFixture(t)
	workspaceTestWrite(t, root, "sub/guide.txt", []byte("unrelated staged\n"))
	workspaceTestGit(t, root, "add", "sub/guide.txt")
	_, index, e := sourceWorkspaceIndex(context.Background(), root)
	if e != nil {
		t.Fatal(e)
	}
	head := workspaceTestGit(t, root, "rev-parse", "HEAD")
	branch := workspaceTestGit(t, root, "symbolic-ref", "HEAD")
	workspaceTestWrite(t, r.Root, "file.txt", []byte("committed change\n"))
	workspaceTestGit(t, r.Root, "add", "file.txt")
	workspaceTestGit(t, r.Root, "commit", "-m", "private")
	binary := []byte{0, 1, 0xff, '\n', '\n'}
	workspaceTestWrite(t, r.Root, "nested/binary.dat", binary)
	if e = os.Chmod(filepath.Join(r.Root, "file.txt"), 0755); e != nil {
		t.Fatal(e)
	}
	if e = integrationApply(m, r, integrationReview(t, m, r)); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(filepath.Join(root, "nested/binary.dat"))
	if e != nil || !bytes.Equal(got, binary) {
		t.Fatalf("binary %q %v", got, e)
	}
	_, after, e := sourceWorkspaceIndex(context.Background(), root)
	if e != nil || !bytes.Equal(index, after) {
		t.Fatal("target index modified", e)
	}
	if workspaceTestGit(t, root, "rev-parse", "HEAD") != head || workspaceTestGit(t, root, "symbolic-ref", "HEAD") != branch {
		t.Fatal("target HEAD/branch modified")
	}
	st, e := os.Stat(filepath.Join(root, "file.txt"))
	if e != nil || st.Mode()&0111 == 0 {
		t.Fatal("executable lost", e)
	}
	if e = integrationApply(m, r, integrationReview(t, m, r)); e != nil {
		t.Fatal("idempotent apply", e)
	}
}
func TestCodeWorkspaceApplyConflictNoWrites(t *testing.T) {
	root, m, r := integrationFixture(t)
	workspaceTestWrite(t, r.Root, "file.txt", []byte("source"))
	workspaceTestWrite(t, r.Root, "aaa-new.txt", []byte("new"))
	workspaceTestWrite(t, root, "file.txt", []byte("target"))
	if e := integrationApply(m, r, integrationReview(t, m, r)); !errors.Is(e, ErrWorkspaceConflict) {
		t.Fatalf("conflict: %v", e)
	}
	if _, e := os.Stat(filepath.Join(root, "aaa-new.txt")); !os.IsNotExist(e) {
		t.Fatal("partial writes", e)
	}
	got, _ := os.ReadFile(filepath.Join(root, "file.txt"))
	if string(got) != "target" {
		t.Fatal("overwritten")
	}
}
func TestCodeWorkspaceApplyStaleReview(t *testing.T) {
	for _, side := range []string{"source", "target", "index"} {
		t.Run(side, func(t *testing.T) {
			root, m, r := integrationFixture(t)
			workspaceTestWrite(t, r.Root, "file.txt", []byte("source"))
			v := integrationReview(t, m, r)
			switch side {
			case "source":
				workspaceTestWrite(t, r.Root, "other", []byte("late"))
			case "target":
				workspaceTestWrite(t, root, "other", []byte("late"))
			case "index":
				workspaceTestGit(t, r.Root, "add", "file.txt")
			}
			if e := integrationApply(m, r, v); !errors.Is(e, ErrWorkspaceStale) {
				t.Fatalf("stale: %v", e)
			}
			got, _ := os.ReadFile(filepath.Join(root, "file.txt"))
			if string(got) != "base\n" {
				t.Fatal("wrote stale review")
			}
		})
	}
}
func TestCodeWorkspaceApplyRecoveryJournal(t *testing.T) {
	root, m, r := integrationFixture(t)
	workspaceTestWrite(t, r.Root, "file.txt", []byte{0, 255, 10, 10})
	workspaceTestWrite(t, r.Root, "sub/guide.txt", []byte("two"))
	v := integrationReview(t, m, r)
	path := filepath.Join(m.root, r.Project.ID, "apply-journal.json")
	j := workspaceApplyJournal{Version: 1, WorkspaceID: r.ID, TargetRoot: root, TargetHEAD: v.TargetHEAD, TargetIndexDigest: v.TargetIndexDigest, Changes: v.Changes}
	if e := workspaceWriteJournal(path, j); e != nil {
		t.Fatal(e)
	}
	if e := workspaceInstallFile(root, v.Changes[0].Path, v.Changes[0].Desired); e != nil {
		t.Fatal(e)
	}
	if e := integrationApply(m, r, v); !errors.Is(e, ErrWorkspaceRecoveryRequired) {
		t.Fatal(e)
	}
	if e := m.RecoverApply(context.Background(), r.Project); e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(filepath.Join(root, "file.txt"))
	if string(got) != "base\n" {
		t.Fatalf("preimage %q", got)
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("journal retained", e)
	}
	// Independent external edits are never overwritten by recovery.
	if e := workspaceWriteJournal(path, j); e != nil {
		t.Fatal(e)
	}
	workspaceTestWrite(t, root, "file.txt", []byte("external"))
	if e := m.RecoverApply(context.Background(), r.Project); !errors.Is(e, ErrWorkspaceRecoveryRequired) {
		t.Fatal(e)
	}
	got, _ = os.ReadFile(filepath.Join(root, "file.txt"))
	if string(got) != "external" {
		t.Fatal("external edit lost")
	}
}
func TestCodeWorkspaceApplyDeletesAndSelection(t *testing.T) {
	root, m, r := integrationFixture(t)
	if e := os.Remove(filepath.Join(r.Root, "file.txt")); e != nil {
		t.Fatal(e)
	}
	workspaceTestWrite(t, r.Root, "new", []byte("new"))
	v := integrationReview(t, m, r)
	if e := m.Apply(context.Background(), r.Project, r.ID, WorkspaceApplyOptions{ReviewDigest: v.Digest, Paths: []string{"file.txt"}}); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(root, "file.txt")); !os.IsNotExist(e) {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(e) {
		t.Fatal("selection ignored")
	}
}
func TestCodeWorkspaceApplySelectedReceiptsAndRecovery(t *testing.T) {
	root, m, r := integrationFixture(t)
	ctx := context.Background()
	workspaceTestWrite(t, r.Root, "first", []byte("one"))
	workspaceTestWrite(t, r.Root, "second", []byte("two"))
	for _, path := range []string{"first", "second"} {
		v := integrationReview(t, m, r)
		if e := m.Apply(ctx, r.Project, r.ID, WorkspaceApplyOptions{ReviewDigest: v.Digest, Paths: []string{path}}); e != nil {
			t.Fatal(e)
		}
	}
	v := integrationReview(t, m, r)
	if e := integrationApply(m, r, v); e != nil {
		t.Fatal("lost earlier receipt", e)
	}
	receiptPath := filepath.Join(m.root, r.Project.ID, "applied-"+r.ID+".json")
	previous, e := os.ReadFile(receiptPath)
	if e != nil {
		t.Fatal(e)
	}
	workspaceTestWrite(t, r.Root, "file.txt", []byte("new"))
	v = integrationReview(t, m, r)
	j := workspaceApplyJournal{Version: 1, WorkspaceID: r.ID, TargetRoot: root, TargetHEAD: v.TargetHEAD, TargetIndexDigest: v.TargetIndexDigest, Changes: v.Changes, PreviousReceipt: previous}
	journalPath := filepath.Join(m.root, r.Project.ID, "apply-journal.json")
	if e = workspaceWriteJournal(journalPath, j); e != nil {
		t.Fatal(e)
	}
	// Simulate a crash after receipt replacement but before journal deletion.
	if e = workspaceWriteJournal(receiptPath, j); e != nil {
		t.Fatal(e)
	}
	if e = m.RecoverApply(ctx, r.Project); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(receiptPath)
	if e != nil {
		t.Fatal(e)
	}
	var a, b workspaceApplyJournal
	if e = json.Unmarshal(previous, &a); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(got, &b); e != nil {
		t.Fatal(e)
	}
	if workspaceJSONDigest(a) != workspaceJSONDigest(b) {
		t.Fatal("prior receipt not restored")
	}
}

func TestCodeWorkspaceApplyRecoveryPreparedFiles(t *testing.T) {
	root, m, r := integrationFixture(t)
	workspaceTestWrite(t, r.Root, "newdir/file", []byte{0, 255, 10})
	v := integrationReview(t, m, r)
	dir := filepath.Join(root, "newdir")
	tmp := filepath.Join(dir, ".astonish-apply-test")
	j := workspaceApplyJournal{Version: 1, WorkspaceID: r.ID, TargetRoot: root, TargetHEAD: v.TargetHEAD, TargetIndexDigest: v.TargetIndexDigest, Changes: v.Changes, CreatedDirs: []string{dir}, Prepared: map[string]string{"newdir/file": tmp}}
	path := filepath.Join(m.root, r.Project.ID, "apply-journal.json")
	if e := workspaceWriteJournal(path, j); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(tmp, []byte("prepared"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := m.RecoverApply(context.Background(), r.Project); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("prepared data remains", e)
	}
}

func TestCodeWorkspaceApplyRecoveryBeforePreparation(t *testing.T) {
	root, m, r := integrationFixture(t)
	workspaceTestWrite(t, r.Root, "newdir/nested/file", []byte{0, 255, 10})
	v := integrationReview(t, m, r)
	dir := filepath.Join(root, "newdir")
	nested := filepath.Join(dir, "nested")
	j := workspaceApplyJournal{Version: 1, WorkspaceID: r.ID, TargetRoot: root, TargetHEAD: v.TargetHEAD, TargetIndexDigest: v.TargetIndexDigest, Changes: v.Changes, CreatedDirs: []string{dir, nested}, Prepared: map[string]string{"newdir/nested/file": filepath.Join(nested, ".astonish-apply-test")}}
	path := filepath.Join(m.root, r.Project.ID, "apply-journal.json")
	for _, partiallyPrepared := range []bool{false, true} {
		if e := workspaceWriteJournal(path, j); e != nil {
			t.Fatal(e)
		}
		if partiallyPrepared {
			if e := os.Mkdir(dir, 0755); e != nil {
				t.Fatal(e)
			}
		}
		if e := m.RecoverApply(context.Background(), r.Project); e != nil {
			t.Fatal("recovery before complete directory preparation", e)
		}
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal("partial preparation remains", e)
		}
	}
}

func TestCodeWorkspaceApplyCaseFoldParents(t *testing.T) {
	root, _, _ := integrationFixture(t)
	if e := os.Mkdir(filepath.Join(root, "Existing"), 0755); e != nil {
		t.Fatal(e)
	}
	for _, changes := range [][]WorkspaceChange{
		{{Path: "existing/new", Desired: WorkspaceFileState{Exists: true}}},
		{{Path: "fresh/one", Desired: WorkspaceFileState{Exists: true}}, {Path: "FRESH/two", Desired: WorkspaceFileState{Exists: true}}},
	} {
		if e := workspaceCaseCheck(context.Background(), root, changes); !errors.Is(e, ErrWorkspaceConflict) {
			t.Fatal("case-fold parent accepted", e)
		}
	}
}

func TestCodeWorkspaceApplyUnsafeAndBusy(t *testing.T) {
	t.Run("untracked", func(t *testing.T) {
		root, m, r := integrationFixture(t)
		workspaceTestWrite(t, r.Root, "new", []byte("same"))
		workspaceTestWrite(t, root, "new", []byte("same"))
		if e := integrationApply(m, r, integrationReview(t, m, r)); !errors.Is(e, ErrWorkspaceConflict) {
			t.Fatal(e)
		}
	})
	t.Run("symlink parent", func(t *testing.T) {
		root, m, r := integrationFixture(t)
		workspaceTestWrite(t, r.Root, "newdir/file", []byte("new"))
		if e := os.Symlink(workspaceTestDir(t), filepath.Join(root, "newdir")); e != nil {
			t.Fatal(e)
		}
		if _, e := m.Review(context.Background(), r.Project, r.ID, ""); e == nil {
			t.Fatal("accepted symlink")
		}
	})
	t.Run("case alias", func(t *testing.T) {
		_, m, r := integrationFixture(t)
		workspaceTestGit(t, r.Root, "mv", "file.txt", "FILE.txt")
		v, e := m.Review(context.Background(), r.Project, r.ID, "")
		if e != nil {
			return
		}
		if e = integrationApply(m, r, v); e == nil {
			t.Fatal("accepted case alias")
		}
	})
	t.Run("managed target lease", func(t *testing.T) {
		root, m, r := integrationFixture(t)
		target, e := m.Create(context.Background(), WorkspaceCreateOptions{SourceDir: root})
		if e != nil {
			t.Fatal(e)
		}
		workspaceTestWrite(t, r.Root, "file.txt", []byte("source"))
		v, e := m.Review(context.Background(), r.Project, r.ID, target.Root)
		if e != nil {
			t.Fatal(e)
		}
		lease, e := m.AcquireLease(context.Background(), r.Project, target.ID)
		if e != nil {
			t.Fatal(e)
		}
		defer lease.Close()
		if e = m.Apply(context.Background(), r.Project, r.ID, WorkspaceApplyOptions{ReviewDigest: v.Digest, TargetDir: target.Root}); !errors.Is(e, ErrWorkspaceBusy) {
			t.Fatal(e)
		}
	})
}
