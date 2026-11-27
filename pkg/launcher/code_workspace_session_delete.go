package launcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/gitutil"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tui/backend"
	adksession "google.golang.org/adk/session"
)

// finishCodeSessionDeletion runs only after the old TUI and its owned runtime
// resources have closed. It never changes the CWD of a running agent.
func finishCodeSessionDeletion(ctx context.Context, request backend.WorkspaceLaunchRequest) error {
	if request.DeleteSessionID == "" {
		return nil
	}
	app, err := config.LoadAppConfig()
	if err != nil {
		return err
	}
	storage, err := codeSessionsDir(app)
	if err != nil {
		return err
	}
	store, err := persistentsession.NewFileStore(storage)
	if err != nil {
		return err
	}
	w, err := resolveCodeWorkspace(ctx, &CodeConfig{WorkingDir: request.WorkingDir})
	if err != nil {
		return err
	}
	if w == nil {
		return fmt.Errorf("session deletion handoff requires a Git project")
	}
	defer w.close()
	checkpoints, err := persistentsession.NewCheckpointStore(storage)
	if err != nil {
		return err
	}
	b := &localAgentBackend{workspace: w, workingDir: request.WorkingDir, userID: codeUserIDForDir(request.WorkingDir), fileStore: store, sessionSvc: store, appConfig: app, checkpoints: checkpoints}
	return b.DeleteSession(ctx, request.DeleteSessionID)
}

// deleteCodeSessionData removes a conversation's full child tree. Workspace
// cleanup precedes transcript removal so a blocked cleanup leaves a retryable
// conversation in /sessions. Deletion is destructive only for owned checkouts.
func (b *localAgentBackend) deleteCodeSessionData(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("session id required")
	}
	metas := []persistentsession.SessionMeta{{ID: id, AppName: codeAppName, UserID: b.effectiveUserID()}}
	if b.fileStore != nil {
		meta, err := b.fileStore.GetSessionMeta(id)
		if err != nil {
			return err
		}
		if meta == nil || meta.AppName != codeAppName {
			return fmt.Errorf("saved Code session not found")
		}
		if b.workspace != nil {
			allowed, err := b.projectSessionMetas(ctx)
			if err != nil {
				return err
			}
			found := false
			for _, m := range allowed {
				if m.ID == id && m.UserID == meta.UserID {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("saved Code session belongs to another project")
			}
		} else if meta.UserID != b.effectiveUserID() {
			return fmt.Errorf("saved Code session belongs to another directory")
		}
		metas[0] = *meta
		seen := map[string]bool{id: true}
		for i := 0; i < len(metas); i++ {
			children, err := b.fileStore.ListChildren(metas[i].ID)
			if err != nil {
				return err
			}
			for _, child := range children {
				if child.AppName != codeAppName || child.UserID != meta.UserID {
					return fmt.Errorf("child Code session ownership mismatch: %s", child.ID)
				}
				if !seen[child.ID] {
					seen[child.ID] = true
					metas = append(metas, child)
				}
			}
		}
	}
	bindings := make([]gitutil.SessionBinding, 0, len(metas))
	deleting := map[string]bool{}
	for _, meta := range metas {
		bindings = append(bindings, gitutil.SessionBinding{SessionID: meta.ID, AppName: meta.AppName, UserID: meta.UserID})
		deleting[meta.ID] = true
	}
	if w := b.workspace; w != nil {
		records, err := w.manager.List(ctx, w.project)
		if err != nil {
			return err
		}
		// The active checkout must keep its writer lease until runtime shutdown.
		// The fresh-process handoff finishes deletion from the original checkout.
		for _, r := range records {
			matched, exclusive := false, len(r.Sessions) > 0
			for _, s := range r.Sessions {
				owned := false
				for _, d := range bindings {
					if d.SessionID == s.SessionID && d.AppName == s.AppName && d.UserID == s.UserID {
						owned = true
						break
					}
				}
				matched = matched || owned
				exclusive = exclusive && owned
			}
			if matched && exclusive && w.record != nil && w.record.ID == r.ID && w.lease != nil {
				return &backend.WorkspaceHandoffError{Request: backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceCurrent, WorkingDir: w.project.OriginalRoot, ProjectRoot: w.project.OriginalRoot, DeleteSessionID: id}}
			}
		}
		if err := w.manager.DeleteSessionWorkspaces(ctx, w.project, bindings); err != nil {
			return fmt.Errorf("workspace cleanup failed; conversation retained for retry: %w", err)
		}
	}
	for _, meta := range metas {
		if b.checkpoints != nil {
			if err := b.checkpoints.DeleteSession(meta.ID); err != nil {
				return fmt.Errorf("remove session checkpoints: %w", err)
			}
		}
		if b.fileStore != nil {
			path := filepath.Join(b.fileStore.BaseDir(), meta.AppName, meta.UserID, meta.ID+".PLAN.md")
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove session plan: %w", err)
			}
		}
	}
	// FileStore historically treats transcript removal as best-effort. Check
	// removal here before dropping the index entry so failures remain retryable.
	if b.fileStore != nil {
		for _, meta := range metas {
			path := filepath.Join(b.fileStore.BaseDir(), meta.AppName, meta.UserID, meta.ID+".jsonl")
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove session transcript: %w", err)
			}
		}
	}
	// Reverse traversal deletes grandchildren before parents. FileStore's legacy
	// cascade alone only removes immediate children.
	for i := len(metas) - 1; i >= 0; i-- {
		meta := metas[i]
		if err := b.sessionSvc.Delete(ctx, &adksession.DeleteRequest{AppName: meta.AppName, UserID: meta.UserID, SessionID: meta.ID}); err != nil {
			return fmt.Errorf("delete saved session %s: %w", meta.ID, err)
		}
	}
	// Keep exact bindings (including removed tombstones) through transcript AND
	// index cleanup: FileStore.Delete can fail while locking/writing its index.
	// A fresh launch from the original checkout must still discover the session
	// after either failure, even though its physical workspace is already gone.
	if w := b.workspace; w != nil {
		if err := w.manager.ForgetSessions(ctx, w.project, bindings); err != nil {
			return fmt.Errorf("workspace metadata cleanup failed after saved session deletion: %w", err)
		}
	}
	b.mu.Lock()
	if deleting[b.sessionID] {
		b.sessionID = ""
		b.usage = nil
		b.resumed = false
	}
	b.mu.Unlock()
	return nil
}
