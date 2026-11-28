package gitutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// workspaceSessionKey intentionally excludes StorageDir: storage location is not
// part of a session's identity and can change between cleanup attempts.
type workspaceSessionKey struct{ session, app, user string }

func workspaceSessionSet(bindings []SessionBinding) map[workspaceSessionKey]bool {
	set := make(map[workspaceSessionKey]bool, len(bindings))
	for _, b := range bindings {
		set[workspaceSessionKey{b.SessionID, b.AppName, b.UserID}] = true
	}
	return set
}

func workspaceSessionOwnership(r WorkspaceRecord, set map[workspaceSessionKey]bool) (matched, exclusive bool) {
	exclusive = len(r.Sessions) != 0
	for _, b := range r.Sessions {
		if set[workspaceSessionKey{b.SessionID, b.AppName, b.UserID}] {
			matched = true
		} else {
			exclusive = false
		}
	}
	return
}

func (m *CodeWorkspaceManager) sessionCleanupRecord(p ProjectIdentity, id string, r WorkspaceRecord) error {
	if !validWorkspaceID(id) || r.ID != id || r.Version != 1 || r.Project != p || r.Root != filepath.Join(m.root, p.ID, "worktrees", id) || r.Root == p.OriginalRoot {
		return ErrWorkspaceOwnership
	}
	return nil
}

func sessionCleanupJournal(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return ErrWorkspaceRecoveryRequired
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteSessionWorkspaces destructively removes only exclusively owned managed
// checkouts. Unlike Remove, dirty and ignored files are explicitly discarded.
// Bindings remain on removed tombstones until transcript cleanup succeeds and
// ForgetSessions is called; retrying this operation never removes a checkout twice.
func (m *CodeWorkspaceManager) DeleteSessionWorkspaces(ctx context.Context, p ProjectIdentity, bindings []SessionBinding) error {
	if len(bindings) == 0 {
		return nil
	}
	lock, journal, err := m.integrationLock(ctx, p)
	if err != nil {
		return fmt.Errorf("delete session workspaces: integration lock: %w", err)
	}
	defer lock.Unlock()
	if err = sessionCleanupJournal(journal); err != nil {
		return fmt.Errorf("delete session workspaces: pending apply: %w", err)
	}
	return m.deleteSessionWorkspacesLocked(ctx, p, bindings, journal)
}

// Caller holds the integration lock across deletion and binding changes.
func (m *CodeWorkspaceManager) deleteSessionWorkspacesLocked(ctx context.Context, p ProjectIdentity, bindings []SessionBinding, journal string) error {
	set := workspaceSessionSet(bindings)
	records, err := m.List(ctx, p)
	if err != nil {
		return err
	}
	for _, candidate := range records {
		matched, exclusive := workspaceSessionOwnership(candidate, set)
		if !matched || !exclusive || candidate.State == WorkspaceRemoved {
			continue
		}
		if err := m.sessionCleanupRecord(p, candidate.ID, candidate); err != nil {
			return err
		}
		// Raw writer acquisition permits recovery after the checkout disappeared.
		// Acquire outside the registry transaction, then reload under its lock.
		lease, err := m.sessionCleanupLease(p, candidate.ID)
		if err != nil {
			return fmt.Errorf("workspace %s: writer lease: %w", candidate.ID, err)
		}
		err = m.deleteSessionWorkspaceLeased(ctx, p, candidate.ID, set, journal)
		closeErr := lease.Close()
		if err != nil {
			return fmt.Errorf("workspace %s: %w", candidate.ID, err)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (m *CodeWorkspaceManager) sessionCleanupLease(p ProjectIdentity, id string) (*WorkspaceLease, error) {
	if !validWorkspaceID(p.ID) || !validWorkspaceID(id) {
		return nil, ErrWorkspaceOwnership
	}
	path := filepath.Join(m.root, p.ID, id+".writer.lock")
	if err := checkWorkspaceFile(path); err != nil {
		return nil, err
	}
	lock := flock.New(path, flock.SetPermissions(0600))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrWorkspaceBusy
	}
	return &WorkspaceLease{lock: lock}, nil
}

// An absent root alone does not prove removal: the recorded admin directory
// and Git's registration must also be absent. Never recursively delete paths.
func (m *CodeWorkspaceManager) sessionCleanupStatus(ctx context.Context, p ProjectIdentity, id string, r WorkspaceRecord) (bool, error) {
	if err := m.sessionCleanupRecord(p, id, r); err != nil {
		return false, err
	}
	original, err := ResolveCodeProject(ctx, p.OriginalRoot)
	if err != nil || original != p {
		return false, fmt.Errorf("original checkout identity: %w", ErrWorkspaceOwnership)
	}
	if r.State != WorkspaceDeleting {
		return false, m.Validate(ctx, r)
	}
	if filepath.Dir(r.GitDir) != filepath.Join(p.CommonDir, "worktrees") || !validWorkspaceID(filepath.Base(r.GitDir)) {
		return false, ErrWorkspaceOwnership
	}
	if err := secureWorkspaceDir(filepath.Dir(r.Root)); err != nil {
		return false, err
	}
	if _, err := os.Lstat(r.Root); err == nil {
		copy := r
		copy.State = WorkspaceReady
		return false, m.Validate(ctx, copy)
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if _, err := os.Lstat(r.GitDir); !os.IsNotExist(err) {
		return false, ErrWorkspaceOwnership
	}
	for path := filepath.Dir(r.GitDir); ; path = filepath.Dir(path) {
		if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return false, ErrWorkspaceOwnership
		} else if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	listed, err := workspaceGit(ctx, p.OriginalRoot, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, err
	}
	for _, field := range strings.Split(listed, "\x00") {
		if field == "worktree "+r.Root {
			return false, ErrWorkspaceOwnership
		}
	}
	return true, nil
}

func (m *CodeWorkspaceManager) deleteSessionWorkspaceLeased(ctx context.Context, p ProjectIdentity, id string, set map[workspaceSessionKey]bool, journal string) error {
	pending := false
	// Persist intent before Git removal; if the final write fails, retry can
	// prove whether Git completed using the retained structural identity.
	if err := m.transaction(ctx, p, func(reg *workspaceRegistry) error {
		r, exists := reg.Records[id]
		if !exists || r.State == WorkspaceRemoved {
			return nil
		}
		matched, exclusive := workspaceSessionOwnership(r, set)
		if !matched || !exclusive {
			return nil
		}
		if err := sessionCleanupJournal(journal); err != nil {
			return err
		}
		if _, err := m.sessionCleanupStatus(ctx, p, id, r); err != nil {
			return err
		}
		r.State = WorkspaceDeleting
		reg.Records[id] = r
		pending = true
		return nil
	}, true); err != nil {
		return err
	}
	if !pending {
		return nil
	}
	return m.transaction(ctx, p, func(reg *workspaceRegistry) error {
		r, exists := reg.Records[id]
		if !exists || r.State != WorkspaceDeleting {
			return ErrWorkspaceOwnership
		}
		matched, exclusive := workspaceSessionOwnership(r, set)
		if !matched || !exclusive {
			return ErrWorkspaceOwnership
		}
		if err := sessionCleanupJournal(journal); err != nil {
			return err
		}
		removed, err := m.sessionCleanupStatus(ctx, p, id, r)
		if err != nil {
			return err
		}
		if !removed {
			// Single force discards dirty files but refuses Git-locked worktrees.
			if _, err := workspaceGit(ctx, p.OriginalRoot, nil, "worktree", "remove", "--force", "--", r.Root); err != nil {
				return err
			}
		}
		r.State = WorkspaceRemoved
		r.LastError = ""
		reg.Records[id] = r
		return nil
	}, true)
}

// ForgetSessions removes only exact binding identities. Removed records are
// dropped after their last binding is forgotten; live shared checkouts survive.
// Permanent writer lock files are deliberately retained to avoid inode races.
func (m *CodeWorkspaceManager) ForgetSessions(ctx context.Context, p ProjectIdentity, bindings []SessionBinding) error {
	if len(bindings) == 0 {
		return nil
	}
	lock, journal, err := m.integrationLock(ctx, p)
	if err != nil {
		return fmt.Errorf("forget sessions: integration lock: %w", err)
	}
	defer lock.Unlock()
	if err = sessionCleanupJournal(journal); err != nil {
		return fmt.Errorf("forget sessions: pending apply: %w", err)
	}
	// Re-evaluate last ownership while holding the same integration lock through
	// deletion and forgetting. Two callers may both have skipped deletion when
	// they still shared a checkout; the last forget must not orphan it.
	if err := m.deleteSessionWorkspacesLocked(ctx, p, bindings, journal); err != nil {
		return fmt.Errorf("forget sessions: last owner cleanup: %w", err)
	}
	set := workspaceSessionSet(bindings)
	err = m.transaction(ctx, p, func(reg *workspaceRegistry) error {
		for id, r := range reg.Records {
			matched, _ := workspaceSessionOwnership(r, set)
			if !matched {
				continue
			}
			if err := m.sessionCleanupRecord(p, id, r); err != nil {
				return fmt.Errorf("workspace %s: %w", id, err)
			}
			remaining := make([]SessionBinding, 0, len(r.Sessions))
			for _, b := range r.Sessions {
				if !set[workspaceSessionKey{b.SessionID, b.AppName, b.UserID}] {
					remaining = append(remaining, b)
				}
			}
			if r.State == WorkspaceRemoved && len(remaining) == 0 {
				original, err := ResolveCodeProject(ctx, p.OriginalRoot)
				if err != nil || original != p {
					return fmt.Errorf("workspace %s: original checkout identity: %w", id, ErrWorkspaceOwnership)
				}
				path := filepath.Join(m.root, p.ID, "applied-"+id+".json")
				if err := checkWorkspaceFile(path); err != nil {
					return fmt.Errorf("workspace %s: apply receipt: %w", id, err)
				}
				for _, name := range []string{"committed", "staged", "full"} {
					if _, err := workspaceGit(ctx, p.OriginalRoot, nil, "update-ref", "-d", "refs/astonish/workspaces/"+id+"/"+name); err != nil {
						return fmt.Errorf("workspace %s: delete %s ref: %w", id, name, err)
					}
				}
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("workspace %s: remove apply receipt: %w", id, err)
				}
				delete(reg.Records, id)
			} else {
				r.Sessions = remaining
				reg.Records[id] = r
			}
		}
		return nil
	}, true)
	if err != nil {
		return fmt.Errorf("forget sessions: %w", err)
	}
	return nil
}
