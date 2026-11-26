package gitutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sessionCleanupBind(t *testing.T, m *CodeWorkspaceManager, r *WorkspaceRecord, bindings ...SessionBinding) {
	t.Helper()
	for _, b := range bindings {
		if err := m.BindSession(context.Background(), r.Project, r.ID, b); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCodeWorkspaceSessionCleanupDirtyRetryAndForget(t *testing.T) {
	ctx := context.Background()
	root, m, r := integrationFixture(t)
	binding := SessionBinding{SessionID: "session", AppName: "code", UserID: "user", StorageDir: "old-location"}
	sessionCleanupBind(t, m, r, binding)
	originalHEAD := workspaceTestGit(t, root, "rev-parse", "HEAD")
	workspaceTestWrite(t, root, "original-private", []byte("keep"))
	workspaceTestGit(t, root, "branch", "existing")
	workspaceTestWrite(t, r.Root, "file.txt", []byte("unique commit"))
	workspaceTestGit(t, r.Root, "add", "file.txt")
	workspaceTestGit(t, r.Root, "commit", "-m", "unique")
	privateHEAD := workspaceTestGit(t, r.Root, "rev-parse", "HEAD")
	if err := m.CreateBranch(ctx, r.Project, r.ID, "private-preserved"); err != nil {
		t.Fatal(err)
	}
	workspaceTestWrite(t, r.Root, "file.txt", []byte("staged"))
	workspaceTestGit(t, r.Root, "add", "file.txt")
	workspaceTestWrite(t, r.Root, "file.txt", []byte("unstaged"))
	workspaceTestWrite(t, r.Root, "untracked", []byte("discard"))
	workspaceTestWrite(t, r.Root, ".gitignore", []byte("secret\n"))
	workspaceTestWrite(t, r.Root, "secret", []byte("ignored discard"))
	if workspaceTestGit(t, r.Root, "check-ignore", "secret") != "secret" {
		t.Fatal("ignored fixture not ignored")
	}
	receipt := filepath.Join(m.root, r.Project.ID, "applied-"+r.ID+".json")
	if err := os.WriteFile(receipt, []byte("receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	binding.StorageDir = "new-location"
	for attempt := 0; attempt < 2; attempt++ {
		if err := m.DeleteSessionWorkspaces(ctx, r.Project, []SessionBinding{binding}); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if _, err := os.Lstat(r.Root); !os.IsNotExist(err) {
			t.Fatal("checkout remains", err)
		}
		got, err := m.Get(ctx, r.Project, r.ID)
		if err != nil || got.State != WorkspaceRemoved || len(got.Sessions) != 1 || got.Sessions[0].StorageDir != "old-location" {
			t.Fatal("retry tombstone/binding lost", got, err)
		}
		if _, err := os.Stat(receipt); err != nil {
			t.Fatal("receipt removed before forget", err)
		}
	}
	if err := m.BindSession(ctx, r.Project, r.ID, SessionBinding{SessionID: "late", AppName: "code", UserID: "user"}); !errors.Is(err, ErrWorkspaceOwnership) {
		t.Fatal("removed checkout accepted new binding", err)
	}
	lockPath := filepath.Join(m.root, r.Project.ID, r.ID+".writer.lock")
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := m.ForgetSessions(ctx, r.Project, []SessionBinding{binding}); err != nil {
			t.Fatal(err)
		}
		if err := m.DeleteSessionWorkspaces(ctx, r.Project, []SessionBinding{binding}); err != nil {
			t.Fatal("missing record not idempotent", err)
		}
	}
	if _, err := m.Get(ctx, r.Project, r.ID); !os.IsNotExist(err) {
		t.Fatal("empty removed record retained", err)
	}
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatal("receipt retained", err)
	}
	if refs := workspaceTestGit(t, root, "for-each-ref", "--format=%(refname)", "refs/astonish/workspaces/"+r.ID+"/"); refs != "" {
		t.Fatal("workspace refs retained", refs)
	}
	after, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("permanent writer lock replaced/deleted", err)
	}
	if workspaceTestGit(t, root, "rev-parse", "HEAD") != originalHEAD || workspaceTestGit(t, root, "rev-parse", "existing") != originalHEAD || workspaceTestGit(t, root, "rev-parse", "private-preserved") != privateHEAD {
		t.Fatal("original checkout or named branches changed")
	}
	if data, err := os.ReadFile(filepath.Join(root, "original-private")); err != nil || string(data) != "keep" {
		t.Fatal("original files changed", err)
	}
}

func TestCodeWorkspaceSessionCleanupSharedAndForeign(t *testing.T) {
	ctx := context.Background()
	root, m, r := integrationFixture(t)
	first := SessionBinding{SessionID: "same", AppName: "code", UserID: "one"}
	second := SessionBinding{SessionID: "same", AppName: "other", UserID: "two"}
	sessionCleanupBind(t, m, r, first, second)
	workspaceTestWrite(t, r.Root, "private", []byte("retain while shared"))
	for _, foreign := range []SessionBinding{
		{SessionID: "same", AppName: "other", UserID: "one"},
		{SessionID: "same", AppName: "code", UserID: "two"},
		{SessionID: "different", AppName: "code", UserID: "one"},
	} {
		if err := m.DeleteSessionWorkspaces(ctx, r.Project, []SessionBinding{foreign}); err != nil {
			t.Fatal(err)
		}
		if err := m.ForgetSessions(ctx, r.Project, []SessionBinding{foreign}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.Get(ctx, r.Project, r.ID)
	if err != nil || len(got.Sessions) != 2 {
		t.Fatal("foreign identity changed bindings", got, err)
	}
	if err := m.DeleteSessionWorkspaces(ctx, r.Project, []SessionBinding{first}); err != nil {
		t.Fatal(err)
	}
	if err := m.ForgetSessions(ctx, r.Project, []SessionBinding{first}); err != nil {
		t.Fatal(err)
	}
	got, err = m.Get(ctx, r.Project, r.ID)
	if err != nil || got.State != WorkspaceReady || len(got.Sessions) != 1 || got.Sessions[0] != second {
		t.Fatal("shared checkout/bindings not retained", got, err)
	}
	if _, err := os.Stat(filepath.Join(r.Root, "private")); err != nil {
		t.Fatal("shared private data deleted", err)
	}
	if refs := workspaceTestGit(t, root, "for-each-ref", "--format=%(refname)", "refs/astonish/workspaces/"+r.ID+"/"); refs == "" {
		t.Fatal("live shared workspace refs removed")
	}
	if err := m.DeleteSessionWorkspaces(ctx, r.Project, []SessionBinding{second}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Root); !os.IsNotExist(err) {
		t.Fatal("last-owned checkout retained", err)
	}
	if err := m.ForgetSessions(ctx, r.Project, []SessionBinding{second}); err != nil {
		t.Fatal(err)
	}
}

func TestCodeWorkspaceSessionCleanupInterleavedLastOwners(t *testing.T) {
	ctx := context.Background()
	_, m, r := integrationFixture(t)
	a := SessionBinding{SessionID: "a", AppName: "code", UserID: "user"}
	b := SessionBinding{SessionID: "b", AppName: "code", UserID: "user"}
	sessionCleanupBind(t, m, r, a, b)
	// Deterministic schedule: both delete passes see the other live owner.
	for _, owner := range []SessionBinding{a, b} {
		if err := m.DeleteSessionWorkspaces(ctx, r.Project, []SessionBinding{owner}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.ForgetSessions(ctx, r.Project, []SessionBinding{a}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Root); err != nil {
		t.Fatal("shared checkout removed", err)
	}
	if err := m.ForgetSessions(ctx, r.Project, []SessionBinding{b}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(r.Root); !os.IsNotExist(err) {
		t.Fatal("last owner left orphan", err)
	}
	if _, err := m.Get(ctx, r.Project, r.ID); !os.IsNotExist(err) {
		t.Fatal("last owner record retained", err)
	}
}

func TestCodeWorkspaceSessionCleanupInterruptedDeleting(t *testing.T) {
	for _, kind := range []string{"present", "removed", "foreign-root", "foreign-gitdir", "registered", "gitdir-present"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			root, m, r := integrationFixture(t)
			b := SessionBinding{SessionID: "session", AppName: "code", UserID: "user"}
			sessionCleanupBind(t, m, r, b)
			if err := m.transaction(ctx, r.Project, func(reg *workspaceRegistry) error {
				pending := reg.Records[r.ID]
				pending.State = WorkspaceDeleting
				reg.Records[r.ID] = pending
				return nil
			}, true); err != nil {
				t.Fatal(err)
			}
			if err := m.BindSession(ctx, r.Project, r.ID, b); !errors.Is(err, ErrWorkspaceOwnership) {
				t.Fatal("deleting accepted binding", err)
			}
			if lease, err := m.AcquireLease(ctx, r.Project, r.ID); !errors.Is(err, ErrWorkspaceOwnership) {
				if lease != nil {
					lease.Close()
				}
				t.Fatal("deleting accepted writer", err)
			}
			switch kind {
			case "removed", "foreign-gitdir", "gitdir-present":
				workspaceTestGit(t, root, "worktree", "remove", "--force", "--", r.Root)
			case "registered":
				if err := os.Rename(r.Root, r.Root+"-moved"); err != nil {
					t.Fatal(err)
				}
			case "foreign-root":
				if err := os.Rename(r.Root, r.Root+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root, r.Root); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "gitdir-present" {
				if err := os.MkdirAll(r.GitDir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "foreign-gitdir" {
				if err := m.transaction(ctx, r.Project, func(reg *workspaceRegistry) error {
					bad := reg.Records[r.ID]
					bad.GitDir = filepath.Join(root, "foreign")
					reg.Records[r.ID] = bad
					return nil
				}, true); err != nil {
					t.Fatal(err)
				}
			}
			err := m.DeleteSessionWorkspaces(ctx, r.Project, []SessionBinding{b})
			if kind != "present" && kind != "removed" {
				if !errors.Is(err, ErrWorkspaceOwnership) {
					t.Fatal("ambiguous/foreign path accepted", err)
				}
				got, getErr := m.Get(ctx, r.Project, r.ID)
				if getErr != nil || got.State != WorkspaceDeleting || len(got.Sessions) != 1 {
					t.Fatal("recovery evidence lost", got, getErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := m.Get(ctx, r.Project, r.ID)
			if err != nil || got.State != WorkspaceRemoved || len(got.Sessions) != 1 {
				t.Fatal("recovery tombstone missing", got, err)
			}
			if _, err := os.Lstat(r.Root); !os.IsNotExist(err) {
				t.Fatal("recovery checkout retained", err)
			}
		})
	}
}

func TestCodeWorkspaceSessionCleanupGuards(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"writer", "journal", "git-locked", "symlink", "original-path"} {
		t.Run(kind, func(t *testing.T) {
			root, m, r := integrationFixture(t)
			binding := SessionBinding{SessionID: "session", AppName: "code", UserID: "user"}
			sessionCleanupBind(t, m, r, binding)
			want := ErrWorkspaceOwnership
			switch kind {
			case "writer":
				lease, err := m.AcquireLease(ctx, r.Project, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				want = ErrWorkspaceBusy
			case "journal":
				if err := os.WriteFile(filepath.Join(m.root, r.Project.ID, "apply-journal.json"), []byte("pending"), 0600); err != nil {
					t.Fatal(err)
				}
				want = ErrWorkspaceRecoveryRequired
				if err := m.ForgetSessions(ctx, r.Project, []SessionBinding{binding}); !errors.Is(err, want) {
					t.Fatal("forgot session during recovery", err)
				}
			case "git-locked":
				workspaceTestGit(t, root, "worktree", "lock", r.Root)
			case "symlink":
				if err := os.Rename(r.Root, r.Root+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(r.Root+"-moved", r.Root); err != nil {
					t.Fatal(err)
				}
			case "original-path":
				if err := m.transaction(ctx, r.Project, func(reg *workspaceRegistry) error {
					bad := reg.Records[r.ID]
					bad.Root = root
					reg.Records[r.ID] = bad
					return nil
				}, true); err != nil {
					t.Fatal(err)
				}
			}
			err := m.DeleteSessionWorkspaces(ctx, r.Project, []SessionBinding{binding})
			if kind == "git-locked" {
				if err == nil || !strings.Contains(err.Error(), "locked") {
					t.Fatal("single force did not refuse Git lock", err)
				}
			} else if !errors.Is(err, want) {
				t.Fatalf("want %v: %v", want, err)
			}
			if _, err := os.Lstat(r.Root); err != nil {
				t.Fatal("refused checkout removed", err)
			}
			got, err := m.Get(ctx, r.Project, r.ID)
			if err != nil || got.State == WorkspaceRemoved || len(got.Sessions) != 1 {
				t.Fatal("refused binding/tombstone changed", got, err)
			}
			if kind == "git-locked" {
				if got.State != WorkspaceDeleting {
					t.Fatal("missing durable deleting intent", got.State)
				}
				workspaceTestGit(t, root, "worktree", "unlock", r.Root)
				if err := m.DeleteSessionWorkspaces(ctx, r.Project, []SessionBinding{binding}); err != nil {
					t.Fatal("retry after Git unlock", err)
				}
			}
		})
	}
}
