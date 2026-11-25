package launcher

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

func TestBotTaskWorkspaceDockerIntegration(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_TASK_WORKSPACE") != "1" {
		t.Skip("explicit physical Docker acceptance required")
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo := scheduleFixture(t, scope)
			b, err := repo.CreateDraft(f.ctx, store.BotCreateRequest{Name: "Workspace integration", OwnerID: f.req.Bot.OwnerID, Version: f.req.Version, IdempotencyKey: "workspace-integration"})
			if err != nil {
				t.Fatal(err)
			}
			backend := newTaskWorkspaceDockerBackend(t)
			p := f.req.Principal
			h, err := backend.EnsureBotHome(f.ctx, sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: b.ID.String()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				c, done := context.WithTimeout(context.Background(), 20*time.Second)
				defer done()
				if e := backend.DeleteBotHome(c, h); e != nil {
					t.Error(e)
				}
			})
			service, err := execution.WithPrincipal(f.ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "owned-workspace-integration", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			reg := entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: string(h.Backend), StorageKey: h.Ref, Revision: 1}}
			if err = entstore.RegisterBotHomeBinding(service, repo, reg, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(f.ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			prepareTaskWorkspaceDockerSession(t, f.ctx, backend, h, b.HomeSessionID.String())
			tasks := repo.(store.BotTaskStore)
			w := &BotTaskWorkspace{Backend: backend, Bots: repo, Tasks: tasks}
			if err = w.Install(); err != nil {
				t.Fatal(err)
			}
			id := uuid.New()
			file := sandbox.BotHomeWorkspaceFile{Path: "TASK.md", Data: []byte("# Owned integration\n")}
			r := store.BotTaskRevision{SchemaVersion: 1, ID: uuid.New(), BotID: b.ID, Ref: store.BotTaskRef{TaskID: id, Revision: 1}, Manifest: store.BotTaskManifest{SchemaVersion: 1, BotID: b.ID, TaskID: id, Title: "Owned integration", Assignment: "Retain bytes", Assets: []store.BotTaskAsset{{Path: file.Path, Kind: store.BotTaskAssetDefinition, Digest: file.Digest(), SizeBytes: int64(len(file.Data))}}}, Bundle: map[string][]byte{file.Path: file.Data}}
			r.Ref.Digest = r.ContentDigest()
			q := store.BotTaskPrepareRequest{Mutation: store.BotTaskMutation{BotID: b.ID, ActorID: b.OwnerID, IdempotencyKey: "first", AuthorityConstraintRef: "owner-request"}, Task: store.BotTask{ID: id, BotID: b.ID, Key: id.String(), HomePath: "tasks/" + id.String(), Title: "Owned integration", Status: store.BotTaskReady}, Candidate: r}
			task, err := w.Publish(f.ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			for _, terminal := range []string{"held", "aborted"} {
				t.Run(terminal, func(t *testing.T) {
					q.Candidate = *q.Candidate.Clone()
					q.Candidate.ID = uuid.New()
					q.Candidate.Ref.Revision = task.Head.Revision + 1
					q.Candidate.Parent = task.Head.Clone()
					q.ExpectedHead = task.Head.Clone()
					q.Mutation.ExpectedRevision = task.Revision
					stranded := sandbox.BotHomeWorkspaceFile{Path: file.Path, Data: []byte("# Unselected " + terminal + "\n")}
					q.Candidate.Bundle[file.Path] = stranded.Data
					q.Candidate.Manifest.Assets[0].Digest = stranded.Digest()
					q.Candidate.Manifest.Assets[0].SizeBytes = int64(len(stranded.Data))
					q.Mutation.IdempotencyKey = "stranded-" + terminal
					q.Candidate.Ref.Digest = q.Candidate.ContentDigest()
					pending, e := tasks.PrepareTaskPublication(f.ctx, q)
					if e != nil {
						t.Fatal(e)
					}
					snap, e := backend.SnapshotBotHomeWorkspace(f.ctx, h, q.Task.HomePath, 1024)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = backend.PublishBotHomeWorkspace(f.ctx, h, sandbox.BotHomeWorkspacePublishRequest{PublicationID: pending.ID.String(), TaskPath: q.Task.HomePath, ExpectedDigest: snap.Receipt.Digest, Files: []sandbox.BotHomeWorkspaceFile{stranded}}); e != nil {
						t.Fatal(e)
					}
					recovery := repo.(store.BotTaskRecoveryStore)
					if terminal == "held" {
						e = recovery.HoldTaskPublications(f.ctx, b.ID, "uncertain")
					} else {
						e = recovery.AbortTaskPublication(f.ctx, q.Mutation, pending.ID, "uncertain")
					}
					if e != nil {
						t.Fatal(e)
					}
					selected, e := w.Read(f.ctx, b.ID, q.Task.HomePath+"/TASK.md", 1024)
					if e != nil || string(selected) != string(file.Data) {
						t.Fatalf("unselected physical head became authority: %q %v", selected, e)
					}
					q.Candidate.Bundle[file.Path] = file.Data
					q.Candidate.Manifest.Assets[0].Digest = file.Digest()
					q.Candidate.Manifest.Assets[0].SizeBytes = int64(len(file.Data))
					q.Candidate.ID = uuid.New()
					q.Mutation.IdempotencyKey = "authorized-" + terminal
					q.Candidate.Ref.Digest = q.Candidate.ContentDigest()
					next, e := w.Publish(f.ctx, q)
					if e != nil {
						t.Fatal(e)
					}
					if next.Head.Revision <= pending.Candidate.Ref.Revision {
						t.Fatalf("fresh publication %+v", next)
					}
					task = next
					data, e := w.Read(f.ctx, b.ID, q.Task.HomePath+"/TASK.md", 1024)
					if e != nil || string(data) != string(file.Data) {
						t.Fatalf("selected bytes %q %v", data, e)
					}
				})
			}
			t.Run("cancelled-write", func(t *testing.T) {
				file := sandbox.BotHomeWorkspaceFile{Path: q.Task.HomePath + "/state/checkpoint", Data: []byte("before")}
				if e := w.Write(f.ctx, q.Mutation, file); e != nil {
					t.Fatal(e)
				}
				gate := &launcherDockerStagingGate{BotHomeWorkspaceBackend: backend, staged: make(chan struct{}), release: make(chan struct{})}
				w.Backend = gate
				defer func() { w.Backend = backend }()
				c, cancel := context.WithCancel(f.ctx)
				defer cancel()
				file.Data = []byte("late")
				done := make(chan error, 1)
				go func() { done <- w.Write(c, q.Mutation, file) }()
				select {
				case <-gate.staged:
				case <-time.After(20 * time.Second):
					t.Fatal("physical staging not reached")
				}
				cancel()
				close(gate.release)
				select {
				case e := <-done:
					if e == nil {
						t.Fatal("late write succeeded")
					}
				case <-time.After(20 * time.Second):
					t.Fatal("helper not quiescent")
				}
				data, e := backend.ReadBotHomeWorkspace(f.ctx, h, file.Path, 1024)
				if e != nil || string(data) != "before" {
					t.Fatalf("late state %q %v", data, e)
				}
			})
		})
	}
}

// Physical fixtures select their recipe explicitly; Home registration is not
// launch authority, and file codecs must never invent or resume compute.
func newTaskWorkspaceDockerBackend(t *testing.T) *docker.DockerBackend {
	t.Helper()
	layers, image := os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS"), os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if !filepath.IsAbs(layers) || image == "" {
		t.Fatal("test-owned image and absolute retained layer path required")
	}
	// Ambient macOS TMPDIR is not Docker-visible. Own only this fresh root
	// beside the explicitly shared layers; never remove the retained layers.
	uppers, err := os.MkdirTemp(filepath.Dir(layers), "task-workspace-uppers-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(uppers); err != nil {
			t.Error(err)
		}
	})
	sessions, err := sandbox.NewLocalSessionStore(filepath.Join(t.TempDir(), "sandbox-registry"))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := docker.New(docker.Config{Sessions: sandbox.NewSessionRegistryFromStore(sessions), LayersDir: layers, UppersDir: uppers, SandboxImage: image, OverlayMode: "fuse"})
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func prepareTaskWorkspaceDockerSession(t *testing.T, ctx context.Context, backend *docker.DockerBackend, home *sandbox.BotHomeBinding, id string) {
	t.Helper()
	if err := backend.PrepareBotHomeSession(ctx, home, id); err == nil || !strings.Contains(err.Error(), "missing or mismatched authorized Bot session recipe") {
		t.Fatalf("unconfigured Home must fail closed: %v", err)
	}
	// This is fixture-authorized configuration, not a production default.
	// No Bot activation, model call, or overlay Python is required.
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := backend.DestroySession(c, id); err != nil {
			t.Error(err)
		}
	})
	if _, err := backend.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, Type: sandbox.SessionTypeChat, UserID: home.Scope.UserID, BotHome: home.Clone(), TemplateID: sandbox.BaseTemplateID, LayerChain: []string{sandbox.BaseTemplateID}}); err != nil {
		t.Fatal(err)
	}
	if err := backend.WaitForSessionReady(ctx, id); err != nil {
		t.Fatal(err)
	}
}

type launcherDockerStagingGate struct {
	sandbox.BotHomeWorkspaceBackend
	staged, release chan struct{}
}

func (g *launcherDockerStagingGate) WriteBotHomeWorkspace(c context.Context, h *sandbox.BotHomeBinding, f sandbox.BotHomeWorkspaceFile) error {
	c = sandbox.WithBotHomeWorkspaceCommitGuard(c, func(live context.Context) error { close(g.staged); <-g.release; return live.Err() })
	return g.BotHomeWorkspaceBackend.WriteBotHomeWorkspace(c, h, f)
}
