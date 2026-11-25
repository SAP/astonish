package gitutil

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type WorkspaceRemovalReview struct {
	WorkspaceID string   `json:"workspace_id"`
	Digest      string   `json:"digest"`
	HEAD        string   `json:"head"`
	Eligible    bool     `json:"eligible"`
	Reasons     []string `json:"reasons"`
}

// CreateBranch attaches an inactive detached checkout to a NEW local branch.
// Existing branches are never reset, switched to or deleted.
func (m *CodeWorkspaceManager) CreateBranch(ctx context.Context, p ProjectIdentity, id, name string) error {
	l, jpath, e := m.integrationLock(ctx, p)
	if e != nil {
		return e
	}
	defer l.Unlock()
	if _, e = os.Lstat(jpath); e == nil {
		return ErrWorkspaceRecoveryRequired
	} else if !os.IsNotExist(e) {
		return e
	}
	lease, e := m.AcquireLease(ctx, p, id)
	if e != nil {
		return e
	}
	defer lease.Close()
	r, e := m.Get(ctx, p, id)
	if e != nil {
		return e
	}
	if e = workspaceOperationCheck(ctx, r.Root); e != nil {
		return e
	}
	if strings.HasPrefix(name, "-") || name == "" {
		return fmt.Errorf("invalid branch name")
	}
	if _, e = workspaceGit(ctx, r.Root, nil, "check-ref-format", "refs/heads/"+name); e != nil {
		return e
	}
	b, e := workspaceGitBytes(ctx, r.Root, "rev-parse", "--abbrev-ref", "HEAD")
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(b)) != "HEAD" {
		return fmt.Errorf("workspace must be detached before creating a branch")
	}
	// switch -c refuses an existing local ref and does not reset HEAD or work files.
	_, e = workspaceGit(ctx, r.Root, nil, "-c", "core.hooksPath=/dev/null", "switch", "-c", name)
	return e
}
func (m *CodeWorkspaceManager) removalReview(ctx context.Context, p ProjectIdentity, id string) (*WorkspaceRemovalReview, error) {
	r, e := m.Get(ctx, p, id)
	if e != nil {
		return nil, e
	}
	if e = m.Validate(ctx, *r); e != nil {
		return nil, e
	}
	s, e := inspectWorkspace(ctx, r.Root)
	if e != nil {
		return nil, e
	}
	review := &WorkspaceRemovalReview{WorkspaceID: id, HEAD: s.HEAD, Reasons: []string{}}
	status, e := workspaceGitBytes(ctx, r.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if e != nil {
		return nil, e
	}
	if len(status) > 0 {
		review.Reasons = append(review.Reasons, "tracked, staged or untracked changes")
	}
	ignored, e := workspaceGitBytes(ctx, r.Root, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if e != nil {
		return nil, e
	}
	if len(ignored) > 0 {
		review.Reasons = append(review.Reasons, "ignored data remains")
	}
	e = filepath.WalkDir(r.Root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if path == r.Root {
			return nil
		}
		if d.Name() == ".git" {
			if path == filepath.Join(r.Root, ".git") {
				return nil
			}
			review.Reasons = append(review.Reasons, "embedded repository")
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		if d.Type()&os.ModeSymlink != 0 {
			review.Reasons = append(review.Reasons, "symlink remains")
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if !st.IsDir() && !st.Mode().IsRegular() && st.Mode()&os.ModeSymlink == 0 {
			review.Reasons = append(review.Reasons, "special file remains")
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	refs, e := workspaceGitBytes(ctx, r.Root, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/", "refs/tags/")
	if e != nil {
		return nil, e
	}
	reachable, e := workspaceGitBytes(ctx, r.Root, "for-each-ref", "--contains="+s.HEAD, "--format=%(refname)", "refs/heads/", "refs/tags/")
	if e != nil {
		return nil, e
	}
	if len(reachable) == 0 {
		review.Reasons = append(review.Reasons, "HEAD commits are not preserved by a named branch or tag")
	}
	// Detached commits can be abandoned by reset/checkout while remaining only
	// in this worktree's HEAD reflog. Removing the checkout deletes that reflog.
	reflog, e := workspaceGitBytes(ctx, r.Root, "reflog", "show", "--format=%H", "HEAD")
	if e != nil {
		return nil, e
	}
	seenCommits := map[string]bool{s.HEAD: true}
	for _, commit := range strings.Fields(string(reflog)) {
		if seenCommits[commit] {
			continue
		}
		seenCommits[commit] = true
		preserved, err := workspaceGitBytes(ctx, r.Root, "for-each-ref", "--contains="+commit, "--format=%(refname)", "refs/heads/", "refs/tags/")
		if err != nil {
			return nil, err
		}
		if len(preserved) == 0 {
			review.Reasons = append(review.Reasons, "abandoned detached commits are not preserved by a named branch or tag")
			break
		}
	}
	// Retain the complete filesystem inventory, including ignored and empty directories,
	// in the digest, so newly-created ignored data invalidates a removal confirmation.
	inventory := map[string]WorkspaceFileState{}
	e = filepath.WalkDir(r.Root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if path == r.Root || path == filepath.Join(r.Root, ".git") {
			return nil
		}
		rel, e := filepath.Rel(r.Root, path)
		if e != nil {
			return e
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		f := WorkspaceFileState{Exists: true, Mode: uint32(st.Mode())}
		if st.Mode().IsRegular() {
			//nolint:gosec // G122: path is produced by WalkDir over the controlled workspace root; only regular files are read.
			f.Data, e = os.ReadFile(path)
			if e != nil {
				return e
			}
		} else if st.Mode()&os.ModeSymlink != 0 {
			link, e := os.Readlink(path)
			if e != nil {
				return e
			}
			f.Data = []byte(link)
		}
		inventory[rel] = f
		return nil
	})
	if e != nil {
		return nil, e
	}
	review.Eligible = len(review.Reasons) == 0
	review.Digest = workspaceJSONDigest(struct {
		Record                        WorkspaceRecord
		State                         workspaceInspection
		Status, Ignored, Refs, Reflog []byte
		Inventory                     map[string]WorkspaceFileState
	}{*r, s, status, ignored, refs, reflog, inventory})
	return review, nil
}

// ReviewRemoval refuses active checkouts and reports why retained data is not removable.
func (m *CodeWorkspaceManager) ReviewRemoval(ctx context.Context, p ProjectIdentity, id string) (*WorkspaceRemovalReview, error) {
	lease, e := m.AcquireLease(ctx, p, id)
	if e != nil {
		return nil, e
	}
	defer lease.Close()
	return m.removalReview(ctx, p, id)
}

// Remove requires explicit confirmation of a fresh eligible review; transcripts and
// named branches are retained. git worktree remove is never passed --force.
func (m *CodeWorkspaceManager) Remove(ctx context.Context, p ProjectIdentity, id, digest string, confirmed bool) error {
	if !confirmed || digest == "" {
		return fmt.Errorf("explicit removal confirmation required")
	}
	l, jpath, e := m.integrationLock(ctx, p)
	if e != nil {
		return e
	}
	defer l.Unlock()
	if _, e = os.Lstat(jpath); e == nil {
		return ErrWorkspaceRecoveryRequired
	} else if !os.IsNotExist(e) {
		return e
	}
	lease, e := m.AcquireLease(ctx, p, id)
	if e != nil {
		return e
	}
	defer lease.Close()
	review, e := m.removalReview(ctx, p, id)
	if e != nil {
		return e
	}
	if review.Digest != digest {
		return ErrWorkspaceStale
	}
	if !review.Eligible {
		return fmt.Errorf("%w: %s", ErrWorkspaceConflict, strings.Join(review.Reasons, "; "))
	}
	r, e := m.Get(ctx, p, id)
	if e != nil {
		return e
	}
	if e = m.Validate(ctx, *r); e != nil {
		return e
	}
	if _, e = workspaceGit(ctx, p.OriginalRoot, nil, "worktree", "remove", "--", r.Root); e != nil {
		return e
	}
	r.State = WorkspaceRemoved
	r.LastError = ""
	return m.store(ctx, *r, false)
}
