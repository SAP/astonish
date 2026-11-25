package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SAP/astonish/pkg/bot"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

// BotTaskWorkspace is an optional dependency. It never provisions storage or
// grants access based on a file path, receipt, or caller-provided Home binding.
var _ bot.TaskWorkspace = (*BotTaskWorkspace)(nil)
var _ bot.WorkspaceScriptBackend = (*BotTaskWorkspace)(nil)

type BotTaskWorkspace struct {
	Backend       sandbox.BotHomeWorkspaceBackend
	Bots          store.BotStore
	Tasks         store.BotTaskStore
	commitBarrier func(context.Context, store.BotTaskMutation, func() error) error
}

func (w *BotTaskWorkspace) binding(c context.Context, b store.BotID) (*sandbox.BotHomeBinding, error) {
	if w == nil || w.Backend == nil || w.Bots == nil {
		return nil, store.ErrBotUnsupported
	}
	p, ok := execution.PrincipalFromContext(c)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		return nil, store.ErrBotForbidden
	}
	bot, e := w.Bots.Get(c, b)
	if e != nil {
		return nil, e
	}
	if bot == nil || bot.OwnerID.String() != p.Subject {
		return nil, store.ErrBotForbidden
	}
	h := bot.Home
	if h.ID == uuid.Nil || h.BotID != b || h.Scope != bot.Scope || h.Revision < 1 {
		return nil, store.ErrBotUnsupported
	}
	binding := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: b.String()}, Backend: sandbox.BackendKind(h.Backend), Ref: h.StorageKey}
	if binding.Validate() != nil {
		return nil, store.ErrBotConflict
	}
	if binder, ok := w.Backend.(sandbox.BotHomeSessionBinder); ok {
		if bot.HomeSessionID == uuid.Nil {
			return nil, store.ErrBotConflict
		}
		if err := binder.BindBotHomeSession(binding, bot.HomeSessionID.String()); err != nil {
			return nil, err
		}
	}
	return binding, nil
}
func (w *BotTaskWorkspace) authorize(c context.Context, m store.BotTaskMutation) (*sandbox.BotHomeBinding, error) {
	h, e := w.binding(c, m.BotID)
	if e != nil {
		return nil, e
	}
	if e := c.Err(); e != nil {
		return nil, e
	}
	if identity, worker := execution.BotRunIdentityFromContext(c); worker {
		if identity.Validate() != nil || identity.BotID != m.BotID || identity.RunID != m.Lease.RunID || m.Lease.BotID != m.BotID || m.Lease.RunID == uuid.Nil {
			return nil, store.ErrBotForbidden
		}
	} else if m.Lease.RunID == uuid.Nil {
		return h, nil
	}
	run, e := w.Bots.GetRun(c, m.BotID, m.Lease.RunID)
	if e != nil {
		return nil, e
	}
	if run == nil || run.Status != store.RunRunning || run.Lease.BotID != m.BotID || run.Lease.RunID != m.Lease.RunID || run.Lease.Fence != m.Lease.Fence || run.Lease.WorkerID != m.Lease.WorkerID || !time.Now().Before(run.Lease.ExpiresAt) {
		return nil, store.ErrBotConflict
	}
	return h, nil
}

// Install connects only the trusted capability to the scoped repository.
func (w *BotTaskWorkspace) Install() error {
	if w == nil || w.Tasks == nil || w.Backend == nil {
		return store.ErrBotUnsupported
	}
	return entstore.InstallBotTaskWorkspaceVerifier(w.Bots, w.VerifyReceipt)
}
func (w *BotTaskWorkspace) VerifyReceipt(c context.Context, p store.BotTaskPublication, ref string) error {
	h, e := w.binding(c, p.BotID)
	if e != nil {
		return e
	}
	r, e := w.Backend.ResolveBotHomeWorkspaceReceipt(c, h, ref)
	if e != nil {
		return e
	}
	if r == nil || r.ReceiptRef != ref || r.PublicationID != p.ID.String() || r.BindingKey != h.Scope.Key() || r.TaskPath != "tasks/"+p.TaskID.String() || len(r.Entries) != len(p.Candidate.Manifest.Assets) {
		return store.ErrBotConflict
	}
	expected := map[string]store.BotTaskAsset{}
	for _, a := range p.Candidate.Manifest.Assets {
		expected[a.Path] = a
	}
	for _, a := range r.Entries {
		v, ok := expected[a.Path]
		if !ok || v.Digest != a.Digest || v.SizeBytes != a.SizeBytes || v.Executable != a.Executable {
			return store.ErrBotConflict
		}
		delete(expected, a.Path)
	}
	if len(expected) != 0 {
		return store.ErrBotConflict
	}
	return nil
}
func (w *BotTaskWorkspace) Read(c context.Context, b store.BotID, p string, n int64) ([]byte, error) {
	h, e := w.binding(c, b)
	if e != nil {
		return nil, e
	}
	// Canonical executable reads follow the registry-selected revision, not a
	// backend head left ahead by a crash before the database commit.
	parts := strings.Split(p, "/")
	if sandbox.ValidateBotHomeArtifactPath(p) != nil {
		return nil, store.ErrBotInvalid
	}
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return nil, store.ErrBotForbidden
		}
	}
	// Knowledge and skill working files live in ordinary retained Home, not the
	// private task receipt/authority root. They are untrusted content; reading
	// them cannot select a runnable revision or authorize a script.
	if len(parts) >= 2 && (parts[0] == "skills" || parts[0] == "knowledge" || parts[0] == "goals") {
		return w.readWorkingFile(c, h, p, n)
	}
	if len(parts) >= 3 && parts[0] == "tasks" && parts[2] != "state" && parts[2] != "outputs" && parts[2] != "drafts" {
		if w.Tasks == nil || parts[2] == ".revisions" || strings.HasPrefix(parts[2], ".") {
			return nil, store.ErrBotForbidden
		}
		id, err := uuid.Parse(parts[1])
		if err != nil {
			return nil, store.ErrBotInvalid
		}
		task, err := w.Tasks.GetTask(c, b, id)
		if err != nil {
			return nil, err
		}
		if task == nil || task.BotID != b || task.ID != id || task.Head == nil {
			return nil, store.ErrBotNotFound
		}
		if n < 1 || task.Head.Validate() != nil {
			return nil, store.ErrBotInvalid
		}
		revision, err := w.Tasks.GetTaskRevision(c, b, *task.Head)
		if err != nil {
			return nil, err
		}
		if revision == nil || revision.ID == uuid.Nil || revision.BotID != b || revision.Ref != *task.Head {
			return nil, store.ErrBotConflict
		}
		// An explicit read fetches only the requested retained asset; execution
		// still verifies the complete snapshot and never a writable working copy.
		path := strings.Join(parts[2:], "/")
		for _, asset := range revision.Manifest.Assets {
			if asset.Path == path {
				if asset.SizeBytes > n {
					return nil, store.ErrBotInvalid
				}
				return w.readAsset(c, h, revision, asset)
			}
		}
		return nil, store.ErrBotNotFound
	}
	if len(parts) < 4 || parts[0] != "tasks" || (parts[2] != "state" && parts[2] != "outputs" && parts[2] != "drafts") {
		return nil, store.ErrBotForbidden
	}
	return w.Backend.ReadBotHomeWorkspace(c, h, p, n)
}
func (w *BotTaskWorkspace) readWorkingFile(c context.Context, h *sandbox.BotHomeBinding, path string, n int64) ([]byte, error) {
	if n < 1 || n > sandbox.MaxBotHomeWorkspaceAssetBytes || sandbox.ValidateBotHomeArtifactPath(path) != nil {
		return nil, store.ErrBotInvalid
	}
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, ".") {
			return nil, store.ErrBotForbidden
		}
	}
	files, ok := w.Backend.(sandbox.BotHomeFilesBackend)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	stream, err := files.OpenBotHomeFile(c, h, path, store.BotFileRange{})
	if err != nil {
		return nil, err
	}
	if stream == nil || stream.ReadCloser == nil {
		return nil, store.ErrBotConflict
	}
	data, readErr := io.ReadAll(io.LimitReader(stream, n+1))
	if err := errors.Join(readErr, stream.Close()); err != nil {
		return nil, err
	}
	if int64(len(data)) > n {
		return nil, store.ErrBotInvalid
	}
	return data, nil
}

func (w *BotTaskWorkspace) Write(c context.Context, m store.BotTaskMutation, f sandbox.BotHomeWorkspaceFile) error {
	h, e := w.authorize(c, m)
	if e != nil {
		return e
	}
	c = w.guardedContext(c, m, nil)
	f.BeforeCommit = w.barrier(c, m)
	return w.Backend.WriteBotHomeWorkspace(c, h, f)
}
func (w *BotTaskWorkspace) Publish(c context.Context, q store.BotTaskPrepareRequest) (*store.BotTask, error) {
	if w == nil || w.Tasks == nil {
		return nil, store.ErrBotUnsupported
	}
	h, e := w.authorize(c, q.Mutation)
	if e != nil {
		return nil, e
	}
	p, e := w.Tasks.PrepareTaskPublication(c, q)
	if e != nil {
		return nil, e
	}
	if p.State == store.BotTaskPublicationCommitted {
		return w.Tasks.GetTask(c, p.BotID, p.TaskID)
	}
	if p.State != store.BotTaskPublicationPrepared && p.State != store.BotTaskPublicationFilesReady {
		return nil, store.ErrBotConflict
	}
	var expected string
	if p.ExpectedHead != nil {
		if _, e := w.Load(c, q.Task.BotID, *p.ExpectedHead); e != nil {
			return nil, e
		}
	}
	files := []sandbox.BotHomeWorkspaceFile{}
	for _, a := range p.Candidate.Manifest.Assets {
		files = append(files, sandbox.BotHomeWorkspaceFile{Path: a.Path, Data: p.Candidate.Bundle[a.Path], Executable: a.Executable})
	}
	if _, e = w.authorize(c, q.Mutation); e != nil {
		return nil, e
	}
	// A physically published Held/Aborted candidate can leave the backend head
	// ahead of the registry. CAS against that verified physical head; only the
	// registry-selected revision supplies current executable authority.
	if snap, err := w.Backend.SnapshotBotHomeWorkspace(c, h, q.Task.HomePath, sandbox.MaxBotHomeWorkspaceBundleBytes); err == nil {
		expected = snap.Receipt.Digest
	} else if p.ExpectedHead != nil {
		return nil, err
	}
	c = w.guardedContext(c, q.Mutation, nil)
	receipt, e := w.Backend.PublishBotHomeWorkspace(c, h, sandbox.BotHomeWorkspacePublishRequest{PublicationID: p.ID.String(), TaskPath: q.Task.HomePath, ExpectedDigest: expected, Files: files, BeforeCommit: w.barrier(c, q.Mutation)})
	if e != nil {
		return nil, e
	}
	if _, e = w.authorize(c, q.Mutation); e != nil {
		return nil, e
	}
	return w.Tasks.CommitTaskPublication(c, store.BotTaskCommitRequest{Mutation: q.Mutation, PublicationID: p.ID, WorkspaceReceiptRef: receipt.ReceiptRef})
}

type workspaceLiveGuardKey struct{}
type workspaceLiveGuard struct{ check func(context.Context) error }

func (w *BotTaskWorkspace) guardedContext(c context.Context, m store.BotTaskMutation, budget execution.RunBudget) context.Context {
	state := &atomic.Pointer[workspaceLiveGuard]{}
	c = context.WithValue(c, workspaceLiveGuardKey{}, state)
	return sandbox.WithBotHomeWorkspaceCommitGuard(c, func(live context.Context) error {
		if budget != nil {
			if err := budget.Check(live); err != nil {
				return err
			}
		}
		if guard := state.Load(); guard != nil {
			return guard.check(live)
		}
		_, err := w.authorize(live, m)
		return err
	})
}

func (w *BotTaskWorkspace) barrier(c context.Context, m store.BotTaskMutation) sandbox.BotHomeWorkspaceCommitBarrier {
	return func(commit func() error) error {
		if _, e := w.authorize(c, m); e != nil {
			return e
		}
		if w.commitBarrier != nil {
			return w.commitBarrier(c, m, commit)
		}
		return entstore.WithBotTaskWorkspaceCommitGuard(c, w.Bots, m, func(check func(context.Context) error) error {
			state, ok := c.Value(workspaceLiveGuardKey{}).(*atomic.Pointer[workspaceLiveGuard])
			if !ok {
				return store.ErrBotConflict
			}
			state.Store(&workspaceLiveGuard{check: check})
			defer state.Store(nil)
			return commit()
		})
	}
}

func (w *BotTaskWorkspace) Execute(c context.Context, m store.BotTaskMutation, ref store.BotTaskRef, script string, budget execution.RunBudget) (*sandbox.BotHomeWorkspaceScriptResult, error) {
	h, e := w.authorize(c, m)
	if e != nil {
		return nil, e
	}
	identity, ok := execution.BotRunIdentityFromContext(c)
	if !ok || identity.RunID != m.Lease.RunID || m.Lease.RunID == uuid.Nil || budget == nil {
		return nil, store.ErrBotForbidden
	}
	backend, ok := w.Backend.(sandbox.BotHomeWorkspaceScriptBackend)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	if ref.Validate() != nil || store.ValidateBotTaskAssetPath(script) != nil || !strings.HasPrefix(script, "scripts/") || !strings.HasSuffix(script, ".py") {
		return nil, store.ErrBotInvalid
	}
	r, e := w.Load(c, m.BotID, ref)
	if e != nil {
		return nil, e
	}
	if r.Ref != ref || r.BotID != m.BotID {
		return nil, store.ErrBotConflict
	}
	var digest string
	for _, a := range r.Manifest.Assets {
		if a.Path == script && a.Kind == store.BotTaskAssetScript && a.Executable {
			digest = a.Digest
		}
	}
	if digest == "" {
		return nil, store.ErrBotForbidden
	}
	if e = budget.ReserveTool(c, "bot_workspace_script"); e != nil {
		return nil, e
	}
	if e = budget.Check(c); e != nil {
		return nil, e
	}
	c = w.guardedContext(c, m, budget)
	guard := w.barrier(c, m)
	return backend.RunBotHomeWorkspaceScript(c, h, sandbox.BotHomeWorkspaceScriptRequest{TaskPath: "tasks/" + ref.TaskID.String(), PublicationID: r.ID.String(), ScriptPath: script, Digest: digest, BeforeCommit: func(commit func() error) error {
		if e := budget.Check(c); e != nil {
			return e
		}
		return guard(commit)
	}})
}

func (w *BotTaskWorkspace) Load(c context.Context, b store.BotID, ref store.BotTaskRef) (*store.BotTaskRevision, error) {
	if w == nil || w.Tasks == nil {
		return nil, store.ErrBotUnsupported
	}
	h, e := w.binding(c, b)
	if e != nil {
		return nil, e
	}
	if ref.Validate() != nil {
		return nil, store.ErrBotInvalid
	}
	r, e := w.Tasks.GetTaskRevision(c, b, ref)
	if e != nil {
		return nil, e
	}
	if r == nil || r.ID == uuid.Nil || r.BotID != b || r.Ref != ref {
		return nil, store.ErrBotConflict
	}
	if len(r.Manifest.Assets) > store.BotTaskMaxAssets {
		return nil, store.ErrBotConflict
	}
	// The registry selects the immutable revision and its asset metadata. Bundle
	// is a transient hydration, not a second content authority: reconstruct it
	// exclusively from retained bytes and verify each declared digest and size.
	bundle := make(map[string][]byte, len(r.Manifest.Assets))
	var size int64
	for _, a := range r.Manifest.Assets {
		if store.ValidateBotTaskAssetPath(a.Path) != nil || a.SizeBytes < 0 || a.SizeBytes > sandbox.MaxBotHomeWorkspaceAssetBytes {
			return nil, store.ErrBotConflict
		}
		if _, duplicate := bundle[a.Path]; duplicate {
			return nil, store.ErrBotConflict
		}
		size += a.SizeBytes
		if size > sandbox.MaxBotHomeWorkspaceBundleBytes {
			return nil, store.ErrBotConflict
		}
		data, err := w.readAsset(c, h, r, a)
		if err != nil {
			return nil, err
		}
		bundle[a.Path] = data
	}
	out := *r
	out.Bundle = bundle
	return &out, nil
}

func (w *BotTaskWorkspace) readAsset(c context.Context, h *sandbox.BotHomeBinding, r *store.BotTaskRevision, a store.BotTaskAsset) ([]byte, error) {
	if store.ValidateBotTaskAssetPath(a.Path) != nil || a.SizeBytes < 0 || a.SizeBytes > sandbox.MaxBotHomeWorkspaceAssetBytes {
		return nil, store.ErrBotConflict
	}
	data, err := w.Backend.ReadBotHomeWorkspace(c, h, "tasks/"+r.Ref.TaskID.String()+"/.revisions/"+r.ID.String()+"/"+a.Path, sandbox.MaxBotHomeWorkspaceAssetBytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != a.SizeBytes || hex.EncodeToString(sum[:]) != a.Digest {
		return nil, store.ErrBotConflict
	}
	return data, nil
}
