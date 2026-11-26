package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/gitutil"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tui/backend"
	adksession "google.golang.org/adk/session"
)

func TestCodeSessionDeletionWorkspace(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "dedicated", true: "shared"}[shared], func(t *testing.T) {
			dir := workspaceLaunchRepo(t)
			ctx := context.Background()
			cfg := &CodeConfig{WorkingDir: dir, Workspace: backend.WorkspaceIndependent}
			w, err := resolveCodeWorkspace(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer w.close()
			b := newFileStoreBackend(t, filepath.Join(t.TempDir(), "sessions"), w.owner)
			b.checkpoints, err = persistentsession.NewCheckpointStore(b.fileStore.BaseDir())
			if err != nil {
				t.Fatal(err)
			}
			b.workspace, b.workingDir = w, cfg.WorkingDir
			id := seedSession(t, b, "Delete workspace")
			if err := b.bindCodeWorkspaceSession(ctx, id); err != nil {
				t.Fatal(err)
			}
			child, err := b.fileStore.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: w.owner, State: map[string]any{persistentsession.StateKeyParentID: id}})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.bindCodeWorkspaceSession(ctx, child.Session.ID()); err != nil {
				t.Fatal(err)
			}
			grandchild, err := b.fileStore.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: w.owner, State: map[string]any{persistentsession.StateKeyParentID: child.Session.ID()}})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.bindCodeWorkspaceSession(ctx, grandchild.Session.ID()); err != nil {
				t.Fatal(err)
			}
			var other string
			if shared {
				other = seedSession(t, b, "Keep workspace")
				if err := b.bindCodeWorkspaceSession(ctx, other); err != nil {
					t.Fatal(err)
				}
			}
			root := w.record.Root
			if err := os.WriteFile(filepath.Join(root, "file"), []byte("dirty"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := b.checkpoints.Capture(id, 1, filepath.Join(root, "file")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(b.planFilePath(id), []byte("plan"), 0600); err != nil {
				t.Fatal(err)
			}
			w.close()
			current, err := resolveCodeWorkspace(ctx, &CodeConfig{WorkingDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			b.workspace, b.workingDir, b.userID = current, dir, current.owner
			if err := b.DeleteSession(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(b.fileStore.BaseDir(), "checkpoints", id)); !os.IsNotExist(err) {
				t.Fatalf("checkpoints retained: %v", err)
			}
			for _, sid := range []string{id, child.Session.ID(), grandchild.Session.ID()} {
				meta, _ := b.fileStore.GetSessionMeta(sid)
				if meta != nil {
					t.Fatalf("session retained %s: %+v", sid, meta)
				}
				if _, err := os.Stat(filepath.Join(b.fileStore.BaseDir(), codeAppName, w.owner, sid+".jsonl")); !os.IsNotExist(err) {
					t.Fatalf("transcript retained: %v", err)
				}
				if _, err := os.Stat(filepath.Join(b.fileStore.BaseDir(), codeAppName, w.owner, sid+".PLAN.md")); !os.IsNotExist(err) {
					t.Fatalf("plan retained: %v", err)
				}
			}
			if shared {
				if _, err := os.Stat(root); err != nil {
					t.Fatal("shared workspace removed", err)
				}
				if err := b.DeleteSession(ctx, other); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("workspace retained: %v", err)
			}
			records, err := current.manager.List(ctx, current.project)
			if err != nil || len(records) != 0 {
				t.Fatalf("bindings retained: %+v %v", records, err)
			}
			if workspaceLaunchGit(t, dir, "status", "--porcelain") != "" {
				t.Fatal("original modified")
			}
		})
	}
}

func TestCodeSessionDeletionActiveAndBusy(t *testing.T) {
	dir := workspaceLaunchRepo(t)
	ctx := context.Background()
	cfg := &CodeConfig{WorkingDir: dir, Workspace: backend.WorkspaceIndependent}
	w, err := resolveCodeWorkspace(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	b := newFileStoreBackend(t, t.TempDir(), w.owner)
	b.workspace, b.workingDir = w, cfg.WorkingDir
	id := seedSession(t, b, "Active")
	if err := b.bindCodeWorkspaceSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	var handoff *backend.WorkspaceHandoffError
	if err := b.DeleteSession(ctx, id); !errors.As(err, &handoff) || handoff.Request.DeleteSessionID != id || handoff.Request.WorkingDir != w.project.OriginalRoot {
		t.Fatalf("active deletion handoff: %v", err)
	}
	if _, err := os.Stat(w.record.Root); err != nil {
		t.Fatal("removed before shutdown", err)
	}
	current, err := resolveCodeWorkspace(ctx, &CodeConfig{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	b.workspace, b.userID, b.workingDir = current, current.owner, dir
	if err := b.DeleteSession(ctx, id); !errors.Is(err, gitutil.ErrWorkspaceBusy) {
		t.Fatalf("busy deletion: %v", err)
	}
	meta, err := b.fileStore.GetSessionMeta(id)
	if err != nil || meta == nil {
		t.Fatal("failed cleanup lost retry session")
	}
	w.close()
	if err := b.DeleteSession(ctx, id); err != nil {
		t.Fatal(err)
	}
}
