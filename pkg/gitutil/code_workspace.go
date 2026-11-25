package gitutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/gofrs/flock"
	"github.com/google/uuid"
)

type WorkspaceMode string

const (
	WorkspaceModeCurrent     WorkspaceMode = "current"
	WorkspaceModeIndependent WorkspaceMode = "independent"
)

type WorkspaceCopyMode string

const (
	WorkspaceCopyCommitted   WorkspaceCopyMode = "committed"
	WorkspaceCopyCurrentWork WorkspaceCopyMode = "current-work"
)

type WorkspaceState string

const (
	WorkspacePreparing      WorkspaceState = "preparing"
	WorkspaceReady          WorkspaceState = "ready"
	WorkspaceRetained       WorkspaceState = "retained"
	WorkspaceCleanupBlocked WorkspaceState = "cleanup-blocked"
	WorkspaceRemoved        WorkspaceState = "removed"
)

var (
	ErrWorkspaceUnsupported = errors.New("unsupported repository for independent workspace")
	ErrWorkspaceBusy        = errors.New("workspace already has an active writer")
	ErrWorkspaceStale       = errors.New("workspace source changed; retry creation")
	ErrWorkspaceOwnership   = errors.New("workspace ownership could not be verified")
	ErrWorkspaceConflict    = errors.New("workspace changes conflict")
)

type ProjectIdentity struct {
	ID           string `json:"id"`
	CommonDir    string `json:"common_dir"`
	OriginalRoot string `json:"original_root"`
}
type SessionBinding struct {
	SessionID  string `json:"session_id"`
	UserID     string `json:"user_id"`
	AppName    string `json:"app_name"`
	StorageDir string `json:"storage_dir,omitempty"`
}
type WorkspaceRecord struct {
	Version       int               `json:"version"`
	ID            string            `json:"id"`
	Project       ProjectIdentity   `json:"project"`
	Root          string            `json:"root"`
	InvocationDir string            `json:"invocation_dir"`
	SourceRoot    string            `json:"source_root"`
	GitDir        string            `json:"git_dir,omitempty"`
	CopyMode      WorkspaceCopyMode `json:"copy_mode"`
	State         WorkspaceState    `json:"state"`
	BaseCommit    string            `json:"base_commit"`
	StagedTree    string            `json:"staged_tree"`
	FullTree      string            `json:"full_tree"`
	Sessions      []SessionBinding  `json:"sessions,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	LastError     string            `json:"last_error,omitempty"`
}
type WorkspaceCreateOptions struct {
	SourceDir string
	CopyMode  WorkspaceCopyMode
	Ref       string
}

// CodeWorkspaceManager stores private local worktrees. It does not sandbox tools.
type CodeWorkspaceManager struct{ root string }

func NewCodeWorkspaceManager(root string) (*CodeWorkspaceManager, error) {
	if root == "" {
		d, e := config.GetConfigDir()
		if e != nil {
			return nil, e
		}
		root = filepath.Join(d, "code-workspaces")
	}
	a, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	if e = secureWorkspaceDir(a); e != nil {
		return nil, e
	}
	return &CodeWorkspaceManager{root: a}, nil
}
func (m *CodeWorkspaceManager) Root() string { return m.root }
func workspaceGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Ignore caller Git routing variables: never accidentally operate on another index/repository.
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	out, e := cmd.CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), e, strings.TrimSpace(string(out)))
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}
func canonicalWorkspacePath(p string) (string, error) {
	a, e := filepath.Abs(p)
	if e != nil {
		return "", e
	}
	return filepath.EvalSymlinks(a)
}
func secureWorkspaceDir(p string) error {
	// Refuse symlink ancestors, including an existing root; sidecars must remain permanent.
	for q := p; ; q = filepath.Dir(q) {
		st, e := os.Lstat(q)
		if e == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink at %s", ErrWorkspaceOwnership, q)
		}
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if filepath.Dir(q) == q {
			break
		}
	}
	return os.MkdirAll(p, 0700)
}
func ResolveCodeProject(ctx context.Context, dir string) (ProjectIdentity, error) {
	var p ProjectIdentity
	root, e := workspaceGit(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if e != nil {
		return p, fmt.Errorf("%w: use Current workspace outside a non-bare Git checkout: %v", ErrWorkspaceUnsupported, e)
	}
	root, e = canonicalWorkspacePath(root)
	if e != nil {
		return p, e
	}
	common, e := workspaceGit(ctx, root, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return p, e
	}
	common, e = canonicalWorkspacePath(common)
	if e != nil {
		return p, e
	}
	sum := sha256.Sum256([]byte(common))
	return ProjectIdentity{ID: hex.EncodeToString(sum[:16]), CommonDir: common, OriginalRoot: root}, nil
}
func validateWorkspaceSource(ctx context.Context, p ProjectIdentity) error {
	if _, e := workspaceGit(ctx, p.OriginalRoot, nil, "rev-parse", "--verify", "HEAD^{commit}"); e != nil {
		return fmt.Errorf("%w: commit the initial HEAD first", ErrWorkspaceUnsupported)
	}
	if s, _ := workspaceGit(ctx, p.OriginalRoot, nil, "rev-parse", "--show-superproject-working-tree"); s != "" {
		return fmt.Errorf("%w: submodules are not supported", ErrWorkspaceUnsupported)
	}
	if s, _ := workspaceGit(ctx, p.OriginalRoot, nil, "config", "--bool", "core.sparseCheckout"); s == "true" {
		return fmt.Errorf("%w: disable sparse checkout first", ErrWorkspaceUnsupported)
	}
	if s, e := workspaceGit(ctx, p.OriginalRoot, nil, "ls-files", "--unmerged"); e != nil {
		return e
	} else if s != "" {
		return fmt.Errorf("%w: resolve merge conflicts first", ErrWorkspaceUnsupported)
	}
	if s, e := workspaceGit(ctx, p.OriginalRoot, nil, "ls-files", "--stage"); e != nil {
		return e
	} else {
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, "160000 ") {
				return fmt.Errorf("%w: repositories with submodules are not supported", ErrWorkspaceUnsupported)
			}
		}
	}
	return nil
}
func (m *CodeWorkspaceManager) Create(ctx context.Context, o WorkspaceCreateOptions) (*WorkspaceRecord, error) {
	p, e := ResolveCodeProject(ctx, o.SourceDir)
	if e != nil {
		return nil, e
	}
	if e = validateWorkspaceSource(ctx, p); e != nil {
		return nil, e
	}
	if rel, err := filepath.Rel(p.OriginalRoot, m.root); err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, fmt.Errorf("%w: managed storage must be outside the source checkout", ErrWorkspaceOwnership)
	}
	if o.CopyMode == "" {
		o.CopyMode = WorkspaceCopyCommitted
	}
	if o.CopyMode != WorkspaceCopyCommitted && o.CopyMode != WorkspaceCopyCurrentWork {
		return nil, fmt.Errorf("unknown workspace copy mode %q", o.CopyMode)
	}
	ref := o.Ref
	if ref == "" {
		ref = "HEAD"
	}
	if strings.HasPrefix(ref, "-") {
		return nil, fmt.Errorf("invalid base ref")
	}
	base, e := workspaceGit(ctx, p.OriginalRoot, nil, "rev-parse", "--verify", ref+"^{commit}")
	if e != nil {
		return nil, e
	}
	project, e := m.ResolveProject(ctx, o.SourceDir)
	if e != nil {
		return nil, e
	}
	r := &WorkspaceRecord{Version: 1, ID: uuid.NewString(), Project: project, SourceRoot: p.OriginalRoot, CopyMode: o.CopyMode, State: WorkspacePreparing, BaseCommit: base, CreatedAt: time.Now().UTC()}
	r.Root = filepath.Join(m.root, p.ID, "worktrees", r.ID)
	source, e := canonicalWorkspacePath(o.SourceDir)
	if e != nil {
		return nil, e
	}
	rel, e := filepath.Rel(p.OriginalRoot, source)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, ErrWorkspaceOwnership
	}
	r.InvocationDir = filepath.Join(r.Root, rel)
	if e = secureWorkspaceDir(filepath.Dir(r.Root)); e != nil {
		return nil, e
	}
	var snap *workspaceSnapshot
	if o.CopyMode == WorkspaceCopyCurrentWork {
		if ref != "HEAD" {
			return nil, fmt.Errorf("current-work snapshot requires HEAD")
		}
		snap, e = captureWorkspaceSnapshot(ctx, p.OriginalRoot)
		if e != nil {
			return nil, e
		}
		defer snap.close()
		if snap.head != base {
			return nil, ErrWorkspaceStale
		}
		r.StagedTree = snap.staged
		r.FullTree = snap.full
	} else {
		tree, e := workspaceGit(ctx, p.OriginalRoot, nil, "rev-parse", base+"^{tree}")
		if e != nil {
			return nil, e
		}
		r.StagedTree = tree
		r.FullTree = tree
	}
	if e = m.store(ctx, *r, true); e != nil {
		return nil, e
	}
	fail := func(err error) (*WorkspaceRecord, error) {
		r.State = WorkspaceCleanupBlocked
		r.LastError = err.Error()
		if se := m.store(ctx, *r, false); se != nil {
			return r, errors.Join(err, se)
		}
		return r, err
	}
	if _, e = workspaceGit(ctx, p.OriginalRoot, nil, "worktree", "add", "--detach", r.Root, base); e != nil {
		return fail(e)
	}
	r.GitDir, e = workspaceGit(ctx, r.Root, nil, "rev-parse", "--absolute-git-dir")
	if e != nil {
		return fail(e)
	}
	r.GitDir, e = canonicalWorkspacePath(r.GitDir)
	if e != nil {
		return fail(e)
	}
	if snap != nil {
		if e = snap.verify(ctx, p.OriginalRoot); e != nil {
			return fail(e)
		}
		if _, e = workspaceGit(ctx, r.Root, nil, "read-tree", "--reset", "-u", r.FullTree); e != nil {
			return fail(e)
		}
		if _, e = workspaceGit(ctx, r.Root, nil, "read-tree", r.StagedTree); e != nil {
			return fail(e)
		}
		if e = snap.verify(ctx, p.OriginalRoot); e != nil {
			return fail(e)
		}
	}
	for name, oid := range map[string]string{"committed": r.BaseCommit, "staged": r.StagedTree, "full": r.FullTree} {
		if _, e = workspaceGit(ctx, p.OriginalRoot, nil, "update-ref", "refs/astonish/workspaces/"+r.ID+"/"+name, oid); e != nil {
			return fail(e)
		}
	}
	if st, err := os.Stat(r.InvocationDir); err != nil || !st.IsDir() {
		r.InvocationDir = r.Root
	} else if actual, err := canonicalWorkspacePath(r.InvocationDir); err != nil || (actual != r.Root && !strings.HasPrefix(actual, r.Root+string(filepath.Separator))) {
		r.InvocationDir = r.Root
	}
	r.State = WorkspaceReady
	if e = m.store(ctx, *r, false); e != nil {
		return fail(e)
	}
	return r, nil
}

// WorkspaceLease must be held for the entire managed-workspace writer process lifetime.
type WorkspaceLease struct{ lock *flock.Flock }

func (l *WorkspaceLease) Close() error {
	if l == nil || l.lock == nil {
		return nil
	}
	return l.lock.Unlock()
}
func (m *CodeWorkspaceManager) AcquireLease(ctx context.Context, p ProjectIdentity, id string) (*WorkspaceLease, error) {
	r, e := m.Get(ctx, p, id)
	if e != nil {
		return nil, e
	}
	if e = m.Validate(ctx, *r); e != nil {
		return nil, e
	}
	path := filepath.Join(m.root, p.ID, id+".writer.lock")
	if e = checkWorkspaceFile(path); e != nil {
		return nil, e
	}
	l := flock.New(path, flock.SetPermissions(0600))
	ok, e := l.TryLock()
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, ErrWorkspaceBusy
	}
	if e = m.Validate(ctx, *r); e != nil {
		_ = l.Unlock()
		return nil, e
	}
	return &WorkspaceLease{lock: l}, nil
}
func (m *CodeWorkspaceManager) Validate(ctx context.Context, r WorkspaceRecord) error {
	if r.State != WorkspaceReady && r.State != WorkspaceRetained {
		return fmt.Errorf("%w: workspace is %s", ErrWorkspaceOwnership, r.State)
	}
	if !validWorkspaceID(r.Project.ID) || !validWorkspaceID(r.ID) || r.Root != filepath.Join(m.root, r.Project.ID, "worktrees", r.ID) {
		return ErrWorkspaceOwnership
	}
	if e := secureWorkspaceDir(filepath.Dir(r.Root)); e != nil {
		return e
	}
	actual, e := canonicalWorkspacePath(r.Root)
	if e != nil || actual != r.Root {
		return ErrWorkspaceOwnership
	}
	p, e := ResolveCodeProject(ctx, r.Root)
	if e != nil || p.CommonDir != r.Project.CommonDir || p.OriginalRoot != r.Root {
		return ErrWorkspaceOwnership
	}
	gd, e := workspaceGit(ctx, r.Root, nil, "rev-parse", "--absolute-git-dir")
	if e != nil {
		return ErrWorkspaceOwnership
	}
	gd, e = canonicalWorkspacePath(gd)
	if e != nil || gd != r.GitDir {
		return ErrWorkspaceOwnership
	}
	backlink, e := os.ReadFile(filepath.Join(gd, "gitdir"))
	if e != nil || strings.TrimSuffix(string(backlink), "\n") != filepath.Join(r.Root, ".git") {
		return ErrWorkspaceOwnership
	}
	return nil
}
