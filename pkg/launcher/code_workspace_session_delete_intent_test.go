package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tui/backend"
	adksession "google.golang.org/adk/session"
)

type deletionFailureService struct {
	adksession.Service
	after func(*adksession.DeleteRequest) error
}

func (s deletionFailureService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	if err := s.Service.Delete(ctx, req); err != nil {
		return err
	}
	return s.after(req)
}

func TestCodeSessionDeletionRejectsForgedIntent(t *testing.T) {
	for _, forged := range []string{"project", "user", "child-app", "child-parent", "storage", "path", "omitted-child"} {
		t.Run(forged, func(t *testing.T) {
			ctx := context.Background()
			dir := workspaceLaunchRepo(t)
			w, err := resolveCodeWorkspace(ctx, &CodeConfig{WorkingDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer w.close()
			b := newFileStoreBackend(t, t.TempDir(), w.owner)
			b.workspace, b.workingDir = w, dir
			id := seedSession(t, b, "Keep safe")
			child, err := b.fileStore.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: w.owner, State: map[string]any{persistentsession.StateKeyParentID: id}})
			if err != nil {
				t.Fatal(err)
			}
			rootMeta, _ := b.fileStore.GetSessionMeta(id)
			childMeta, _ := b.fileStore.GetSessionMeta(child.Session.ID())
			if err := b.writeDeletionIntent(ctx, id, []persistentsession.SessionMeta{*rootMeta, *childMeta}); err != nil {
				t.Fatal(err)
			}
			intent, err := b.readDeletionIntent(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			switch forged {
			case "project":
				intent.Project.ID = "foreign-project"
			case "user":
				for i := range intent.Metas {
					intent.Metas[i].UserID = "foreign-user"
				}
			case "child-app":
				intent.Metas[1].AppName = "foreign-app"
			case "child-parent":
				intent.Metas[1].ParentID = "unrelated-session"
			case "storage":
				intent.StorageDir = t.TempDir()
			case "omitted-child":
				intent.Metas = intent.Metas[:1]
			case "path":
				intent.Metas[1].ID = "../foreign-session"
			}
			data, _ := json.Marshal(intent)
			if err := os.WriteFile(b.deletionIntentPath(id), data, 0600); err != nil {
				t.Fatal(err)
			}
			metas, err := b.projectSessionMetas(ctx)
			if err != nil || len(metas) != 1 || metas[0].ID != id {
				t.Fatalf("forged intent blocked durable metadata discovery: %+v %v", metas, err)
			}
			if err := b.DeleteSession(ctx, id); err == nil {
				t.Fatal("forged intent accepted")
			}
			for _, sid := range []string{id, child.Session.ID()} {
				if meta, err := b.fileStore.GetSessionMeta(sid); err != nil || meta == nil {
					t.Fatalf("forged intent deleted session: %s %v", sid, err)
				}
			}
		})
	}
}
func TestCodeSessionDeletionDiscoveryIsolatesInvalidIntent(t *testing.T) {
	for _, invalid := range []string{"stale-storage", "malformed", "version", "filename", "symlink"} {
		t.Run(invalid, func(t *testing.T) {
			ctx := context.Background()
			dir := workspaceLaunchRepo(t)
			w, err := resolveCodeWorkspace(ctx, &CodeConfig{WorkingDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer w.close()
			old := newFileStoreBackend(t, t.TempDir(), w.owner)
			old.workspace, old.workingDir = w, dir
			pendingID := seedSession(t, old, "Old pending deletion")
			oldMeta, err := old.fileStore.GetSessionMeta(pendingID)
			if err != nil {
				t.Fatal(err)
			}
			if err := old.writeDeletionIntent(ctx, pendingID, []persistentsession.SessionMeta{*oldMeta}); err != nil {
				t.Fatal(err)
			}
			path := old.deletionIntentPath(pendingID)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch invalid {
			case "malformed":
				data = []byte("{invalid")
			case "version":
				var intent codeSessionDeletionIntent
				if err := json.Unmarshal(data, &intent); err != nil {
					t.Fatal(err)
				}
				intent.Version = 99
				data, err = json.Marshal(intent)
				if err != nil {
					t.Fatal(err)
				}
			case "filename":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				pendingID = "bad.id"
				path = old.deletionIntentPath(pendingID)
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "intent.json")
				if err := os.WriteFile(target, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if invalid != "symlink" {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			b := newFileStoreBackend(t, t.TempDir(), w.owner)
			b.workspace, b.workingDir = w, dir
			validID := seedSession(t, b, "Delete from new store")
			keepID := seedSession(t, b, "Keep unrelated metadata")
			keepMeta, err := b.fileStore.GetSessionMeta(keepID)
			if err != nil {
				t.Fatal(err)
			}
			metas, err := b.projectSessionMetas(ctx)
			if err != nil || len(metas) != 2 {
				t.Fatalf("invalid intent blocked new store discovery: %+v %v", metas, err)
			}
			if err := b.DeleteSession(ctx, pendingID); err == nil {
				t.Fatal("invalid intent accepted for deletion")
			}
			if err := b.DeleteSession(ctx, validID); err != nil {
				t.Fatalf("invalid intent blocked unrelated deletion: %v", err)
			}
			if meta, err := b.fileStore.GetSessionMeta(validID); meta != nil || (err != nil && !strings.Contains(err.Error(), "not found")) {
				t.Fatalf("valid session retained: %+v %v", meta, err)
			}
			if got, err := b.fileStore.GetSessionMeta(keepID); err != nil || !reflect.DeepEqual(got, keepMeta) {
				t.Fatalf("unrelated metadata changed: %+v %v", got, err)
			}
			if got, err := old.fileStore.GetSessionMeta(oldMeta.ID); err != nil || !reflect.DeepEqual(got, oldMeta) {
				t.Fatalf("old store metadata changed: %+v %v", got, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != string(data) {
				t.Fatalf("invalid intent modified: %q %v", got, err)
			}
		})
	}
}

func TestCodeSessionDeletionDurableTreeRetry(t *testing.T) {
	for _, failure := range []string{"partial-child-index", "post-index-forget"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			dir := workspaceLaunchRepo(t)
			cfg := &CodeConfig{WorkingDir: dir, Workspace: backend.WorkspaceIndependent}
			w, err := resolveCodeWorkspace(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer w.close()
			b := newFileStoreBackend(t, t.TempDir(), w.owner)
			b.workspace, b.workingDir = w, cfg.WorkingDir
			id := seedSession(t, b, "Durable tree retry")
			ids := []string{id}
			for i := 0; i < 2; i++ {
				child, err := b.fileStore.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: w.owner, State: map[string]any{persistentsession.StateKeyParentID: ids[len(ids)-1]}})
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, child.Session.ID())
			}
			for _, sid := range ids {
				if err := b.bindCodeWorkspaceSession(ctx, sid); err != nil {
					t.Fatal(err)
				}
			}
			root := w.record.Root
			workspaceLaunchGit(t, dir, "branch", "keep-durable-retry")
			branches := workspaceLaunchGit(t, dir, "show-ref", "--heads")
			status := workspaceLaunchGit(t, dir, "status", "--porcelain")
			files := workspaceLaunchGit(t, dir, "diff", "HEAD")
			w.close()
			current, err := resolveCodeWorkspace(ctx, &CodeConfig{WorkingDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer current.close()
			b.workspace, b.workingDir, b.userID = current, dir, current.owner
			obstruction := filepath.Join(current.manager.Root(), current.project.ID, "apply-journal.json")
			b.sessionSvc = deletionFailureService{Service: b.fileStore, after: func(req *adksession.DeleteRequest) error {
				if failure == "partial-child-index" && req.SessionID == ids[2] {
					return fmt.Errorf("injected partial index failure")
				}
				if failure == "post-index-forget" && req.SessionID == id {
					return os.WriteFile(obstruction, []byte("pending"), 0600)
				}
				return nil
			}}
			if err := b.DeleteSession(ctx, id); err == nil {
				t.Fatal("expected injected failure")
			}
			if meta, err := b.fileStore.GetSessionMeta(ids[2]); meta != nil || (err != nil && !strings.Contains(err.Error(), "not found")) {
				t.Fatalf("child index did not disappear: %+v %v", meta, err)
			}
			if failure == "post-index-forget" {
				if meta, err := b.fileStore.GetSessionMeta(id); meta != nil || (err != nil && !strings.Contains(err.Error(), "not found")) {
					t.Fatalf("root index did not disappear: %+v %v", meta, err)
				}
				if err := os.Remove(obstruction); err != nil {
					t.Fatal(err)
				}
			}
			current.close()
			fresh, err := resolveCodeWorkspace(ctx, &CodeConfig{WorkingDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.close()
			store, err := persistentsession.NewFileStore(b.fileStore.BaseDir())
			if err != nil {
				t.Fatal(err)
			}
			reopened := &localAgentBackend{workspace: fresh, workingDir: dir, userID: fresh.owner, fileStore: store, sessionSvc: store}
			metas, err := reopened.projectSessionMetas(ctx)
			if err != nil || len(metas) != 1 || metas[0].ID != id {
				t.Fatalf("pending root lost discovery: %+v %v", metas, err)
			}
			intent, err := reopened.readDeletionIntent(ctx, id)
			if err != nil || intent == nil || len(intent.Metas) != len(ids) {
				t.Fatalf("complete intent lost: %+v %v", intent, err)
			}
			for i, meta := range intent.Metas {
				if meta.ID != ids[i] || meta.UserID != w.owner || meta.AppName != codeAppName {
					t.Fatalf("binding identity changed: %+v", intent.Metas)
				}
			}
			if err := reopened.DeleteSession(ctx, id); err != nil {
				t.Fatalf("fresh retry failed: %v", err)
			}
			records, err := fresh.manager.List(ctx, fresh.project)
			if err != nil || len(records) != 0 {
				t.Fatalf("full tree bindings retained: %+v %v", records, err)
			}
			for _, sid := range ids {
				if meta, err := store.GetSessionMeta(sid); meta != nil || (err != nil && !strings.Contains(err.Error(), "not found")) {
					t.Fatalf("session retained: %s %+v %v", sid, meta, err)
				}
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("workspace retained: %v", err)
			}
			if workspaceLaunchGit(t, dir, "show-ref", "--heads") != branches || workspaceLaunchGit(t, dir, "diff", "HEAD") != files || workspaceLaunchGit(t, dir, "status", "--porcelain") != status {
				t.Fatal("original branches/files/index changed")
			}
			// Unknown IDs cannot be used as arbitrary cleanup requests.
			if err := reopened.DeleteSession(ctx, "unknown-id"); err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("unknown cleanup accepted: %v", err)
			}
		})
	}
}
