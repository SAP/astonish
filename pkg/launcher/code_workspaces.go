package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/gitutil"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/gofrs/flock"
)

func newLocalCodeWorkspaceManager() (*gitutil.CodeWorkspaceManager, error) {
	dir, e := config.GetConfigDir()
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	dir, e = filepath.EvalSymlinks(dir)
	if e != nil {
		return nil, e
	}
	return gitutil.NewCodeWorkspaceManager(filepath.Join(dir, "code-workspaces"))
}

func codeWorkspaceHandoffArgs(cfg *CodeConfig, r backend.WorkspaceLaunchRequest) ([]string, error) {
	if cfg == nil {
		cfg = &CodeConfig{}
	}
	if !filepath.IsAbs(r.WorkingDir) {
		return nil, fmt.Errorf("workspace destination must be absolute")
	}
	if st, e := os.Stat(r.WorkingDir); e != nil || !st.IsDir() {
		return nil, fmt.Errorf("workspace destination unavailable: %s", r.WorkingDir)
	}
	args := []string{"code", "--dir", r.WorkingDir, "--workspace", "current"}
	if r.SessionID != "" {
		args = append(args, "--resume", r.SessionID)
	}
	if cfg.ThemeID != "" {
		args = append(args, "--theme", string(cfg.ThemeID))
	}
	if cfg.Model != "" {
		model := cfg.Model
		if cfg.Provider != "" {
			model = cfg.Provider + ":" + model
		}
		args = append(args, "--model", model)
	}
	if cfg.DebugMode {
		args = append(args, "--debug")
	}
	if cfg.AutoApprove {
		args = append(args, "--yolo")
	}
	return args, nil
}

type codeWorkspaceContext struct {
	manager *gitutil.CodeWorkspaceManager
	project gitutil.ProjectIdentity
	record  *gitutil.WorkspaceRecord
	lease   *gitutil.WorkspaceLease
	owner   string
}
type codeWorkspaceOwner struct {
	UserID    string `json:"user_id"`
	Directory string `json:"directory"`
}

// Owners are project-scoped directory identities, never a global transcript scan.
func (w *codeWorkspaceContext) owners(ctx context.Context, add ...codeWorkspaceOwner) ([]codeWorkspaceOwner, error) {
	dir := filepath.Join(w.manager.Root(), w.project.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "owners.json")
	for _, p := range []string{dir, path, path + ".lock"} {
		if st, e := os.Lstat(p); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unsafe workspace owner registry: %s", p)
		}
	}
	lock := flock.New(path+".lock", flock.SetPermissions(0600))
	ok, err := lock.TryLockContext(ctx, 10*1000000)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("workspace owner registry busy")
	}
	defer lock.Unlock()
	var out []codeWorkspaceOwner
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	changed := false
	for _, a := range add {
		found := false
		for _, o := range out {
			if o == a {
				found = true
				break
			}
		}
		if !found {
			out = append(out, a)
			changed = true
		}
	}
	if changed {
		data, err = json.Marshal(out)
		if err != nil {
			return nil, err
		}
		f, e := os.CreateTemp(dir, "owners-*.tmp")
		if e != nil {
			return nil, e
		}
		defer os.Remove(f.Name())
		if e = f.Chmod(0600); e == nil {
			_, e = f.Write(data)
		}
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e == nil {
			e = os.Rename(f.Name(), path)
		}
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}

func ValidateCodeWorkspaceOptions(mode backend.WorkspaceMode, base string, copy backend.WorkspaceCopyMode) error {
	if mode == "" {
		mode = backend.WorkspaceCurrent
	}
	switch mode {
	case backend.WorkspaceCurrent, backend.WorkspaceIndependent, backend.WorkspaceAsk:
	default:
		return fmt.Errorf("invalid --workspace %q: use current, independent or ask", mode)
	}
	if copy != "" && copy != backend.WorkspaceCommitted && copy != backend.WorkspaceCurrentWork {
		return fmt.Errorf("invalid --workspace-copy %q: use committed or current-work", copy)
	}
	if mode == backend.WorkspaceCurrent && (base != "" || copy != "") {
		return fmt.Errorf("--workspace-base and --workspace-copy require --workspace independent or ask")
	}
	return nil
}

// resolveCodeWorkspace executes before factory/tool initialization in both launch paths.
func resolveCodeWorkspace(ctx context.Context, cfg *CodeConfig) (*codeWorkspaceContext, error) {
	if err := ValidateCodeWorkspaceOptions(cfg.Workspace, cfg.WorkspaceBase, cfg.WorkspaceCopy); err != nil {
		return nil, err
	}
	dir := cfg.WorkingDir
	if dir == "" {
		var e error
		dir, e = os.Getwd()
		if e != nil {
			return nil, e
		}
	}
	dir, e := filepath.Abs(dir)
	if e != nil {
		return nil, e
	}
	if st, e := os.Stat(dir); e != nil || !st.IsDir() {
		return nil, fmt.Errorf("working directory does not exist: %s", dir)
	}
	cfg.WorkingDir = dir
	m, e := newLocalCodeWorkspaceManager()
	if e != nil {
		return nil, e
	}
	p, e := m.ResolveProject(ctx, dir)
	if e != nil {
		if cfg.Workspace == backend.WorkspaceIndependent {
			return nil, e
		}
		return nil, nil
	}
	w := &codeWorkspaceContext{manager: m, project: p, owner: codeUserIDForDir(dir)}
	// Record the exact invocation path without changing legacy hashes; canonical
	// aliases are additional owners, not replacements for existing transcript paths.
	aliases := []codeWorkspaceOwner{{w.owner, dir}, {codeUserIDForDir(p.OriginalRoot), p.OriginalRoot}}
	if canonical, err := filepath.EvalSymlinks(dir); err == nil {
		aliases = append(aliases, codeWorkspaceOwner{codeUserIDForDir(canonical), canonical})
	}
	if _, e = w.owners(ctx, aliases...); e != nil {
		return nil, e
	}
	if cfg.SessionID != "" {
		r, b, err := m.FindSession(ctx, p, cfg.SessionID)
		if err == nil {
			if b.AppName != codeAppName {
				return nil, fmt.Errorf("workspace session ownership mismatch")
			}
			if err = m.Validate(ctx, *r); err != nil {
				return nil, err
			}
			app, e := config.LoadAppConfig()
			if e != nil {
				return nil, e
			}
			storage, e := codeSessionsDir(app)
			if e != nil {
				return nil, e
			}
			store, e := persistentsession.NewFileStore(storage)
			if e != nil {
				return nil, e
			}
			meta, e := store.GetSessionMeta(cfg.SessionID)
			if e != nil {
				return nil, e
			}
			if meta == nil || meta.AppName != b.AppName || meta.UserID != b.UserID {
				return nil, fmt.Errorf("workspace transcript ownership mismatch")
			}
			dir = r.InvocationDir
			w.record = r
			w.owner = b.UserID
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		} else {
			// Explicit legacy resumes are restricted to registered project owners and
			// verified metadata. This preserves old directory-scoped sessions.
			app, e := config.LoadAppConfig()
			if e != nil {
				return nil, e
			}
			storage, e := codeSessionsDir(app)
			if e != nil {
				return nil, e
			}
			store, e := persistentsession.NewFileStore(storage)
			if e != nil {
				return nil, e
			}
			meta, e := store.GetSessionMeta(cfg.SessionID)
			if e != nil {
				return nil, e
			}
			if meta == nil || meta.AppName != codeAppName {
				return nil, fmt.Errorf("saved Code session not found")
			}
			owners, e := w.owners(ctx)
			if e != nil {
				return nil, e
			}
			found := false
			for _, o := range owners {
				if o.UserID == meta.UserID {
					w.owner = o.UserID
					dir = o.Directory
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("saved Code session belongs to another project")
			}
		}
	} else if cfg.Workspace == backend.WorkspaceIndependent {
		copy := cfg.WorkspaceCopy
		if copy == "" {
			copy = backend.WorkspaceCommitted
		}
		r, err := m.Create(ctx, gitutil.WorkspaceCreateOptions{SourceDir: dir, CopyMode: gitutil.WorkspaceCopyMode(copy), Ref: cfg.WorkspaceBase})
		if err != nil {
			return nil, err
		}
		w.record = r
		dir = r.InvocationDir
		w.project = r.Project
		w.owner = codeUserIDForDir(dir)
	}
	if w.record == nil {
		r, err := m.LookupDirectory(ctx, dir)
		if err == nil {
			w.record = r
			w.project = r.Project
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if w.record != nil {
		if e = m.Validate(ctx, *w.record); e != nil {
			return nil, e
		}
		l, err := m.AcquireLease(ctx, w.project, w.record.ID)
		if err != nil {
			return nil, fmt.Errorf("Code workspace %s is busy or unavailable: %w", dir, err)
		}
		w.lease = l
	}
	cfg.WorkingDir = dir
	return w, nil
}
func (w *codeWorkspaceContext) close() {
	if w != nil && w.lease != nil {
		_ = w.lease.Close()
		w.lease = nil
	}
}
func (b *localAgentBackend) bindCodeWorkspaceSession(ctx context.Context, id string) error {
	w := b.workspace
	if w == nil || id == "" {
		return nil
	}
	if w.record != nil {
		return w.manager.BindSession(ctx, w.project, w.record.ID, gitutil.SessionBinding{SessionID: id, UserID: b.effectiveUserID(), AppName: codeAppName})
	}
	_, err := w.owners(ctx, codeWorkspaceOwner{b.effectiveUserID(), b.workingDir})
	return err
}
func (b *localAgentBackend) ListCodeWorkspaces(ctx context.Context) ([]backend.WorkspaceSummary, error) {
	w := b.workspace
	if w == nil {
		return nil, fmt.Errorf("Independent workspace requires a non-bare Git project; Current workspace remains available")
	}
	records, e := w.manager.List(ctx, w.project)
	if e != nil {
		return nil, e
	}
	out := []backend.WorkspaceSummary{{ProjectRoot: w.project.OriginalRoot, WorkingDir: w.project.OriginalRoot, Mode: backend.WorkspaceCurrent, State: "current"}}
	for _, r := range records {
		active := w.record != nil && r.ID == w.record.ID && w.lease != nil
		label := "detached"
		if r.State == gitutil.WorkspaceReady || r.State == gitutil.WorkspaceRetained {
			if !active {
				lease, err := w.manager.AcquireLease(ctx, w.project, r.ID)
				if errors.Is(err, gitutil.ErrWorkspaceBusy) {
					active = true
				} else if err == nil {
					_ = lease.Close()
				}
			}
			if branch := gitutil.DetectBranch(r.Root); branch != "" {
				label = branch
			}
		}
		out = append(out, backend.WorkspaceSummary{ID: r.ID, ProjectRoot: w.project.OriginalRoot, WorkingDir: r.InvocationDir, Mode: backend.WorkspaceIndependent, State: string(r.State), Retained: r.State == gitutil.WorkspaceRetained, Active: active, Label: label})
	}
	return out, nil
}
func (b *localAgentBackend) PrepareWorkspaceLaunch(ctx context.Context, req backend.WorkspaceLaunchRequest) (backend.WorkspaceLaunchRequest, error) {
	b.workspacePrompt = false
	if req.Mode == backend.WorkspaceAsk {
		return req, fmt.Errorf("choose Current workspace or Independent workspace first")
	}
	if err := ValidateCodeWorkspaceOptions(req.Mode, req.BaseRef, req.CopyMode); err != nil {
		return req, err
	}
	w := b.workspace
	if req.Mode == backend.WorkspaceCurrent && req.SessionID == "" && req.WorkspaceID == "" {
		req.WorkingDir = b.workingDir
		return req, nil
	}
	if w == nil {
		return req, fmt.Errorf("Independent workspace requires a non-bare Git project")
	}
	req.ProjectRoot = w.project.OriginalRoot
	if req.WorkspaceID != "" && req.SessionID == "" {
		r, err := w.manager.Get(ctx, w.project, req.WorkspaceID)
		if err != nil {
			return req, err
		}
		if err = b.validateWorkspaceHandoff(ctx, *r); err != nil {
			return req, err
		}
		req.WorkingDir = r.InvocationDir
		req.Mode, req.CopyMode, req.BaseRef = backend.WorkspaceCurrent, "", ""
		return req, nil
	}
	if req.SessionID != "" && req.Mode == backend.WorkspaceIndependent {
		if b.fileStore == nil {
			return req, fmt.Errorf("active saved Code session required")
		}
		meta, e := b.fileStore.GetSessionMeta(req.SessionID)
		if e != nil || meta == nil || meta.AppName != codeAppName || meta.UserID != b.effectiveUserID() {
			return req, fmt.Errorf("active saved Code session required")
		}
		copyMode := req.CopyMode
		if copyMode == "" {
			copyMode = backend.WorkspaceCurrentWork
		}
		r, e := w.manager.Create(ctx, gitutil.WorkspaceCreateOptions{SourceDir: b.workingDir, CopyMode: gitutil.WorkspaceCopyMode(copyMode), Ref: req.BaseRef})
		if e != nil {
			return req, e
		}
		if e = w.manager.BindSession(ctx, w.project, r.ID, gitutil.SessionBinding{SessionID: req.SessionID, UserID: meta.UserID, AppName: meta.AppName}); e != nil {
			_ = w.manager.SetState(ctx, w.project, r.ID, gitutil.WorkspaceCleanupBlocked, e.Error())
			return req, fmt.Errorf("workspace retained at %s; bind session: %w", r.InvocationDir, e)
		}
		// File checkpoints contain absolute paths from the original checkout. The
		// conversation and plan continue, but rollback starts a fresh file epoch in
		// the independent workspace so it can never restore into the source tree.
		if b.checkpoints != nil {
			b.checkpoints.ResetSession(req.SessionID)
		}
		req.WorkingDir = r.InvocationDir
		req.WorkspaceID = r.ID
		req.Mode, req.CopyMode, req.BaseRef = backend.WorkspaceCurrent, "", ""
		return req, nil
	}
	if req.SessionID != "" {
		r, binding, e := w.manager.FindSession(ctx, w.project, req.SessionID)
		if errors.Is(e, os.ErrNotExist) {
			return b.legacyWorkspaceResumeRequest(ctx, req.SessionID)
		}
		if e != nil {
			return req, e
		}
		if binding.AppName != codeAppName {
			return req, fmt.Errorf("workspace session ownership mismatch")
		}
		if e = b.validateWorkspaceHandoff(ctx, *r); e != nil {
			return req, e
		}
		if b.fileStore != nil {
			meta, e := b.fileStore.GetSessionMeta(req.SessionID)
			if e != nil {
				return req, e
			}
			if meta == nil || meta.AppName != binding.AppName || meta.UserID != binding.UserID {
				return req, fmt.Errorf("workspace transcript ownership mismatch")
			}
		}
		req.WorkspaceID = r.ID
		req.WorkingDir = r.InvocationDir
		req.Mode = backend.WorkspaceCurrent
		req.CopyMode = ""
		req.BaseRef = ""
		return req, nil
	}
	copy := req.CopyMode
	if copy == "" {
		copy = backend.WorkspaceCommitted
	}
	r, e := w.manager.Create(ctx, gitutil.WorkspaceCreateOptions{SourceDir: b.workingDir, CopyMode: gitutil.WorkspaceCopyMode(copy), Ref: req.BaseRef})
	if e != nil {
		return req, e
	}
	// Destination already exists: the next process opens current, never creates twice.
	req.WorkingDir = r.InvocationDir
	req.WorkspaceID = r.ID
	req.Mode = backend.WorkspaceCurrent
	req.CopyMode = ""
	req.BaseRef = ""
	return req, nil
}

// Preparation checks availability but the fresh process must reacquire its own lease.
func (b *localAgentBackend) validateWorkspaceHandoff(ctx context.Context, r gitutil.WorkspaceRecord) error {
	w := b.workspace
	if err := w.manager.Validate(ctx, r); err != nil {
		return err
	}
	if w.record != nil && w.record.ID == r.ID && w.lease != nil {
		return nil
	}
	lease, err := w.manager.AcquireLease(ctx, w.project, r.ID)
	if err != nil {
		return err
	}
	return lease.Close()
}

func (b *lazyCodeBackend) ListCodeWorkspaces(ctx context.Context) ([]backend.WorkspaceSummary, error) {
	if inner, ok := b.inner.(backend.CodeWorkspaceBackend); ok {
		return inner.ListCodeWorkspaces(ctx)
	}
	return nil, fmt.Errorf("Code backend not opened")
}
func (b *lazyCodeBackend) PrepareWorkspaceLaunch(ctx context.Context, r backend.WorkspaceLaunchRequest) (backend.WorkspaceLaunchRequest, error) {
	if inner, ok := b.inner.(backend.CodeWorkspaceBackend); ok {
		return inner.PrepareWorkspaceLaunch(ctx, r)
	}
	return r, fmt.Errorf("Code backend not opened")
}

var _ backend.CodeWorkspaceManagementBackend = (*localAgentBackend)(nil)
var _ backend.CodeWorkspaceManagementBackend = (*lazyCodeBackend)(nil)

func (b *localAgentBackend) codeWorkspaceManagementContext() (*codeWorkspaceContext, error) {
	if b.workspace == nil || b.workspace.manager == nil {
		return nil, fmt.Errorf("Independent workspace requires a non-bare Git project")
	}
	return b.workspace, nil
}

func (b *localAgentBackend) ReviewCodeWorkspace(ctx context.Context, id string) (backend.WorkspaceReview, error) {
	w, err := b.codeWorkspaceManagementContext()
	if err != nil {
		return backend.WorkspaceReview{}, err
	}
	review, err := w.manager.Review(ctx, w.project, id, w.project.OriginalRoot)
	if err != nil {
		return backend.WorkspaceReview{}, err
	}
	out := backend.WorkspaceReview{WorkspaceID: review.WorkspaceID, Digest: review.Digest}
	for _, change := range review.Changes {
		out.Files = append(out.Files, change.Path)
		if change.Conflict || change.Unsupported != "" {
			out.Conflicts = append(out.Conflicts, change.Path)
		}
	}
	out.Summary = fmt.Sprintf("%d changed files, %d conflicts or unsupported files. Target: %s. Apply preserves current staging; no commit is created.", len(out.Files), len(out.Conflicts), review.TargetRoot)
	return out, nil
}

func (b *localAgentBackend) ApplyCodeWorkspace(ctx context.Context, review backend.WorkspaceReview) error {
	w, err := b.codeWorkspaceManagementContext()
	if err != nil {
		return err
	}
	if review.Digest == "" {
		return fmt.Errorf("review workspace changes before applying")
	}
	return w.manager.Apply(ctx, w.project, review.WorkspaceID, gitutil.WorkspaceApplyOptions{ReviewDigest: review.Digest, TargetDir: w.project.OriginalRoot, Paths: review.Files})
}

// Recovery is explicit: never roll back a pending apply merely by opening a picker.
func (b *localAgentBackend) RecoverCodeWorkspaceApply(ctx context.Context) error {
	w, err := b.codeWorkspaceManagementContext()
	if err != nil {
		return err
	}
	return w.manager.RecoverApply(ctx, w.project)
}

func (b *localAgentBackend) CreateCodeWorkspaceBranch(ctx context.Context, id, name string) error {
	w, err := b.codeWorkspaceManagementContext()
	if err != nil {
		return err
	}
	return w.manager.CreateBranch(ctx, w.project, id, name)
}

// The management interface dispatches this only after the picker confirms removal.
// Both the eligibility review and removal retain the manager's exclusive lease guards.
func (b *localAgentBackend) RemoveCodeWorkspace(ctx context.Context, id string) error {
	w, err := b.codeWorkspaceManagementContext()
	if err != nil {
		return err
	}
	review, err := w.manager.ReviewRemoval(ctx, w.project, id)
	if err != nil {
		return err
	}
	if !review.Eligible {
		return fmt.Errorf("%w: %s", gitutil.ErrWorkspaceConflict, strings.Join(review.Reasons, "; "))
	}
	return w.manager.Remove(ctx, w.project, id, review.Digest, true)
}

func (b *lazyCodeBackend) ReviewCodeWorkspace(ctx context.Context, id string) (backend.WorkspaceReview, error) {
	if inner, ok := b.inner.(backend.CodeWorkspaceManagementBackend); ok {
		return inner.ReviewCodeWorkspace(ctx, id)
	}
	return backend.WorkspaceReview{}, fmt.Errorf("Code backend not opened")
}
func (b *lazyCodeBackend) ApplyCodeWorkspace(ctx context.Context, review backend.WorkspaceReview) error {
	if inner, ok := b.inner.(backend.CodeWorkspaceManagementBackend); ok {
		return inner.ApplyCodeWorkspace(ctx, review)
	}
	return fmt.Errorf("Code backend not opened")
}
func (b *lazyCodeBackend) CreateCodeWorkspaceBranch(ctx context.Context, id, name string) error {
	if inner, ok := b.inner.(backend.CodeWorkspaceManagementBackend); ok {
		return inner.CreateCodeWorkspaceBranch(ctx, id, name)
	}
	return fmt.Errorf("Code backend not opened")
}
func (b *lazyCodeBackend) RemoveCodeWorkspace(ctx context.Context, id string) error {
	if inner, ok := b.inner.(backend.CodeWorkspaceManagementBackend); ok {
		return inner.RemoveCodeWorkspace(ctx, id)
	}
	return fmt.Errorf("Code backend not opened")
}
func (b *lazyCodeBackend) RecoverCodeWorkspaceApply(ctx context.Context) error {
	if inner, ok := b.inner.(interface{ RecoverCodeWorkspaceApply(context.Context) error }); ok {
		return inner.RecoverCodeWorkspaceApply(ctx)
	}
	return fmt.Errorf("Code backend not opened")
}

func (b *localAgentBackend) legacyWorkspaceResumeRequest(ctx context.Context, id string) (backend.WorkspaceLaunchRequest, error) {
	req := backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceCurrent, SessionID: id}
	if b.workspace == nil || b.fileStore == nil {
		return req, fmt.Errorf("saved workspace session not found")
	}
	meta, e := b.fileStore.GetSessionMeta(id)
	if e != nil {
		return req, e
	}
	if meta == nil || meta.AppName != codeAppName {
		return req, fmt.Errorf("saved Code session not found")
	}
	owners, e := b.workspace.owners(ctx)
	if e != nil {
		return req, e
	}
	for _, o := range owners {
		if o.UserID == meta.UserID {
			req.WorkingDir = o.Directory
			req.ProjectRoot = b.workspace.project.OriginalRoot
			return req, nil
		}
	}
	return req, fmt.Errorf("saved Code session belongs to another project")
}

func (b *localAgentBackend) enrichWorkspaceSession(ctx context.Context, s backend.SessionSummary) backend.SessionSummary {
	w := b.workspace
	if w == nil {
		return s
	}
	s.ProjectRoot = w.project.OriginalRoot
	s.WorkspaceMode = backend.WorkspaceCurrent
	r, _, err := w.manager.FindSession(ctx, w.project, s.ID)
	if err == nil {
		s.WorkspaceID = r.ID
		s.WorkspacePath = r.InvocationDir
		s.WorkspaceMode = backend.WorkspaceIndependent
		s.WorkspaceRetained = r.State == gitutil.WorkspaceRetained
		s.WorkspaceActive = w.record != nil && r.ID == w.record.ID && w.lease != nil
	}
	return s
}

// Enrich project listing using only durable owners plus managed bindings.
func (b *localAgentBackend) projectSessionMetas(ctx context.Context) ([]persistentsession.SessionMeta, error) {
	w := b.workspace
	if w == nil {
		return b.fileStore.ListSessionMetas(codeAppName, b.effectiveUserID())
	}
	owners, e := w.owners(ctx)
	if e != nil {
		return nil, e
	}
	records, e := w.manager.List(ctx, w.project)
	if e != nil {
		return nil, e
	}
	users := map[string]bool{b.effectiveUserID(): true}
	for _, o := range owners {
		users[o.UserID] = true
	}
	for _, r := range records {
		for _, s := range r.Sessions {
			if s.AppName == codeAppName {
				users[s.UserID] = true
			}
		}
	}
	var out []persistentsession.SessionMeta
	seen := map[string]bool{}
	for u := range users {
		metas, e := b.fileStore.ListSessionMetas(codeAppName, u)
		if e != nil {
			return nil, e
		}
		for _, m := range metas {
			if m.AppName == codeAppName && m.UserID == u && !seen[m.ID] {
				out = append(out, m)
				seen[m.ID] = true
			}
		}
	}
	pending, e := b.pendingDeletionMetas(ctx)
	if e != nil {
		return nil, e
	}
	for _, meta := range pending {
		if !seen[meta.ID] {
			out = append(out, meta)
			seen[meta.ID] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}
