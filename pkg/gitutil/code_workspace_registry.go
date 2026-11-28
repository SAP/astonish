package gitutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/gofrs/flock"
)

var workspaceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func validWorkspaceID(id string) bool { return id != "" && workspaceIDPattern.MatchString(id) }
func checkWorkspaceFile(path string) error {
	st, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%w: unsafe registry/lock file %s", ErrWorkspaceOwnership, path)
	}
	return nil
}

type workspaceRegistry struct {
	Version int                        `json:"version"`
	Project ProjectIdentity            `json:"project"`
	Records map[string]WorkspaceRecord `json:"records"`
}

func (m *CodeWorkspaceManager) transaction(ctx context.Context, p ProjectIdentity, mutate func(*workspaceRegistry) error, write bool) error {
	if !validWorkspaceID(p.ID) {
		return ErrWorkspaceOwnership
	}
	dir := filepath.Join(m.root, p.ID)
	if e := secureWorkspaceDir(dir); e != nil {
		return e
	}
	lockpath := filepath.Join(dir, "registry.lock")
	if e := checkWorkspaceFile(lockpath); e != nil {
		return e
	}
	l := flock.New(lockpath, flock.SetPermissions(0600))
	ok, e := l.TryLockContext(ctx, 20*time.Millisecond)
	if e != nil {
		return e
	}
	if !ok {
		return ErrWorkspaceBusy
	}
	defer l.Unlock()
	path := filepath.Join(dir, "registry.json")
	if e = checkWorkspaceFile(path); e != nil {
		return e
	}
	reg := workspaceRegistry{Version: 1, Project: p, Records: map[string]WorkspaceRecord{}}
	data, e := os.ReadFile(path)
	if e == nil {
		if e = json.Unmarshal(data, &reg); e != nil {
			return e
		}
		if reg.Version != 1 || reg.Project.ID != p.ID || reg.Project.CommonDir != p.CommonDir || reg.Records == nil {
			return ErrWorkspaceOwnership
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if e = mutate(&reg); e != nil {
		return e
	}
	if !write {
		return nil
	}
	data, e = json.MarshalIndent(reg, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".registry-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (m *CodeWorkspaceManager) store(ctx context.Context, r WorkspaceRecord, create bool) error {
	if !validWorkspaceID(r.ID) {
		return ErrWorkspaceOwnership
	}
	return m.transaction(ctx, r.Project, func(reg *workspaceRegistry) error {
		_, exists := reg.Records[r.ID]
		if create && exists {
			return ErrWorkspaceOwnership
		}
		if !create && !exists {
			return os.ErrNotExist
		}
		if !create {
			r.Sessions = reg.Records[r.ID].Sessions
		}
		reg.Records[r.ID] = r
		return nil
	}, true)
}
func (m *CodeWorkspaceManager) Get(ctx context.Context, p ProjectIdentity, id string) (*WorkspaceRecord, error) {
	var out WorkspaceRecord
	e := m.transaction(ctx, p, func(reg *workspaceRegistry) error {
		r, ok := reg.Records[id]
		if !ok {
			return os.ErrNotExist
		}
		out = r
		return nil
	}, false)
	if e != nil {
		return nil, e
	}
	return &out, nil
}
func (m *CodeWorkspaceManager) List(ctx context.Context, p ProjectIdentity) ([]WorkspaceRecord, error) {
	out := []WorkspaceRecord{}
	e := m.transaction(ctx, p, func(reg *workspaceRegistry) error {
		for _, r := range reg.Records {
			out = append(out, r)
		}
		return nil
	}, false)
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, e
}
func (m *CodeWorkspaceManager) BindSession(ctx context.Context, p ProjectIdentity, id string, b SessionBinding) error {
	if b.SessionID == "" || b.UserID == "" {
		return errors.New("session binding requires session and user IDs")
	}
	return m.transaction(ctx, p, func(reg *workspaceRegistry) error {
		r, ok := reg.Records[id]
		if !ok {
			return os.ErrNotExist
		}
		if r.State == WorkspaceRemoved || r.State == WorkspaceDeleting {
			return ErrWorkspaceOwnership
		}
		for i, s := range r.Sessions {
			if s.SessionID == b.SessionID && s.UserID == b.UserID && s.AppName == b.AppName {
				r.Sessions[i] = b
				reg.Records[id] = r
				return nil
			}
		}
		r.Sessions = append(r.Sessions, b)
		reg.Records[id] = r
		return nil
	}, true)
}
func (m *CodeWorkspaceManager) FindSession(ctx context.Context, p ProjectIdentity, sessionID string) (*WorkspaceRecord, *SessionBinding, error) {
	records, e := m.List(ctx, p)
	if e != nil {
		return nil, nil, e
	}
	for _, r := range records {
		for _, s := range r.Sessions {
			if s.SessionID == sessionID {
				return &r, &s, nil
			}
		}
	}
	return nil, nil, os.ErrNotExist
}

// SetState only records lifecycle; it never removes a checkout or a branch.
func (m *CodeWorkspaceManager) SetState(ctx context.Context, p ProjectIdentity, id string, state WorkspaceState, lastError string) error {
	return m.transaction(ctx, p, func(reg *workspaceRegistry) error {
		r, ok := reg.Records[id]
		if !ok {
			return os.ErrNotExist
		}
		allowed := false
		switch r.State {
		case WorkspacePreparing:
			allowed = state == WorkspaceReady || state == WorkspaceCleanupBlocked
		case WorkspaceReady, WorkspaceRetained:
			allowed = state == WorkspaceRetained || state == WorkspaceReady || state == WorkspaceCleanupBlocked || state == WorkspaceRemoved
		case WorkspaceCleanupBlocked:
			allowed = state == WorkspaceRemoved || state == WorkspaceCleanupBlocked
		case WorkspaceRemoved:
			allowed = state == WorkspaceRemoved
		}
		if !allowed {
			return fmt.Errorf("invalid workspace state transition %s -> %s", r.State, state)
		}
		r.State = state
		r.LastError = lastError
		reg.Records[id] = r
		return nil
	}, true)
}

// ResolveProject preserves the initially recorded original checkout when called from a managed worktree.
func (m *CodeWorkspaceManager) ResolveProject(ctx context.Context, dir string) (ProjectIdentity, error) {
	p, err := ResolveCodeProject(ctx, dir)
	if err != nil {
		return p, err
	}
	err = m.transaction(ctx, p, func(reg *workspaceRegistry) error { p = reg.Project; return nil }, false)
	return p, err
}

// LookupDirectory discovers only a verified managed checkout, never a similarly named foreign path.
func (m *CodeWorkspaceManager) LookupDirectory(ctx context.Context, dir string) (*WorkspaceRecord, error) {
	p, e := ResolveCodeProject(ctx, dir)
	if e != nil {
		return nil, e
	}
	rs, e := m.List(ctx, p)
	if e != nil {
		return nil, e
	}
	for _, r := range rs {
		if r.Root == p.OriginalRoot {
			if e = m.Validate(ctx, r); e != nil {
				return nil, e
			}
			return &r, nil
		}
	}
	return nil, os.ErrNotExist
}
func workspaceDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
