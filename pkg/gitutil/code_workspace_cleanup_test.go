package gitutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCodeWorkspaceCreateBranchNoReset(t *testing.T) {
	root, m, r := integrationFixture(t)
	ctx := context.Background()
	workspaceTestWrite(t, r.Root, "file.txt", []byte("private"))
	workspaceTestGit(t, r.Root, "add", "file.txt")
	workspaceTestGit(t, r.Root, "commit", "-m", "private")
	head := workspaceTestGit(t, r.Root, "rev-parse", "HEAD")
	original := workspaceTestGit(t, root, "rev-parse", "HEAD")
	workspaceTestGit(t, root, "branch", "existing")
	if e := m.CreateBranch(ctx, r.Project, r.ID, "existing"); e == nil {
		t.Fatal("reset existing")
	}
	if workspaceTestGit(t, root, "rev-parse", "existing") != original {
		t.Fatal("existing reset")
	}
	if e := m.CreateBranch(ctx, r.Project, r.ID, "../unsafe"); e == nil {
		t.Fatal("invalid branch accepted")
	}
	if e := m.CreateBranch(ctx, r.Project, r.ID, "task/preserved"); e != nil {
		t.Fatal(e)
	}
	if workspaceTestGit(t, r.Root, "rev-parse", "HEAD") != head || workspaceTestGit(t, r.Root, "symbolic-ref", "HEAD") != "refs/heads/task/preserved" {
		t.Fatal("not preserved")
	}
	if workspaceTestGit(t, root, "rev-parse", "HEAD") != original {
		t.Fatal("original changed")
	}
}
func TestCodeWorkspaceRemoveGuards(t *testing.T) {
	ctx := context.Background()
	t.Run("active", func(t *testing.T) {
		_, m, r := integrationFixture(t)
		lease, e := m.AcquireLease(ctx, r.Project, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		defer lease.Close()
		if _, e = m.ReviewRemoval(ctx, r.Project, r.ID); !errors.Is(e, ErrWorkspaceBusy) {
			t.Fatal(e)
		}
	})
	for _, kind := range []string{"dirty", "staged", "untracked", "ignored", "unique", "embedded"} {
		t.Run(kind, func(t *testing.T) {
			_, m, r := integrationFixture(t)
			switch kind {
			case "dirty":
				workspaceTestWrite(t, r.Root, "file.txt", []byte("dirty"))
			case "staged":
				workspaceTestWrite(t, r.Root, "file.txt", []byte("stage"))
				workspaceTestGit(t, r.Root, "add", "file.txt")
			case "untracked":
				workspaceTestWrite(t, r.Root, "untracked", []byte("data"))
			case "ignored":
				workspaceTestGit(t, r.Root, "config", "core.excludesFile", filepath.Join(workspaceTestDir(t), "ignore"))
				path := workspaceTestGit(t, r.Root, "config", "core.excludesFile")
				if e := os.WriteFile(path, []byte("secret\n"), 0600); e != nil {
					t.Fatal(e)
				}
				workspaceTestWrite(t, r.Root, "secret", []byte("data"))
			case "unique":
				workspaceTestWrite(t, r.Root, "file.txt", []byte("commit"))
				workspaceTestGit(t, r.Root, "add", "file.txt")
				workspaceTestGit(t, r.Root, "commit", "-m", "unique")
			case "embedded":
				if e := os.Mkdir(filepath.Join(r.Root, "embedded"), 0700); e != nil {
					t.Fatal(e)
				}
				workspaceTestGit(t, filepath.Join(r.Root, "embedded"), "init")
			}
			v, e := m.ReviewRemoval(ctx, r.Project, r.ID)
			if e != nil {
				if kind == "embedded" && errors.Is(e, ErrWorkspaceOwnership) {
					return
				}
				t.Fatal(e)
			}
			if v.Eligible {
				t.Fatal("unsafe eligible", kind)
			}
			if e = m.Remove(ctx, r.Project, r.ID, v.Digest, true); e == nil {
				t.Fatal("removed unsafe")
			}
		})
	}
	t.Run("stale and confirmation", func(t *testing.T) {
		_, m, r := integrationFixture(t)
		v, e := m.ReviewRemoval(ctx, r.Project, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		if e = m.Remove(ctx, r.Project, r.ID, v.Digest, false); e == nil {
			t.Fatal("no confirmation")
		}
		workspaceTestWrite(t, r.Root, "late", []byte("data"))
		if e = m.Remove(ctx, r.Project, r.ID, v.Digest, true); !errors.Is(e, ErrWorkspaceStale) {
			t.Fatal(e)
		}
	})
	t.Run("safe retains record and branch", func(t *testing.T) {
		root, m, r := integrationFixture(t)
		workspaceTestWrite(t, r.Root, "file.txt", []byte("private"))
		workspaceTestGit(t, r.Root, "add", "file.txt")
		workspaceTestGit(t, r.Root, "commit", "-m", "private")
		if e := m.CreateBranch(ctx, r.Project, r.ID, "preserve"); e != nil {
			t.Fatal(e)
		}
		if e := m.BindSession(ctx, r.Project, r.ID, SessionBinding{SessionID: "session", UserID: "user", AppName: "code"}); e != nil {
			t.Fatal(e)
		}
		v, e := m.ReviewRemoval(ctx, r.Project, r.ID)
		if e != nil || !v.Eligible {
			t.Fatal(e, v)
		}
		if e = m.Remove(ctx, r.Project, r.ID, v.Digest, true); e != nil {
			t.Fatal(e)
		}
		if _, e = os.Stat(r.Root); !os.IsNotExist(e) {
			t.Fatal("checkout remains", e)
		}
		record, e := m.Get(ctx, r.Project, r.ID)
		if e != nil || record.State != WorkspaceRemoved || len(record.Sessions) != 1 {
			t.Fatal(e, record)
		}
		workspaceTestGit(t, root, "rev-parse", "refs/heads/preserve")
	})
	t.Run("abandoned detached commit", func(t *testing.T) {
		_, m, r := integrationFixture(t)
		workspaceTestWrite(t, r.Root, "file.txt", []byte("private"))
		workspaceTestGit(t, r.Root, "add", "file.txt")
		workspaceTestGit(t, r.Root, "commit", "-m", "private")
		abandoned := workspaceTestGit(t, r.Root, "rev-parse", "HEAD")
		workspaceTestGit(t, r.Root, "checkout", "--detach", r.BaseCommit)
		v, e := m.ReviewRemoval(ctx, r.Project, r.ID)
		if e != nil || v.Eligible {
			t.Fatal("abandoned commit allowed removal", e, v)
		}
		workspaceTestGit(t, r.Root, "branch", "preserve-abandoned", abandoned)
		v, e = m.ReviewRemoval(ctx, r.Project, r.ID)
		if e != nil || !v.Eligible {
			t.Fatal("named ref did not preserve abandoned commit", e, v)
		}
	})
	t.Run("stale ignored data", func(t *testing.T) {
		root, m, r := integrationFixture(t)
		ignore := filepath.Join(workspaceTestDir(t), "ignore")
		if e := os.WriteFile(ignore, []byte("secret\n"), 0600); e != nil {
			t.Fatal(e)
		}
		workspaceTestGit(t, root, "config", "core.excludesFile", ignore)
		v, e := m.ReviewRemoval(ctx, r.Project, r.ID)
		if e != nil || !v.Eligible {
			t.Fatal(e, v)
		}
		workspaceTestWrite(t, r.Root, "secret", []byte("private"))
		if e = m.Remove(ctx, r.Project, r.ID, v.Digest, true); !errors.Is(e, ErrWorkspaceStale) {
			t.Fatal("late ignored data did not invalidate confirmation", e)
		}
	})
	t.Run("foreign symlink", func(t *testing.T) {
		_, m, r := integrationFixture(t)
		moved := r.Root + "-moved"
		if e := os.Rename(r.Root, moved); e != nil {
			t.Fatal(e)
		}
		if e := os.Symlink(moved, r.Root); e != nil {
			t.Fatal(e)
		}
		if _, e := m.ReviewRemoval(ctx, r.Project, r.ID); !errors.Is(e, ErrWorkspaceOwnership) {
			t.Fatal(e)
		}
	})
}
