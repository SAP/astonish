package gitutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// WorkspaceChangeKind describes whole-file changes; apply never performs a line merge.
type WorkspaceChangeKind string

const (
	WorkspaceChangeAdd    WorkspaceChangeKind = "add"
	WorkspaceChangeModify WorkspaceChangeKind = "modify"
	WorkspaceChangeDelete WorkspaceChangeKind = "delete"
)

type WorkspaceFileState struct {
	Exists      bool   `json:"exists"`
	Mode        uint32 `json:"mode"`
	Permissions uint32 `json:"permissions,omitempty"`
	Data        []byte `json:"data,omitempty"`
	Unsupported string `json:"unsupported,omitempty"`
}
type WorkspaceChange struct {
	Path        string              `json:"path"`
	Kind        WorkspaceChangeKind `json:"kind"`
	Base        WorkspaceFileState  `json:"base"`
	Desired     WorkspaceFileState  `json:"desired"`
	Target      WorkspaceFileState  `json:"target"`
	Conflict    bool                `json:"conflict"`
	Unsupported string              `json:"unsupported,omitempty"`
}
type WorkspaceReview struct {
	WorkspaceID       string            `json:"workspace_id"`
	TargetRoot        string            `json:"target_root"`
	SourceHEAD        string            `json:"source_head"`
	TargetHEAD        string            `json:"target_head"`
	SourceIndexDigest string            `json:"source_index_digest"`
	TargetIndexDigest string            `json:"target_index_digest"`
	Digest            string            `json:"digest"`
	Changes           []WorkspaceChange `json:"changes"`
}
type workspaceInspection struct {
	HEAD        string
	Branch      string
	IndexDigest string
	Files       map[string]WorkspaceFileState
	Tracked     map[string]bool
}

// workspaceGitBytes must not trim output: a blob can end in arbitrary newlines.
func workspaceGitBytes(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), e, stderr.String())
	}
	return b, nil
}
func workspaceSafeRelative(path string) bool {
	if path == "" || strings.ContainsRune(path, 0) || strings.Contains(path, "\\") || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == "." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

// Do not follow a destination symlink, even when its resolved path is in the checkout.
func workspaceSafeParents(root, path string) error {
	if !workspaceSafeRelative(path) {
		return ErrWorkspaceOwnership
	}
	for q := filepath.Dir(filepath.Join(root, filepath.FromSlash(path))); q != root; q = filepath.Dir(q) {
		st, e := os.Lstat(q)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: unsafe parent %s", ErrWorkspaceUnsupported, q)
		}
	}
	return nil
}
func workspaceReadFile(root, path string) (WorkspaceFileState, error) {
	var f WorkspaceFileState
	if e := workspaceSafeParents(root, path); e != nil {
		return f, e
	}
	st, e := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
	if os.IsNotExist(e) {
		return f, nil
	}
	if e != nil {
		return f, e
	}
	f.Exists = true
	f.Permissions = uint32(st.Mode().Perm())
	if !st.Mode().IsRegular() {
		f.Unsupported = "symlink, directory or special file"
		return f, nil
	}
	f.Mode = 0100644
	if st.Mode()&0111 != 0 {
		f.Mode = 0100755
	}
	f.Data, e = os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	return f, e
}
func workspaceSameFile(a, b WorkspaceFileState) bool {
	return a.Exists == b.Exists && a.Mode == b.Mode && a.Unsupported == b.Unsupported && bytes.Equal(a.Data, b.Data)
}
func workspaceOperationCheck(ctx context.Context, root string) error {
	p, e := ResolveCodeProject(ctx, root)
	if e != nil {
		return e
	}
	if e = validateWorkspaceSource(ctx, p); e != nil {
		return e
	}
	gd, e := workspaceGit(ctx, root, nil, "rev-parse", "--absolute-git-dir")
	if e != nil {
		return e
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "rebase-apply", "rebase-merge", "sequencer", "index.lock"} {
		if _, e = os.Lstat(filepath.Join(gd, name)); e == nil {
			return fmt.Errorf("%w: Git operation in progress (%s)", ErrWorkspaceBusy, name)
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	return nil
}
func inspectWorkspace(ctx context.Context, root string) (workspaceInspection, error) {
	s := workspaceInspection{Files: map[string]WorkspaceFileState{}, Tracked: map[string]bool{}}
	if e := workspaceOperationCheck(ctx, root); e != nil {
		return s, e
	}
	var e error
	s.HEAD, e = workspaceGit(ctx, root, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if e != nil {
		return s, e
	}
	b, e := workspaceGitBytes(ctx, root, "symbolic-ref", "-q", "HEAD")
	if e == nil {
		s.Branch = strings.TrimSpace(string(b))
	} else {
		detached, de := workspaceGit(ctx, root, nil, "rev-parse", "--abbrev-ref", "HEAD")
		if de != nil || detached != "HEAD" {
			return s, e
		}
	}
	_, b, e = sourceWorkspaceIndex(ctx, root)
	if e != nil {
		return s, e
	}
	s.IndexDigest = workspaceDigest(b)
	tracked, e := workspaceGitBytes(ctx, root, "ls-files", "-z")
	if e != nil {
		return s, e
	}
	others, e := workspaceGitBytes(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if e != nil {
		return s, e
	}
	for _, path := range strings.Split(string(tracked), "\x00") {
		if path != "" {
			s.Tracked[path] = true
		}
	}
	for _, path := range strings.Split(string(append(tracked, others...)), "\x00") {
		if path == "" {
			continue
		}
		f, e := workspaceReadFile(root, path)
		if e != nil {
			return s, e
		}
		s.Files[path] = f
	}
	return s, nil
}
func workspaceBaseline(ctx context.Context, root, tree string) (map[string]WorkspaceFileState, error) {
	files := map[string]WorkspaceFileState{}
	b, e := workspaceGitBytes(ctx, root, "ls-tree", "-r", "-z", tree)
	if e != nil {
		return nil, e
	}
	for _, line := range strings.Split(string(b), "\x00") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || !workspaceSafeRelative(parts[1]) {
			return nil, ErrWorkspaceOwnership
		}
		fields := strings.Fields(parts[0])
		if len(fields) != 3 {
			return nil, ErrWorkspaceOwnership
		}
		mode, e := strconv.ParseUint(fields[0], 8, 32)
		if e != nil {
			return nil, e
		}
		f := WorkspaceFileState{Exists: true, Mode: uint32(mode)}
		if fields[0] != "100644" && fields[0] != "100755" {
			f.Unsupported = "baseline symlink or submodule"
		} else {
			f.Data, e = workspaceGitBytes(ctx, root, "cat-file", "blob", fields[2])
			if e != nil {
				return nil, e
			}
		}
		files[parts[1]] = f
	}
	return files, nil
}

// inspectWorkspaceBaseline includes initially copied paths even if later ignored.
func inspectWorkspaceBaseline(ctx context.Context, root, tree string) (workspaceInspection, error) {
	s, e := inspectWorkspace(ctx, root)
	if e != nil {
		return s, e
	}
	base, e := workspaceBaseline(ctx, root, tree)
	if e != nil {
		return s, e
	}
	for path := range base {
		s.Files[path], e = workspaceReadFile(root, path)
		if e != nil {
			return s, e
		}
	}
	return s, nil
}

func workspaceJSONDigest(v any) string { b, _ := json.Marshal(v); return workspaceDigest(b) }

// Review compares raw working files, including committed changes, against the recorded
// initial full tree. targetDir defaults to the canonical original project checkout.
// The digest includes HEAD, index and all nonignored working paths on both sides.
func (m *CodeWorkspaceManager) Review(ctx context.Context, p ProjectIdentity, id, targetDir string) (*WorkspaceReview, error) {
	r, e := m.Get(ctx, p, id)
	if e != nil {
		return nil, e
	}
	if e = m.Validate(ctx, *r); e != nil {
		return nil, e
	}
	if targetDir == "" {
		targetDir = r.Project.OriginalRoot
	}
	target, e := canonicalWorkspacePath(targetDir)
	if e != nil {
		return nil, e
	}
	tp, e := ResolveCodeProject(ctx, target)
	if e != nil {
		return nil, e
	}
	if tp.CommonDir != r.Project.CommonDir || tp.OriginalRoot != target || target == r.Root {
		return nil, ErrWorkspaceOwnership
	}
	source, e := inspectWorkspace(ctx, r.Root)
	if e != nil {
		return nil, e
	}
	dest, e := inspectWorkspace(ctx, target)
	if e != nil {
		return nil, e
	}
	base, e := workspaceBaseline(ctx, r.Root, r.FullTree)
	if e != nil {
		return nil, e
	}
	paths := map[string]bool{}
	for path := range base {
		paths[path] = true
		source.Files[path], e = workspaceReadFile(r.Root, path)
		if e != nil {
			return nil, e
		}
		dest.Files[path], e = workspaceReadFile(target, path)
		if e != nil {
			return nil, e
		}
	}
	for path := range source.Files {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	review := &WorkspaceReview{WorkspaceID: id, TargetRoot: target, SourceHEAD: source.HEAD, TargetHEAD: dest.HEAD, SourceIndexDigest: source.IndexDigest, TargetIndexDigest: dest.IndexDigest, Changes: []WorkspaceChange{}}
	for _, path := range ordered {
		b, w := base[path], source.Files[path]
		// Recorded initially copied paths remain relevant even if now ignored.
		if workspaceSameFile(b, w) {
			continue
		}
		t, e := workspaceReadFile(target, path)
		if e != nil {
			return nil, e
		}
		c := WorkspaceChange{Path: path, Base: b, Desired: w, Target: t, Kind: WorkspaceChangeModify}
		if !b.Exists {
			c.Kind = WorkspaceChangeAdd
		}
		if !w.Exists {
			c.Kind = WorkspaceChangeDelete
		}
		c.Unsupported = b.Unsupported
		if w.Unsupported != "" {
			c.Unsupported = w.Unsupported
		}
		if t.Unsupported != "" {
			c.Unsupported = t.Unsupported
		}
		c.Conflict = !workspaceSameFile(t, b) && !workspaceSameFile(t, w)
		// A new source file must not replace an independently-created untracked file.
		if !b.Exists && t.Exists && !dest.Tracked[path] {
			c.Conflict = true
			// A retained successful-apply receipt distinguishes our own untracked
			// addition from an independently-created collision.
			receiptPath := filepath.Join(m.root, p.ID, "applied-"+id+".json")
			if err := checkWorkspaceFile(receiptPath); err != nil {
				return nil, err
			}
			data, err := os.ReadFile(receiptPath)
			if err == nil {
				var receipt workspaceApplyJournal
				if err = json.Unmarshal(data, &receipt); err != nil {
					return nil, err
				}
				if receipt.TargetRoot == target {
					for _, prior := range receipt.Changes {
						if prior.Path == path && workspaceSameFile(prior.Desired, t) && workspaceSameFile(t, w) {
							c.Conflict = false
						}
					}
				}
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
		review.Changes = append(review.Changes, c)
	}
	review.Digest = workspaceJSONDigest(struct {
		Record         WorkspaceRecord
		Source, Target workspaceInspection
		Review         *WorkspaceReview
	}{*r, source, dest, review})
	// Optimistically detect writers during the inspection without modifying their indexes.
	s2, e := inspectWorkspace(ctx, r.Root)
	if e != nil {
		return nil, e
	}
	d2, e := inspectWorkspace(ctx, target)
	if e != nil {
		return nil, e
	}
	for path := range base {
		s2.Files[path], e = workspaceReadFile(r.Root, path)
		if e != nil {
			return nil, e
		}
		d2.Files[path], e = workspaceReadFile(target, path)
		if e != nil {
			return nil, e
		}
	}
	if workspaceJSONDigest(source) != workspaceJSONDigest(s2) || workspaceJSONDigest(dest) != workspaceJSONDigest(d2) {
		return nil, ErrWorkspaceStale
	}
	return review, nil
}
