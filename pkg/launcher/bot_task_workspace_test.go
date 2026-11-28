package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type taskWorkspaceFake struct {
	sandbox.BotHomeWorkspaceBackend
	receipt     sandbox.BotHomeWorkspaceReceipt
	snap        sandbox.BotHomeWorkspaceSnapshot
	wrote       bool
	beforeWrite func()
	published   bool
}

func workspaceFakeHandshake(c context.Context) error {
	var approval bytes.Buffer
	stream := sandbox.WorkspaceCommitStream{Context: c, Output: io.Discard, Approve: workspaceFakeApproval{&approval}}
	_, err := stream.Write([]byte("WORKSPACE_READY\n"))
	return err
}

type workspaceFakeApproval struct{ *bytes.Buffer }

func (workspaceFakeApproval) Close() error { return nil }

func (f *taskWorkspaceFake) PublishBotHomeWorkspace(c context.Context, _ *sandbox.BotHomeBinding, q sandbox.BotHomeWorkspacePublishRequest) (*sandbox.BotHomeWorkspaceReceipt, error) {
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	if err := workspaceFakeHandshake(c); err != nil {
		return nil, err
	}
	if q.BeforeCommit == nil {
		return nil, errors.New("missing publication barrier")
	}
	if e := q.BeforeCommit(func() error { f.published = true; return nil }); e != nil {
		return nil, e
	}
	return &f.receipt, nil
}

func (f *taskWorkspaceFake) ResolveBotHomeWorkspaceReceipt(context.Context, *sandbox.BotHomeBinding, string) (*sandbox.BotHomeWorkspaceReceipt, error) {
	return &f.receipt, nil
}
func (f *taskWorkspaceFake) ReadBotHomeWorkspace(_ context.Context, _ *sandbox.BotHomeBinding, p string, _ int64) ([]byte, error) {
	if strings.Contains(p, "/.revisions/") {
		for _, file := range f.snap.Files {
			if strings.HasSuffix(p, "/"+file.Path) {
				return file.Data, nil
			}
		}
	}
	return []byte("retained"), nil
}
func (f *taskWorkspaceFake) WriteBotHomeWorkspace(c context.Context, _ *sandbox.BotHomeBinding, file sandbox.BotHomeWorkspaceFile) error {
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	if err := workspaceFakeHandshake(c); err != nil {
		return err
	}
	commit := func() error { f.wrote = true; return nil }
	if file.BeforeCommit != nil {
		return file.BeforeCommit(commit)
	}
	return commit()
}
func (f *taskWorkspaceFake) SnapshotBotHomeWorkspace(context.Context, *sandbox.BotHomeBinding, string, int64) (*sandbox.BotHomeWorkspaceSnapshot, error) {
	return &f.snap, nil
}

type taskWorkspaceRegistry struct {
	store.BotTaskStore
	revision  store.BotTaskRevision
	committed *bool
}

func (r taskWorkspaceRegistry) PrepareTaskPublication(_ context.Context, q store.BotTaskPrepareRequest) (*store.BotTaskPublication, error) {
	return &store.BotTaskPublication{ID: q.Candidate.ID, BotID: q.Task.BotID, TaskID: q.Task.ID, State: store.BotTaskPublicationPrepared, Candidate: q.Candidate}, nil
}
func (r taskWorkspaceRegistry) CommitTaskPublication(_ context.Context, q store.BotTaskCommitRequest) (*store.BotTask, error) {
	if r.committed != nil {
		*r.committed = true
	}
	return &store.BotTask{BotID: q.Mutation.BotID}, nil
}

func (r taskWorkspaceRegistry) GetTask(_ context.Context, b store.BotID, id uuid.UUID) (*store.BotTask, error) {
	return &store.BotTask{ID: id, BotID: b, Head: r.revision.Ref.Clone()}, nil
}
func (r taskWorkspaceRegistry) GetTaskRevision(context.Context, store.BotID, store.BotTaskRef) (*store.BotTaskRevision, error) {
	return r.revision.Clone(), nil
}

type taskWorkspaceScriptFake struct {
	*taskWorkspaceFake
	ran     bool
	request sandbox.BotHomeWorkspaceScriptRequest
}

func (f *taskWorkspaceScriptFake) RunBotHomeWorkspaceScript(c context.Context, _ *sandbox.BotHomeBinding, q sandbox.BotHomeWorkspaceScriptRequest) (*sandbox.BotHomeWorkspaceScriptResult, error) {
	f.request = q
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	if err := workspaceFakeHandshake(c); err != nil {
		return nil, err
	}
	err := q.BeforeCommit(func() error { f.ran = true; return nil })
	return &sandbox.BotHomeWorkspaceScriptResult{Stdout: "verified-output"}, err
}

type taskWorkspaceBudget struct {
	execution.RunBudget
	calls int
	err   error
}

func (b *taskWorkspaceBudget) ReserveTool(context.Context, string) error { b.calls++; return b.err }
func (b *taskWorkspaceBudget) Check(context.Context) error               { return b.err }

func TestBotTaskWorkspaceScriptAuthorization(t *testing.T) {
	owner, id, run, task := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	c, _ := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	c = execution.WithBotRunIdentity(c, execution.BotRunIdentity{BotID: id, RunID: run, Version: 1, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	lease := store.BotLease{BotID: id, RunID: run, Fence: 1, WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute)}
	bots := homeManagerStore{b: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopeTeam, Home: store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: store.BotScopeTeam, Backend: "docker", StorageKey: "registered", Revision: 1}}, r: store.BotRun{ID: run, BotID: id, Status: store.RunRunning, Lease: lease}}
	file := sandbox.BotHomeWorkspaceFile{Path: "scripts/helper.py", Data: []byte("print('verified-output')"), Executable: true}
	ref := store.BotTaskRef{TaskID: task, Revision: 1, Digest: strings.Repeat("a", 64)}
	r := store.BotTaskRevision{ID: uuid.New(), BotID: id, Ref: ref, Manifest: store.BotTaskManifest{Assets: []store.BotTaskAsset{{Path: file.Path, Kind: store.BotTaskAssetScript, Digest: file.Digest(), SizeBytes: int64(len(file.Data)), Executable: true}}}, Bundle: map[string][]byte{file.Path: file.Data}}
	backend := &taskWorkspaceScriptFake{taskWorkspaceFake: &taskWorkspaceFake{snap: sandbox.BotHomeWorkspaceSnapshot{Files: []sandbox.BotHomeWorkspaceFile{file}}}}
	w := &BotTaskWorkspace{Backend: backend, Bots: bots, Tasks: taskWorkspaceRegistry{revision: r}, commitBarrier: func(_ context.Context, _ store.BotTaskMutation, commit func() error) error { return commit() }}
	m := store.BotTaskMutation{BotID: id, Lease: lease}
	budget := &taskWorkspaceBudget{}
	result, e := w.Execute(c, m, ref, file.Path, budget)
	if e != nil || !backend.ran || result.Stdout != "verified-output" || budget.calls != 1 || backend.request.PublicationID != r.ID.String() || backend.request.Digest != file.Digest() {
		t.Fatalf("script wiring: %+v %v", result, e)
	}
	for _, path := range []string{"../escape.py", "state/a.py", "scripts/unregistered.py"} {
		backend.ran = false
		if _, e = w.Execute(c, m, ref, path, budget); e == nil || backend.ran {
			t.Fatal("unconfined or unregistered script", path)
		}
	}
	backend.ran = false
	bad := ref
	bad.Revision++
	if _, e = w.Execute(c, m, bad, file.Path, budget); !errors.Is(e, store.ErrBotConflict) || backend.ran {
		t.Fatal("registry pin ignored", e)
	}
	if _, e = w.Execute(c, store.BotTaskMutation{BotID: id}, ref, file.Path, budget); !errors.Is(e, store.ErrBotForbidden) {
		t.Fatal("omitted lease", e)
	}
	budget.err = errors.New("budget exhausted")
	if _, e = w.Execute(c, m, ref, file.Path, budget); e == nil || backend.ran {
		t.Fatal("budget bypassed")
	}
	budget.err = nil
	backend.beforeWrite = func() { budget.err = errors.New("budget exhausted during staging") }
	if _, e = w.Execute(c, m, ref, file.Path, budget); e == nil || backend.ran {
		t.Fatal("late budget exhaustion bypassed", e)
	}
	budget.err = nil
	backend.beforeWrite = nil
	badLease := m
	badLease.Lease.WorkerID = "foreign-worker"
	if _, e = w.Execute(c, badLease, ref, file.Path, budget); !errors.Is(e, store.ErrBotConflict) || backend.ran {
		t.Fatal("foreign script lease", e)
	}
	w.Backend = backend.taskWorkspaceFake
	if _, e = w.Execute(c, m, ref, file.Path, budget); !errors.Is(e, store.ErrBotUnsupported) {
		t.Fatal("unsupported script backend", e)
	}
	w.Backend = backend
	cancelled, cancel := context.WithCancel(c)
	backend.beforeWrite = cancel
	if _, e = w.Execute(cancelled, m, ref, file.Path, budget); !errors.Is(e, context.Canceled) || backend.ran {
		t.Fatal("cancelled script committed", e)
	}
}

func TestBotTaskWorkspace(t *testing.T) {
	owner, id, run := uuid.New(), uuid.New(), uuid.New()
	c, _ := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	lease := store.BotLease{BotID: id, RunID: run, Fence: 1, WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute)}
	bots := homeManagerStore{b: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopeTeam, Home: store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: store.BotScopeTeam, Backend: "docker", StorageKey: "registered", Revision: 1}}, r: store.BotRun{ID: run, BotID: id, Status: store.RunRunning, Lease: lease}}
	backend := &taskWorkspaceFake{}
	w := &BotTaskWorkspace{Backend: backend, Bots: bots, commitBarrier: func(_ context.Context, _ store.BotTaskMutation, commit func() error) error { return commit() }}
	if _, e := w.binding(t.Context(), id); !errors.Is(e, store.ErrBotForbidden) {
		t.Fatal(e)
	}
	h, e := w.binding(c, id)
	if e != nil {
		t.Fatal(e)
	}
	m := store.BotTaskMutation{BotID: id, Lease: lease}
	if e = w.Write(c, m, sandbox.BotHomeWorkspaceFile{Path: "tasks/t/state/a", Data: []byte("state")}); e != nil || !backend.wrote {
		t.Fatal(e)
	}
	worker := execution.WithBotRunIdentity(c, execution.BotRunIdentity{BotID: id, RunID: run, Version: 1, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	backend.wrote = false
	if e = w.Write(worker, store.BotTaskMutation{BotID: id}, sandbox.BotHomeWorkspaceFile{}); !errors.Is(e, store.ErrBotForbidden) || backend.wrote {
		t.Fatalf("omitted worker lease: %v", e)
	}
	cancelled, cancel := context.WithCancel(worker)
	backend.beforeWrite = cancel
	if e = w.Write(cancelled, m, sandbox.BotHomeWorkspaceFile{}); !errors.Is(e, context.Canceled) || backend.wrote {
		t.Fatalf("late commit after cancellation: %v", e)
	}
	backend.beforeWrite = nil
	committed := false
	w.Tasks = taskWorkspaceRegistry{committed: &committed}
	q := store.BotTaskPrepareRequest{Mutation: m, Task: store.BotTask{ID: uuid.New(), BotID: id, HomePath: "tasks/test"}, Candidate: store.BotTaskRevision{ID: uuid.New()}}
	if _, e = w.Publish(worker, q); e != nil || !backend.published || !committed {
		t.Fatalf("publication barrier wiring: %v", e)
	}
	backend.published, committed = false, false
	cancelled, cancel = context.WithCancel(worker)
	backend.beforeWrite = cancel
	if _, e = w.Publish(cancelled, q); !errors.Is(e, context.Canceled) || backend.published || committed {
		t.Fatalf("publication committed after staging cancellation: %v", e)
	}
	backend.beforeWrite = nil
	m.Lease.Fence++
	if e = w.Write(c, m, sandbox.BotHomeWorkspaceFile{}); !errors.Is(e, store.ErrBotConflict) {
		t.Fatal(e)
	}
	data, e := w.Read(c, id, "tasks/t/state/checkpoint", 1024)
	if e != nil || string(data) != "retained" {
		t.Fatal(e)
	}
	task := uuid.New()
	file := sandbox.BotHomeWorkspaceFile{Path: "TASK.md", Data: []byte("definition")}
	p := store.BotTaskPublication{ID: uuid.New(), BotID: id, TaskID: task, Candidate: store.BotTaskRevision{Manifest: store.BotTaskManifest{Assets: []store.BotTaskAsset{{Path: file.Path, Digest: file.Digest(), SizeBytes: int64(len(file.Data))}}}}}
	backend.receipt = sandbox.BotHomeWorkspaceReceipt{ReceiptRef: "opaque", PublicationID: p.ID.String(), BindingKey: h.Scope.Key(), TaskPath: "tasks/" + task.String(), Entries: []sandbox.BotHomeWorkspaceEntry{{Path: file.Path, Digest: file.Digest(), SizeBytes: int64(len(file.Data))}}}
	if e = w.VerifyReceipt(c, p, "opaque"); e != nil {
		t.Fatal(e)
	}
	backend.receipt.Entries[0].Digest = "tampered"
	if e = w.VerifyReceipt(c, p, "opaque"); !errors.Is(e, store.ErrBotConflict) {
		t.Fatal(e)
	}
	ref := store.BotTaskRef{TaskID: task, Revision: 1, Digest: strings.Repeat("a", 64)}
	revision := store.BotTaskRevision{ID: uuid.New(), BotID: id, Ref: ref, Manifest: store.BotTaskManifest{Assets: p.Candidate.Manifest.Assets}, Bundle: nil}
	w.Tasks = taskWorkspaceRegistry{revision: revision}
	backend.snap.Files = []sandbox.BotHomeWorkspaceFile{file}
	if _, e = w.Load(c, id, ref); e != nil {
		t.Fatal(e)
	}
	// Fake canonical backend bytes remain "retained" (an uncommitted head),
	// while executable reads select the registry revision's verified bytes.
	data, e = w.Read(c, id, "tasks/"+task.String()+"/TASK.md", 1024)
	if e != nil || string(data) != "definition" {
		t.Fatalf("registry selected read %q %v", data, e)
	}
	if _, e = w.Read(c, id, "tasks/"+task.String()+"/TASK.md", 1); e == nil {
		t.Fatal("truncated selected read")
	}
	backend.snap.Files[0].Data = []byte("tampered")
	if _, e = w.Load(c, id, ref); !errors.Is(e, store.ErrBotConflict) {
		t.Fatal(e)
	}
	if e = (&BotTaskWorkspace{}).Install(); !errors.Is(e, store.ErrBotUnsupported) {
		t.Fatal(e)
	}
	if _, e = (&BotTaskWorkspace{}).Publish(c, store.BotTaskPrepareRequest{}); !errors.Is(e, store.ErrBotUnsupported) {
		t.Fatal(e)
	}
}
