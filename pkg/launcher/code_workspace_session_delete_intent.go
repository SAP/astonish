package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/gitutil"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/gofrs/flock"
)

// A separate project journal survives both workspace tombstones and session indices.
// It is trusted local coordination data, like owners.json, not a same-user sandbox.
type codeSessionDeletionIntent struct {
	Version        int                             `json:"version"`
	Project        gitutil.ProjectIdentity         `json:"project"`
	StorageDir     string                          `json:"storage_dir"`
	OwnerDirectory string                          `json:"owner_directory"`
	Metas          []persistentsession.SessionMeta `json:"sessions"`
}

var codeDeletionComponent = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func (b *localAgentBackend) validateDeletionIntent(ctx context.Context, id string, intent codeSessionDeletionIntent) error {
	w := b.workspace
	if intent.Version != 1 || intent.Project != w.project || intent.StorageDir != b.fileStore.BaseDir() || len(intent.Metas) == 0 || intent.Metas[0].ID != id {
		return gitutil.ErrWorkspaceOwnership
	}
	p, err := w.manager.ResolveProject(ctx, w.project.OriginalRoot)
	if err != nil || p != w.project {
		return gitutil.ErrWorkspaceOwnership
	}
	user := intent.Metas[0].UserID
	if !codeDeletionComponent.MatchString(user) || codeUserIDForDir(intent.OwnerDirectory) != user {
		return gitutil.ErrWorkspaceOwnership
	}
	owners, err := w.owners(ctx)
	if err != nil {
		return err
	}
	owned := false
	for _, owner := range owners {
		owned = owned || (owner.UserID == user && owner.Directory == intent.OwnerDirectory)
	}
	// Removed managed owner directories no longer exist, but their private project
	// namespace and legacy directory hash remain verifiable after ForgetSessions.
	rel, err := filepath.Rel(filepath.Join(w.manager.Root(), w.project.ID, "worktrees"), intent.OwnerDirectory)
	if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && codeDeletionComponent.MatchString(strings.Split(rel, string(filepath.Separator))[0]) {
		owned = true
	}
	if !owned {
		return gitutil.ErrWorkspaceOwnership
	}
	seen := map[string]bool{}
	for i, meta := range intent.Metas {
		if !codeDeletionComponent.MatchString(meta.ID) || meta.AppName != codeAppName || meta.UserID != user || seen[meta.ID] || (i > 0 && !seen[meta.ParentID]) {
			return gitutil.ErrWorkspaceOwnership
		}
		seen[meta.ID] = true
		live, err := b.fileStore.GetSessionMeta(meta.ID)
		if err != nil && err.Error() != fmt.Sprintf("session %s not found", meta.ID) {
			return err
		}
		if live != nil && (live.AppName != meta.AppName || live.UserID != meta.UserID || live.ParentID != meta.ParentID) {
			return gitutil.ErrWorkspaceOwnership
		}
	}
	for _, meta := range intent.Metas {
		children, err := b.fileStore.ListChildren(meta.ID)
		if err != nil {
			return err
		}
		for _, child := range children {
			if !seen[child.ID] || child.AppName != codeAppName || child.UserID != user {
				return gitutil.ErrWorkspaceOwnership
			}
		}
	}
	return nil
}

func (b *localAgentBackend) deletionIntentPath(id string) string {
	return filepath.Join(b.workspace.manager.Root(), b.workspace.project.ID, "delete-session-"+id+".json")
}

func (b *localAgentBackend) lockDeletionIntent(ctx context.Context, id string) (*flock.Flock, error) {
	if !codeDeletionComponent.MatchString(id) {
		return nil, gitutil.ErrWorkspaceOwnership
	}
	// List validates the manager's private directory ancestry and registry.
	if _, err := b.workspace.manager.List(ctx, b.workspace.project); err != nil {
		return nil, err
	}
	path := b.deletionIntentPath(id)
	for _, name := range []string{path, path + ".lock"} {
		if st, err := os.Lstat(name); err == nil && !st.Mode().IsRegular() {
			return nil, gitutil.ErrWorkspaceOwnership
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	lock := flock.New(path+".lock", flock.SetPermissions(0600))
	ok, err := lock.TryLockContext(ctx, 20*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, gitutil.ErrWorkspaceBusy
	}
	return lock, nil
}

func (b *localAgentBackend) readDeletionIntent(ctx context.Context, id string) (*codeSessionDeletionIntent, error) {
	data, err := os.ReadFile(b.deletionIntentPath(id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var intent codeSessionDeletionIntent
	if err := json.Unmarshal(data, &intent); err != nil {
		return nil, err
	}
	if err := b.validateDeletionIntent(ctx, id, intent); err != nil {
		return nil, fmt.Errorf("invalid session deletion intent: %w", err)
	}
	return &intent, nil
}

func (b *localAgentBackend) writeDeletionIntent(ctx context.Context, id string, metas []persistentsession.SessionMeta) error {
	w := b.workspace
	intent := codeSessionDeletionIntent{Version: 1, Project: w.project, StorageDir: b.fileStore.BaseDir(), Metas: metas}
	owners, err := w.owners(ctx)
	if err != nil {
		return err
	}
	for _, o := range owners {
		if o.UserID == metas[0].UserID {
			intent.OwnerDirectory = o.Directory
			break
		}
	}
	if intent.OwnerDirectory == "" {
		records, err := w.manager.List(ctx, w.project)
		if err != nil {
			return err
		}
		for _, r := range records {
			for _, s := range r.Sessions {
				if s.SessionID == id && s.AppName == metas[0].AppName && s.UserID == metas[0].UserID {
					intent.OwnerDirectory = r.InvocationDir
				}
			}
		}
	}
	if err := b.validateDeletionIntent(ctx, id, intent); err != nil {
		return err
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	path := b.deletionIntentPath(id)
	f, err := os.CreateTemp(filepath.Dir(path), ".delete-session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDeletionIntentDir(path)
}
func syncDeletionIntentDir(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
