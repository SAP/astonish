package gitutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrWorkspaceRecoveryRequired = errors.New("workspace apply recovery required")

type WorkspaceApplyOptions struct {
	ReviewDigest string
	TargetDir    string
	// Paths selects reviewed whole files. An empty selection applies all changes.
	Paths []string
}
type workspaceApplyJournal struct {
	Version           int               `json:"version"`
	WorkspaceID       string            `json:"workspace_id"`
	TargetRoot        string            `json:"target_root"`
	TargetHEAD        string            `json:"target_head"`
	TargetIndexDigest string            `json:"target_index_digest"`
	Changes           []WorkspaceChange `json:"changes"`
	CreatedDirs       []string          `json:"created_dirs"`
	Prepared          map[string]string `json:"prepared,omitempty"`
	PreviousReceipt   []byte            `json:"previous_receipt,omitempty"`
	TargetBranch      *string           `json:"target_branch,omitempty"`
}

func (m *CodeWorkspaceManager) integrationLock(ctx context.Context, p ProjectIdentity) (*flock.Flock, string, error) {
	if !validWorkspaceID(p.ID) {
		return nil, "", ErrWorkspaceOwnership
	}
	dir := filepath.Join(m.root, p.ID)
	if e := secureWorkspaceDir(dir); e != nil {
		return nil, "", e
	}
	path := filepath.Join(dir, "integration.lock")
	if e := checkWorkspaceFile(path); e != nil {
		return nil, "", e
	}
	l := flock.New(path, flock.SetPermissions(0600))
	ok, e := l.TryLockContext(ctx, 20*time.Millisecond)
	if e != nil {
		return nil, "", e
	}
	if !ok {
		return nil, "", ErrWorkspaceBusy
	}
	return l, filepath.Join(dir, "apply-journal.json"), nil
}
func workspaceSyncDir(path string) error {
	d, e := os.Open(path)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func workspaceWriteJournal(path string, j workspaceApplyJournal) error {
	if e := checkWorkspaceFile(path); e != nil {
		return e
	}
	b, e := json.Marshal(j)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".journal-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	return workspaceSyncDir(filepath.Dir(path))
}
func workspaceRestoreReceipt(path string, data []byte) error {
	if len(data) == 0 {
		if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
			return e
		}
		return workspaceSyncDir(filepath.Dir(path))
	}
	var receipt workspaceApplyJournal
	if e := json.Unmarshal(data, &receipt); e != nil {
		return e
	}
	return workspaceWriteJournal(path, receipt)
}

func workspaceClearJournal(path string) error {
	if e := os.Remove(path); e != nil {
		return e
	}
	return workspaceSyncDir(filepath.Dir(path))
}
func workspaceEnsureParents(root, path string, j *workspaceApplyJournal) error {
	parent := filepath.Dir(filepath.FromSlash(path))
	if parent == "." {
		return nil
	}
	q := root
	for _, part := range strings.Split(parent, string(filepath.Separator)) {
		q = filepath.Join(q, part)
		st, e := os.Lstat(q)
		if os.IsNotExist(e) {
			j.CreatedDirs = append(j.CreatedDirs, q)
		} else if e != nil {
			return e
		} else if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return ErrWorkspaceUnsupported
		}
	}
	return nil
}

// workspaceInstallFile uses a fsynced sibling and rename; no index operation occurs.
func workspaceInstallFile(root, path string, state WorkspaceFileState) error {
	if e := workspaceSafeParents(root, path); e != nil {
		return e
	}
	dst := filepath.Join(root, filepath.FromSlash(path))
	if !state.Exists {
		if e := os.Remove(dst); e != nil && !os.IsNotExist(e) {
			return e
		}
		return workspaceSyncDir(filepath.Dir(dst))
	}
	if state.Unsupported != "" || state.Mode != 0100644 && state.Mode != 0100755 {
		return ErrWorkspaceUnsupported
	}
	f, e := os.CreateTemp(filepath.Dir(dst), ".astonish-apply-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	mode := os.FileMode(0644)
	if state.Mode == 0100755 {
		mode = 0755
	}
	if state.Permissions != 0 {
		mode = os.FileMode(state.Permissions & 0777)
	}
	e = f.Chmod(mode)
	if e == nil {
		_, e = f.Write(state.Data)
	}
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, dst); e != nil {
		return e
	}
	return workspaceSyncDir(filepath.Dir(dst))
}
func (m *CodeWorkspaceManager) targetLease(ctx context.Context, p ProjectIdentity, target string) (*WorkspaceLease, error) {
	records, e := m.List(ctx, p)
	if e != nil {
		return nil, e
	}
	for _, r := range records {
		if r.Root == target {
			return m.AcquireLease(ctx, p, r.ID)
		}
	}
	// Only the registered original checkout or a registered managed worktree is writable.
	if target != p.OriginalRoot {
		return nil, ErrWorkspaceOwnership
	}
	return nil, nil
}
func workspaceCaseCheck(ctx context.Context, root string, changes []WorkspaceChange) error {
	// Conservatively reject case-fold aliases on every platform.
	b, e := workspaceGitBytes(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if e != nil {
		return e
	}
	paths := map[string]string{}
	for _, p := range strings.Split(string(b), "\x00") {
		if p != "" {
			key := strings.ToLower(p)
			if prior, ok := paths[key]; ok && prior != p {
				return ErrWorkspaceConflict
			}
			paths[key] = p
		}
	}
	// Include ignored files and directories in collision checks too; they must
	// never be replaced just because Git did not list them.
	dirs := map[string]bool{}
	e = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if path == filepath.Join(root, ".git") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		key := strings.ToLower(rel)
		if prior, ok := paths[key]; ok && prior != rel {
			return ErrWorkspaceConflict
		}
		paths[key] = rel
		dirs[key] = d.IsDir()
		return nil
	})
	if e != nil {
		return e
	}
	for _, c := range changes {
		for parent := filepath.ToSlash(filepath.Dir(c.Path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if prior, ok := paths[strings.ToLower(parent)]; ok && prior != parent {
				return fmt.Errorf("%w: case-fold parent collision %s", ErrWorkspaceConflict, c.Path)
			}
			key := strings.ToLower(parent)
			if _, ok := paths[key]; !ok {
				paths[key] = parent
				dirs[key] = true
			}
		}
		key := strings.ToLower(c.Path)
		if prior, ok := paths[key]; ok && prior != c.Path {
			return fmt.Errorf("%w: case-fold collision %s", ErrWorkspaceConflict, c.Path)
		}
		paths[key] = c.Path
		if c.Desired.Exists {
			for p := range paths {
				if p != key && (strings.HasPrefix(p, key+"/") || (strings.HasPrefix(key, p+"/") && !dirs[p])) {
					return ErrWorkspaceConflict
				}
			}
		}
	}
	return nil
}

// Apply validates the entire selected review before writing, then journals preimages.
// Readers can observe the bounded multi-file commit window. External Git/shell writers
// are not locked by this feature; optimistic checks detect changes, not provide a sandbox.
func (m *CodeWorkspaceManager) Apply(ctx context.Context, p ProjectIdentity, id string, o WorkspaceApplyOptions) error {
	l, jpath, e := m.integrationLock(ctx, p)
	if e != nil {
		return e
	}
	defer l.Unlock()
	if e = checkWorkspaceFile(jpath); e != nil {
		return e
	}
	if _, e = os.Lstat(jpath); e == nil {
		return ErrWorkspaceRecoveryRequired
	} else if !os.IsNotExist(e) {
		return e
	}
	review, e := m.Review(ctx, p, id, o.TargetDir)
	if e != nil {
		return e
	}
	lease, e := m.targetLease(ctx, p, review.TargetRoot)
	if e != nil {
		return e
	}
	defer lease.Close()
	if o.ReviewDigest == "" || review.Digest != o.ReviewDigest {
		return ErrWorkspaceStale
	}
	selected := map[string]bool{}
	for _, path := range o.Paths {
		if !workspaceSafeRelative(path) || selected[path] {
			return ErrWorkspaceOwnership
		}
		selected[path] = true
	}
	changes := []WorkspaceChange{}
	for _, c := range review.Changes {
		if len(o.Paths) > 0 && !selected[c.Path] {
			continue
		}
		delete(selected, c.Path)
		if c.Unsupported != "" || (c.Desired.Exists && c.Desired.Mode != 0100644 && c.Desired.Mode != 0100755) {
			return fmt.Errorf("%w: %s: %s", ErrWorkspaceUnsupported, c.Path, c.Unsupported)
		}
		if c.Conflict {
			return fmt.Errorf("%w: %s", ErrWorkspaceConflict, c.Path)
		}
		if !workspaceSameFile(c.Target, c.Desired) {
			changes = append(changes, c)
		}
	}
	if len(selected) > 0 {
		return ErrWorkspaceStale
	}
	if e = workspaceCaseCheck(ctx, review.TargetRoot, changes); e != nil {
		return e
	}
	j := workspaceApplyJournal{Version: 1, WorkspaceID: id, TargetRoot: review.TargetRoot, TargetHEAD: review.TargetHEAD, TargetIndexDigest: review.TargetIndexDigest, Changes: changes}
	state, e := inspectWorkspace(ctx, j.TargetRoot)
	if e != nil {
		return e
	}
	j.TargetBranch = &state.Branch
	receiptPath := filepath.Join(m.root, p.ID, "applied-"+id+".json")
	if e = checkWorkspaceFile(receiptPath); e != nil {
		return e
	}
	j.PreviousReceipt, e = os.ReadFile(receiptPath)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	var receipt workspaceApplyJournal
	if len(j.PreviousReceipt) > 0 {
		if e = json.Unmarshal(j.PreviousReceipt, &receipt); e != nil {
			return e
		}
	}
	for _, c := range changes {
		if e = workspaceEnsureParents(j.TargetRoot, c.Path, &j); e != nil {
			return e
		}
	}
	// Deduplicate directories in shallow-first order; only these may be removed on rollback.
	seen := map[string]bool{}
	dirs := []string{}
	for _, d := range j.CreatedDirs {
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	j.CreatedDirs = dirs
	sourceRecord, e := m.Get(ctx, p, id)
	if e != nil {
		return e
	}
	sourceBefore, e := inspectWorkspaceBaseline(ctx, sourceRecord.Root, sourceRecord.FullTree)
	if e != nil {
		return e
	}
	current, e := m.Review(ctx, p, id, o.TargetDir)
	if e != nil {
		return e
	}
	if current.Digest != o.ReviewDigest {
		return ErrWorkspaceStale
	}
	if len(changes) == 0 {
		return nil
	}
	j.Prepared = map[string]string{}
	for _, c := range changes {
		if c.Desired.Exists {
			j.Prepared[c.Path] = filepath.Join(filepath.Dir(filepath.Join(j.TargetRoot, filepath.FromSlash(c.Path))), ".astonish-apply-"+uuid.NewString())
		}
	}
	if e = workspaceWriteJournal(jpath, j); e != nil {
		return e
	}
	rollback := func(cause error) error {
		re := workspaceRollback(j)
		if re != nil {
			return errors.Join(cause, ErrWorkspaceRecoveryRequired, re)
		}
		if re = workspaceRestoreReceipt(receiptPath, j.PreviousReceipt); re != nil {
			return errors.Join(cause, ErrWorkspaceRecoveryRequired, re)
		}
		return errors.Join(cause, workspaceClearJournal(jpath))
	}
	for _, d := range j.CreatedDirs {
		if e = os.Mkdir(d, 0755); e != nil {
			return rollback(e)
		}
		if e = workspaceSyncDir(filepath.Dir(d)); e != nil {
			return rollback(e)
		}
	}
	// Prepare every fsynced postimage before replacing any reviewed path.
	for _, c := range changes {
		if !c.Desired.Exists {
			continue
		}
		tmp := j.Prepared[c.Path]
		f, re := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if re != nil {
			return rollback(re)
		}
		mode := os.FileMode(0644)
		if c.Desired.Mode == 0100755 {
			mode = 0755
		}
		if c.Desired.Permissions != 0 {
			mode = os.FileMode(c.Desired.Permissions & 0777)
		}
		re = f.Chmod(mode)
		if re == nil {
			_, re = f.Write(c.Desired.Data)
		}
		if re == nil {
			re = f.Sync()
		}
		re = errors.Join(re, f.Close())
		if re == nil {
			re = workspaceSyncDir(filepath.Dir(tmp))
		}
		if re != nil {
			return rollback(re)
		}
	}
	before, e := inspectWorkspace(ctx, j.TargetRoot)
	if e != nil {
		return rollback(e)
	}
	if before.HEAD != j.TargetHEAD || before.IndexDigest != j.TargetIndexDigest || before.Branch != *j.TargetBranch {
		return rollback(ErrWorkspaceStale)
	}
	// Preparing large binary postimages can take time: revalidate the source's
	// complete baseline-aware working state immediately before target mutation.
	sourceNow, e := inspectWorkspaceBaseline(ctx, sourceRecord.Root, sourceRecord.FullTree)
	if e != nil {
		return rollback(e)
	}
	if workspaceJSONDigest(sourceNow) != workspaceJSONDigest(sourceBefore) {
		return rollback(ErrWorkspaceStale)
	}
	// Recheck every preimage together immediately before the first replacement.
	for _, c := range changes {
		now, re := workspaceReadFile(j.TargetRoot, c.Path)
		if re != nil {
			return rollback(re)
		}
		if !workspaceSameWorkingFile(now, c.Target) {
			return rollback(ErrWorkspaceStale)
		}
	}
	for _, c := range changes {
		if e = ctx.Err(); e != nil {
			return rollback(e)
		}
		now, re := workspaceReadFile(j.TargetRoot, c.Path)
		if re != nil {
			return rollback(re)
		}
		if !workspaceSameWorkingFile(now, c.Target) {
			return rollback(ErrWorkspaceStale)
		}
		if c.Desired.Exists {
			e = os.Rename(j.Prepared[c.Path], filepath.Join(j.TargetRoot, filepath.FromSlash(c.Path)))
			if e == nil {
				e = workspaceSyncDir(filepath.Dir(j.Prepared[c.Path]))
			}
		} else {
			e = workspaceInstallFile(j.TargetRoot, c.Path, c.Desired)
		}
		if e != nil {
			return rollback(e)
		}
	}
	after, e := inspectWorkspace(ctx, j.TargetRoot)
	if e != nil {
		return rollback(e)
	}
	if after.HEAD != j.TargetHEAD || after.IndexDigest != j.TargetIndexDigest || after.Branch != *j.TargetBranch {
		return rollback(ErrWorkspaceStale)
	}
	for _, c := range changes {
		now, re := workspaceReadFile(j.TargetRoot, c.Path)
		if re != nil {
			return rollback(re)
		}
		if !workspaceSameWorkingFile(now, c.Desired) {
			return rollback(ErrWorkspaceStale)
		}
	}
	// Keep receipts for earlier selected additions; replacing them would make a
	// subsequent review mistake our own addition for an unrelated untracked file.
	if receipt.TargetRoot != j.TargetRoot {
		receipt = workspaceApplyJournal{}
	}
	prior := map[string]WorkspaceChange{}
	for _, c := range receipt.Changes {
		prior[c.Path] = c
	}
	for _, c := range changes {
		prior[c.Path] = c
	}
	receipt = workspaceApplyJournal{Version: 1, WorkspaceID: id, TargetRoot: j.TargetRoot}
	for _, c := range prior {
		receipt.Changes = append(receipt.Changes, c)
	}
	if e = workspaceWriteJournal(receiptPath, receipt); e != nil {
		return rollback(e)
	}
	return workspaceClearJournal(jpath)
}
func workspaceSameWorkingFile(a, b WorkspaceFileState) bool {
	return workspaceSameFile(a, b) && (a.Permissions == 0 || b.Permissions == 0 || a.Permissions == b.Permissions)
}

func workspaceRollback(j workspaceApplyJournal) error {
	// First validate every preimage/postimage; never overwrite an ambiguous external edit.
	for _, c := range j.Changes {
		now, e := workspaceReadFile(j.TargetRoot, c.Path)
		if e != nil {
			return e
		}
		if !workspaceSameWorkingFile(now, c.Target) && !workspaceSameWorkingFile(now, c.Desired) {
			return fmt.Errorf("%w: recovery path %s changed", ErrWorkspaceConflict, c.Path)
		}
	}
	for path, tmp := range j.Prepared {
		if !workspaceSafeRelative(path) || filepath.Dir(tmp) != filepath.Dir(filepath.Join(j.TargetRoot, filepath.FromSlash(path))) || !strings.HasPrefix(filepath.Base(tmp), ".astonish-apply-") {
			return ErrWorkspaceOwnership
		}
		if e := checkWorkspaceFile(tmp); e != nil {
			return e
		}
	}
	for i := len(j.Changes) - 1; i >= 0; i-- {
		c := j.Changes[i]
		now, e := workspaceReadFile(j.TargetRoot, c.Path)
		if e != nil {
			return e
		}
		if workspaceSameWorkingFile(now, c.Target) {
			continue
		}
		if e = workspaceInstallFile(j.TargetRoot, c.Path, c.Target); e != nil {
			return e
		}
	}
	for _, tmp := range j.Prepared {
		if e := os.Remove(tmp); e != nil && !os.IsNotExist(e) {
			return e
		}
		// A crash after rollback must not resurrect an unlinked prepared file.
		if _, e := os.Stat(filepath.Dir(tmp)); e == nil {
			if e = workspaceSyncDir(filepath.Dir(tmp)); e != nil {
				return e
			}
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	for i := len(j.CreatedDirs) - 1; i >= 0; i-- {
		if e := os.Remove(j.CreatedDirs[i]); e != nil && !os.IsNotExist(e) {
			return e
		}
		parent := filepath.Dir(j.CreatedDirs[i])
		if _, e := os.Stat(parent); e == nil {
			if e = workspaceSyncDir(parent); e != nil {
				return e
			}
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	return nil
}

// RecoverApply deterministically restores journaled preimages. If an external writer
// changed a path, leave the journal intact and require manual reconciliation.
func (m *CodeWorkspaceManager) RecoverApply(ctx context.Context, p ProjectIdentity) error {
	l, path, e := m.integrationLock(ctx, p)
	if e != nil {
		return e
	}
	defer l.Unlock()
	if e = checkWorkspaceFile(path); e != nil {
		return e
	}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var j workspaceApplyJournal
	if e = json.Unmarshal(b, &j); e != nil {
		return e
	}
	if j.Version != 1 {
		return ErrWorkspaceOwnership
	}
	r, e := m.Get(ctx, p, j.WorkspaceID)
	if e != nil {
		return e
	}
	if e = m.Validate(ctx, *r); e != nil {
		return e
	}
	target, e := canonicalWorkspacePath(j.TargetRoot)
	if e != nil || target != j.TargetRoot {
		return ErrWorkspaceOwnership
	}
	lease, e := m.targetLease(ctx, p, target)
	if e != nil {
		return e
	}
	defer lease.Close()
	tp, e := ResolveCodeProject(ctx, target)
	if e != nil || tp.CommonDir != p.CommonDir {
		return ErrWorkspaceOwnership
	}
	s, e := inspectWorkspace(ctx, target)
	if e != nil {
		return e
	}
	if s.HEAD != j.TargetHEAD || s.IndexDigest != j.TargetIndexDigest || (j.TargetBranch != nil && s.Branch != *j.TargetBranch) {
		return ErrWorkspaceStale
	}
	receiptPath := filepath.Join(m.root, p.ID, "applied-"+j.WorkspaceID+".json")
	if e = checkWorkspaceFile(receiptPath); e != nil {
		return e
	}
	if len(j.PreviousReceipt) > 0 {
		var receipt workspaceApplyJournal
		if e = json.Unmarshal(j.PreviousReceipt, &receipt); e != nil {
			return e
		}
	}
	for _, c := range j.Changes {
		if !workspaceSafeRelative(c.Path) || c.Target.Unsupported != "" || c.Desired.Unsupported != "" ||
			(c.Target.Exists && c.Target.Mode != 0100644 && c.Target.Mode != 0100755) ||
			(c.Desired.Exists && c.Desired.Mode != 0100644 && c.Desired.Mode != 0100755) {
			return ErrWorkspaceOwnership
		}
	}
	for _, d := range j.CreatedDirs {
		rel, e := filepath.Rel(target, d)
		if e != nil || !workspaceSafeRelative(filepath.ToSlash(rel)) {
			return ErrWorkspaceOwnership
		}
		if e = workspaceSafeParents(target, filepath.ToSlash(rel)+"/placeholder"); e != nil {
			return e
		}
	}
	if e = workspaceRollback(j); e != nil {
		return errors.Join(ErrWorkspaceRecoveryRequired, e)
	}
	if e = workspaceRestoreReceipt(filepath.Join(m.root, p.ID, "applied-"+j.WorkspaceID+".json"), j.PreviousReceipt); e != nil {
		return errors.Join(ErrWorkspaceRecoveryRequired, e)
	}
	return workspaceClearJournal(path)
}
