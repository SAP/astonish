package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

// Production HTTP creates owned Bots; scoped Ent stores publish only after actual
// retained Docker bytes resolve. No provider calls or configured daemon are used.
func TestBotTaskWorkspaceHTTPAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_TASK_WORKSPACE") != "1" {
		t.Skip("explicit physical Docker acceptance required")
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newSkillStorageHTTPFixture(t)
			raw := f.request("POST", "/api/studio/bots?scope="+string(scope), f.owner, `{"name":"Workspace storage","idempotency_key":"workspace"}`, 201)
			value := skillDecode[store.Bot](t, raw)
			b := &value
			repo := f.repo(scope)
			ctx := f.context(f.owner)
			dir := t.TempDir()
			localSessions, e := sandbox.NewLocalSessionStore(filepath.Join(dir, "sandbox-registry"))
			if e != nil {
				t.Fatal(e)
			}
			sessions := sandbox.NewSessionRegistryFromStore(localSessions)
			layers := os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS")
			if !filepath.IsAbs(layers) || os.Getenv("ASTONISH_SANDBOX_IMAGE") == "" {
				t.Fatal("explicit test image and absolute retained layer directory required")
			}
			uppers, e := os.MkdirTemp(filepath.Dir(layers), "api-task-workspace-uppers-")
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(uppers); err != nil {
					t.Error(err)
				}
			})
			backend, e := docker.New(docker.Config{Sessions: sessions, LayersDir: layers, UppersDir: uppers, SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE"), OverlayMode: "fuse"})
			if e != nil {
				t.Fatal(e)
			}
			h, e := backend.EnsureBotHome(ctx, sandbox.BotHomeScope{OrgID: f.org.Slug, TeamID: f.team.Slug, UserID: f.owner, BotID: b.ID.String()})
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				c, done := context.WithTimeout(context.Background(), 20*time.Second)
				defer done()
				if e := backend.DeleteBotHome(c, h); e != nil {
					t.Error(e)
				}
			})
			service, e := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "owned-storage-registration", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: f.org.Slug, TeamSlug: f.team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if e != nil {
				t.Fatal(e)
			}
			reg := entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: string(h.Backend), StorageKey: h.Ref, Revision: 1}}
			if e = entstore.RegisterBotHomeBinding(service, repo, reg, func(context.Context, entstore.BotHomeRegistration) error { return nil }); e != nil {
				t.Fatal(e)
			}
			b, e = repo.Get(ctx, b.ID)
			if e != nil {
				t.Fatal(e)
			}
			if b.HomeSessionID == uuid.Nil {
				t.Fatal("registered Home has no canonical session")
			}
			sessionID := b.HomeSessionID.String()
			if err := backend.PrepareBotHomeSession(ctx, h, sessionID); err == nil || !strings.Contains(err.Error(), "missing or mismatched authorized Bot session recipe") {
				t.Fatalf("unconfigured Home must fail closed: %v", err)
			}
			t.Cleanup(func() {
				c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := backend.DestroySession(c, sessionID); err != nil {
					t.Error(err)
				}
			})
			// Fixture-authorized template setup precedes file transactions; reads
			// and publication cannot manufacture a session or an authority grant.
			if _, err := backend.CreateSession(ctx, sandbox.SessionSpec{SessionID: sessionID, Type: sandbox.SessionTypeChat, UserID: f.owner, BotHome: h.Clone(), TemplateID: sandbox.BaseTemplateID, LayerChain: []string{sandbox.BaseTemplateID}}); err != nil {
				t.Fatal(err)
			}
			if err := backend.WaitForSessionReady(ctx, sessionID); err != nil {
				t.Fatal(err)
			}
			b, e = repo.Get(ctx, b.ID)
			if e != nil {
				t.Fatal(e)
			}
			run, e := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Retain task", IdempotencyKey: uuid.NewString()})
			if e != nil {
				t.Fatal(e)
			}
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: run.Version, ActorID: f.owner, OrgSlug: f.org.Slug, TeamSlug: f.team.Slug})
			lease, e := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "workspace-test", ExpiresAt: time.Now().Add(time.Minute)})
			if e != nil {
				t.Fatal(e)
			}
			tasks := repo.(store.BotTaskStore)
			id := uuid.New()
			file := sandbox.BotHomeWorkspaceFile{Path: "TASK.md", Data: []byte("# Retained task\nInspect SQLite state\n")}
			revision := store.BotTaskRevision{SchemaVersion: 1, ID: uuid.New(), BotID: b.ID, Ref: store.BotTaskRef{TaskID: id, Revision: 1}, Manifest: store.BotTaskManifest{SchemaVersion: 1, BotID: b.ID, TaskID: id, Title: "Retained task", Assignment: "Inspect SQLite state", Assets: []store.BotTaskAsset{{Path: file.Path, Kind: store.BotTaskAssetDefinition, Digest: file.Digest(), SizeBytes: int64(len(file.Data))}}}, Bundle: map[string][]byte{file.Path: file.Data}}
			revision.Ref.Digest = revision.ContentDigest()
			q := store.BotTaskPrepareRequest{Mutation: store.BotTaskMutation{BotID: b.ID, ActorID: b.OwnerID, Lease: lease, IdempotencyKey: "publish", AuthorityConstraintRef: "original-owner-request"}, Task: store.BotTask{ID: id, BotID: b.ID, Key: id.String(), HomePath: "tasks/" + id.String(), Title: "Retained task", Status: store.BotTaskReady}, Candidate: revision}
			p, e := tasks.PrepareTaskPublication(worker, q)
			if e != nil {
				t.Fatal(e)
			}
			receipt, e := backend.PublishBotHomeWorkspace(worker, h, sandbox.BotHomeWorkspacePublishRequest{PublicationID: p.ID.String(), TaskPath: q.Task.HomePath, Files: []sandbox.BotHomeWorkspaceFile{file}})
			if e != nil {
				t.Fatal(e)
			}
			if e = entstore.InstallBotTaskWorkspaceVerifier(repo, func(c context.Context, p store.BotTaskPublication, ref string) error {
				r, e := backend.ResolveBotHomeWorkspaceReceipt(c, h, ref)
				if e != nil {
					return e
				}
				if r.PublicationID != p.ID.String() || r.TaskPath != q.Task.HomePath || len(r.Entries) != 1 || r.Entries[0].Digest != p.Candidate.Manifest.Assets[0].Digest {
					return store.ErrBotConflict
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			task, e := tasks.CommitTaskPublication(worker, store.BotTaskCommitRequest{Mutation: q.Mutation, PublicationID: p.ID, WorkspaceReceiptRef: receipt.ReceiptRef})
			if e != nil || task.Head == nil {
				t.Fatalf("commit %+v %v", task, e)
			}
			got, e := backend.ReadBotHomeWorkspace(ctx, h, q.Task.HomePath+"/TASK.md", 1024)
			if e != nil || string(got) != string(file.Data) {
				t.Fatalf("physical bytes %v", e)
			}
			read := skillDecode[store.Bot](t, f.request("GET", skillPath(b, ""), f.owner, "", 200))
			if read.Version != b.Version {
				t.Fatal("publication advanced Bot definition")
			}
			// Simulate a crash after file publication but before registry selection.
			next := q
			next.Candidate = *revision.Clone()
			next.Candidate.ID = uuid.New()
			next.Candidate.Parent = revision.Ref.Clone()
			next.Candidate.Ref.Revision++
			next.Candidate.Bundle["TASK.md"] = []byte("# Uncommitted replacement\n")
			changedFile := sandbox.BotHomeWorkspaceFile{Path: "TASK.md", Data: next.Candidate.Bundle["TASK.md"]}
			next.Candidate.Manifest.Assets[0].Digest = changedFile.Digest()
			next.Candidate.Manifest.Assets[0].SizeBytes = int64(len(changedFile.Data))
			next.Candidate.Ref.Digest = next.Candidate.ContentDigest()
			next.ExpectedHead = revision.Ref.Clone()
			next.Mutation.ExpectedRevision = task.Revision
			next.Mutation.IdempotencyKey = "uncommitted"
			pending, e := tasks.PrepareTaskPublication(worker, next)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = backend.PublishBotHomeWorkspace(worker, h, sandbox.BotHomeWorkspacePublishRequest{PublicationID: pending.ID.String(), TaskPath: q.Task.HomePath, ExpectedDigest: receipt.Digest, Files: []sandbox.BotHomeWorkspaceFile{changedFile}}); e != nil {
				t.Fatal(e)
			}
			selectedRevision, e := tasks.GetTaskRevision(ctx, b.ID, *task.Head)
			if e != nil {
				t.Fatal(e)
			}
			selected, e := backend.ReadBotHomeWorkspace(ctx, h, q.Task.HomePath+"/.revisions/"+selectedRevision.ID.String()+"/TASK.md", 1024)
			if e != nil || string(selected) != string(file.Data) {
				t.Fatalf("uncommitted head became authority: %q %v", selected, e)
			}
			workspace := &workspaceAcceptanceService{Backend: backend, Bots: repo, Tasks: tasks}
			if e = workspace.Install(); e != nil {
				t.Fatal(e)
			}
			// Each stranded physical head must permit a fresh authorized publication
			// of the same Task, without selecting or reusing the uncertain revision.
			for _, terminal := range []string{"held", "aborted"} {
				t.Run(terminal, func(t *testing.T) {
					if terminal == "held" {
						if e = tasks.(store.BotTaskRecoveryStore).HoldTaskPublications(ctx, b.ID, "uncertain physical publication"); e != nil {
							t.Fatal(e)
						}
					} else {
						if e = tasks.(store.BotTaskRecoveryStore).AbortTaskPublication(worker, next.Mutation, pending.ID, "owner replaced uncertain candidate"); e != nil {
							t.Fatal(e)
						}
					}
					fresh := next
					fresh.Candidate = *next.Candidate.Clone()
					fresh.Candidate.ID = uuid.New()
					fresh.Mutation.IdempotencyKey = "fresh-" + terminal
					fresh.ExpectedHead = task.Head.Clone()
					fresh.Candidate.Parent = task.Head.Clone()
					fresh.Mutation.ExpectedRevision = task.Revision
					fresh.Candidate.Ref.Digest = fresh.Candidate.ContentDigest()
					previous := pending.Candidate.Ref.Revision
					task, e = workspace.Publish(worker, fresh)
					if e != nil || task.Head == nil || task.Head.Revision <= previous {
						t.Fatalf("fresh same Task: %+v %v", task, e)
					}
					got, e = workspace.Read(ctx, b.ID, q.Task.HomePath+"/TASK.md", 1024)
					if e != nil || string(got) != string(changedFile.Data) {
						t.Fatalf("selected physical read %q %v", got, e)
					}
					// Publish another physical-only candidate for the next transition.
					next = fresh
					next.Candidate = *fresh.Candidate.Clone()
					next.Candidate.ID = uuid.New()
					next.Candidate.Ref = *task.Head.Clone()
					next.Candidate.Ref.Revision++
					next.ExpectedHead = task.Head.Clone()
					next.Candidate.Parent = task.Head.Clone()
					next.Candidate.Ref.Digest = next.Candidate.ContentDigest()
					next.Mutation.ExpectedRevision = task.Revision
					next.Mutation.IdempotencyKey = "stranded-" + terminal
					pending, e = tasks.PrepareTaskPublication(worker, next)
					if e != nil {
						t.Fatal(e)
					}
					snap, err := backend.SnapshotBotHomeWorkspace(ctx, h, q.Task.HomePath, 1024)
					if err != nil {
						t.Fatal(err)
					}
					if _, e = backend.PublishBotHomeWorkspace(worker, h, sandbox.BotHomeWorkspacePublishRequest{PublicationID: pending.ID.String(), TaskPath: q.Task.HomePath, ExpectedDigest: snap.Receipt.Digest, Files: []sandbox.BotHomeWorkspaceFile{changedFile}}); e != nil {
						t.Fatal(e)
					}
				})
			}
			t.Run("cancelled-write", func(t *testing.T) {
				state := sandbox.BotHomeWorkspaceFile{Path: q.Task.HomePath + "/state/cancel.txt", Data: []byte("before")}
				if e := workspace.Write(worker, q.Mutation, state); e != nil {
					t.Fatal(e)
				}
				staged, release := make(chan struct{}), make(chan struct{})
				cancelled, cancel := context.WithCancel(worker)
				defer cancel()
				workspace.Backend = &workspaceStagingGate{BotHomeWorkspaceBackend: backend, staged: staged, release: release}
				defer func() { workspace.Backend = backend }()
				state.Data = []byte("late")
				done := make(chan error, 1)
				go func() { done <- workspace.Write(cancelled, q.Mutation, state) }()
				select {
				case <-staged:
				case <-time.After(20 * time.Second):
					t.Fatal("actual Docker staging not reached")
				}
				cancel()
				close(release)
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("cancelled Docker write succeeded")
					}
				case <-time.After(20 * time.Second):
					t.Fatal("cancelled Docker helper did not quiesce")
				}
				data, err := backend.ReadBotHomeWorkspace(ctx, h, state.Path, 1024)
				if err != nil || string(data) != "before" {
					t.Fatalf("late physical state %q %v", data, err)
				}
			})
			foreign := *h
			foreign.Scope.UserID = f.member
			if _, e = backend.ReadBotHomeWorkspace(f.context(f.member), &foreign, q.Task.HomePath+"/TASK.md", 1024); e == nil {
				t.Fatal("foreign owner read physical workspace")
			}
			f.reopen()
			retained, e := f.repo(scope).(store.BotTaskStore).GetTaskRevision(ctx, b.ID, revision.Ref)
			if e != nil || retained.Ref != revision.Ref {
				t.Fatalf("reopened registry %v", e)
			}
		})
	}
}

// Independent acceptance composition avoids api -> launcher -> api test cycles.
type workspaceAcceptanceService struct {
	Backend sandbox.BotHomeWorkspaceBackend
	Bots    store.BotStore
	Tasks   store.BotTaskStore
}

func (w *workspaceAcceptanceService) home(c context.Context, b store.BotID) (*sandbox.BotHomeBinding, error) {
	v, err := w.Bots.Get(c, b)
	if err != nil {
		return nil, err
	}
	p, _ := execution.PrincipalFromContext(c)
	return &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: b.String()}, Backend: sandbox.BackendKind(v.Home.Backend), Ref: v.Home.StorageKey}, nil
}
func (w *workspaceAcceptanceService) Install() error { return nil }
func (w *workspaceAcceptanceService) Publish(c context.Context, q store.BotTaskPrepareRequest) (*store.BotTask, error) {
	p, err := w.Tasks.PrepareTaskPublication(c, q)
	if err != nil {
		return nil, err
	}
	h, err := w.home(c, q.Task.BotID)
	if err != nil {
		return nil, err
	}
	snap, err := w.Backend.SnapshotBotHomeWorkspace(c, h, q.Task.HomePath, 1024)
	if err != nil {
		return nil, err
	}
	var receipt *sandbox.BotHomeWorkspaceReceipt
	err = entstore.WithBotTaskWorkspaceCommit(c, w.Bots, q.Mutation, func() error {
		var e error
		receipt, e = w.Backend.PublishBotHomeWorkspace(c, h, sandbox.BotHomeWorkspacePublishRequest{PublicationID: p.ID.String(), TaskPath: q.Task.HomePath, ExpectedDigest: snap.Receipt.Digest, Files: []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: p.Candidate.Bundle["TASK.md"]}}})
		return e
	})
	if err != nil {
		return nil, err
	}
	return w.Tasks.CommitTaskPublication(c, store.BotTaskCommitRequest{Mutation: q.Mutation, PublicationID: p.ID, WorkspaceReceiptRef: receipt.ReceiptRef})
}
func (w *workspaceAcceptanceService) Read(c context.Context, b store.BotID, path string, n int64) ([]byte, error) {
	h, err := w.home(c, b)
	if err != nil {
		return nil, err
	}
	id, _ := uuid.Parse(strings.Split(path, "/")[1])
	task, err := w.Tasks.GetTask(c, b, id)
	if err != nil {
		return nil, err
	}
	r, err := w.Tasks.GetTaskRevision(c, b, *task.Head)
	if err != nil {
		return nil, err
	}
	return w.Backend.ReadBotHomeWorkspace(c, h, task.HomePath+"/.revisions/"+r.ID.String()+"/TASK.md", n)
}
func (w *workspaceAcceptanceService) Write(c context.Context, m store.BotTaskMutation, f sandbox.BotHomeWorkspaceFile) error {
	h, err := w.home(c, m.BotID)
	if err != nil {
		return err
	}
	return entstore.WithBotTaskWorkspaceCommit(c, w.Bots, m, func() error { return w.Backend.WriteBotHomeWorkspace(c, h, f) })
}

// Gate the real helper's post-fsync handshake, never a host filesystem fixture.
type workspaceStagingGate struct {
	sandbox.BotHomeWorkspaceBackend
	staged  chan struct{}
	release chan struct{}
}

func (g *workspaceStagingGate) WriteBotHomeWorkspace(c context.Context, h *sandbox.BotHomeBinding, f sandbox.BotHomeWorkspaceFile) error {
	guarded := sandbox.WithBotHomeWorkspaceCommitGuard(c, func(live context.Context) error {
		close(g.staged)
		<-g.release
		return live.Err()
	})
	return g.BotHomeWorkspaceBackend.WriteBotHomeWorkspace(guarded, h, f)
}
