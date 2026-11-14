package launcher

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

func TestBotTaskWorkspaceArchiveDocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_TASK_WORKSPACE") != "1" {
		t.Skip("explicit owned Docker archive acceptance required")
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			p := f.req.Principal
			ctx := f.ctx
			dir := t.TempDir()
			_, source, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			orgID := uuid.NewString()
			teamID := uuid.NewString()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(source.Users().Create(ctx, &store.User{ID: p.Subject, Email: "archive@example.test", DisplayName: "Archive owner", Status: "active"}))
			must(source.Organizations().Create(ctx, &store.Organization{ID: orgID, Slug: p.OrgSlug, Name: "Archive", Status: "active"}))
			must(source.Organizations().AddMember(ctx, p.Subject, orgID, "owner"))
			must(source.ProvisionOrg(ctx, orgID, p.OrgSlug))
			org, err := source.ForOrg(p.OrgSlug)
			must(err)
			must(org.Teams().CreateTeam(ctx, &store.Team{ID: teamID, Slug: p.TeamSlug, Name: "Archive", SchemaName: "team_archive"}))
			must(org.Teams().AddMember(ctx, &store.TeamMembership{UserID: p.Subject, TeamID: teamID, Role: "admin"}))
			services, err := BotChatServicesForOwner(org, p, scope)
			must(err)
			repo := services.Bots
			if scope == store.BotScopePersonal {
				repo = services.PersonalBots
			}
			b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Archive task", OwnerID: uuid.MustParse(p.Subject), Version: f.req.Version, IdempotencyKey: "archive"})
			must(err)
			backend, err := docker.New(docker.Config{Sessions: sandbox.NewSessionRegistryFromStore(nil), LayersDir: filepath.Join(dir, "layers"), UppersDir: filepath.Join(dir, "uppers"), SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE")})
			must(err)
			home, err := backend.EnsureBotHome(ctx, sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: b.ID.String()})
			must(err)
			cleanup := func(h *sandbox.BotHomeBinding) {
				t.Cleanup(func() {
					c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := backend.DeleteBotHome(c, h); err != nil {
						t.Error(err)
					}
				})
			}
			cleanup(home)
			service, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "owned-archive", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			must(err)
			must(entstore.RegisterBotHomeBinding(service, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: string(home.Backend), StorageKey: home.Ref, Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }))
			tasks := repo.(store.BotTaskStore)
			w := &BotTaskWorkspace{Backend: backend, Bots: repo, Tasks: tasks}
			must(w.Install())
			taskID := uuid.New()
			definition := sandbox.BotHomeWorkspaceFile{Path: "TASK.md", Data: []byte("# Archive\nRetain checkpoint and sources.\n")}
			r := store.BotTaskRevision{SchemaVersion: 1, ID: uuid.New(), BotID: b.ID, Ref: store.BotTaskRef{TaskID: taskID, Revision: 1}, Manifest: store.BotTaskManifest{SchemaVersion: 1, BotID: b.ID, TaskID: taskID, Title: "Archive", Assignment: "Retain checkpoint", Assets: []store.BotTaskAsset{{Path: "TASK.md", Kind: store.BotTaskAssetDefinition, Digest: definition.Digest(), SizeBytes: int64(len(definition.Data))}}}, Bundle: map[string][]byte{"TASK.md": definition.Data}}
			r.Ref.Digest = r.ContentDigest()
			q := store.BotTaskPrepareRequest{Mutation: store.BotTaskMutation{BotID: b.ID, ActorID: b.OwnerID, IdempotencyKey: "publication", AuthorityConstraintRef: "owner-request"}, Task: store.BotTask{ID: taskID, BotID: b.ID, Key: taskID.String(), HomePath: "tasks/" + taskID.String(), Title: "Archive", Status: store.BotTaskReady}, Candidate: r}
			task, err := w.Publish(ctx, q)
			must(err)
			statePath := filepath.Join(t.TempDir(), "state.sqlite")
			stateDB, err := sql.Open("sqlite", statePath)
			must(err)
			_, err = stateDB.Exec("CREATE TABLE observations(value TEXT); INSERT INTO observations VALUES ('retained-nonempty')")
			must(err)
			must(stateDB.Close())
			state, err := os.ReadFile(statePath)
			must(err)
			must(w.Write(ctx, q.Mutation, sandbox.BotHomeWorkspaceFile{Path: q.Task.HomePath + "/state/state.sqlite", Data: state}))
			must(w.Write(ctx, q.Mutation, sandbox.BotHomeWorkspaceFile{Path: q.Task.HomePath + "/outputs/sources.json", Data: []byte("{\"source\":\"https://example.test/decisive\"}")}))
			// Prepared candidate must restore held; its archival bytes never become head.
			pending := q
			pending.Candidate = *r.Clone()
			pending.Candidate.ID = uuid.New()
			pending.Candidate.Ref.Revision = 2
			pending.Candidate.Parent = task.Head.Clone()
			pending.Candidate.Ref.Digest = pending.Candidate.ContentDigest()
			pending.ExpectedHead = task.Head.Clone()
			pending.Mutation.ExpectedRevision = task.Revision
			pending.Mutation.IdempotencyKey = "pending"
			publication, err := tasks.PrepareTaskPublication(ctx, pending)
			must(err)
			_, err = repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Unprovisioned", OwnerID: uuid.MustParse(p.Subject), Version: f.req.Version, IdempotencyKey: "no-home"})
			must(err)
			archivePath := filepath.Join(t.TempDir(), "workspace.astonish-backup")
			options := entstore.PlatformBackupExportOptions{OrgSlug: p.OrgSlug, WorkspaceBackend: backend, WorkspaceBinding: func(_ backup.Scope, id store.BotID, h store.BotHomeBinding) (*sandbox.BotHomeBinding, error) {
				return &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: id.String()}, Backend: sandbox.BackendKind(h.Backend), Ref: h.StorageKey}, nil
			}, WorkspaceFence: func(c context.Context, s backup.Scope, fn func() error) error {
				var bots store.BotStore
				if s.Kind == "personal" {
					bots = services.PersonalBots
				} else {
					bots = services.Bots
				}
				return entstore.WithBotWorkspaceArchiveFence(c, bots, fn)
			}}
			must(source.ExportPlatformBackup(ctx, archivePath, options))
			sourceScope := backup.Scope{Kind: "team", OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
			if scope == store.BotScopePersonal {
				sourceScope = backup.Scope{Kind: "personal", OrgSlug: p.OrgSlug, UserID: p.Subject}
			}
			archived, err := entstore.ReadBotWorkspaceArchive(archivePath, backup.ReaderOptions{}, sourceScope, b.ID)
			must(err)
			before, err := backend.SnapshotBotHomeWorkspace(ctx, home, "workspace", sandbox.MaxBotHomeWorkspaceBundleBytes)
			must(err)
			if before.Receipt.Digest != archived.Snapshot.Receipt.Digest {
				t.Fatal("archive differed from fenced physical snapshot")
			}
			targetDir := t.TempDir()
			_, target, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(targetDir, "platform.db"), DataDir: targetDir})
			must(err)
			defer target.Close()
			_, err = target.RestorePlatformBackup(ctx, archivePath, entstore.PlatformRestoreOptions{MapOrg: map[string]string{p.OrgSlug: "archive-target"}})
			must(err)
			p.OrgSlug = "archive-target"
			targetCtx, err := execution.WithPrincipal(context.Background(), p)
			must(err)
			targetOrg, err := target.ForOrg(p.OrgSlug)
			must(err)
			targetServices, err := BotChatServicesForOwner(targetOrg, p, scope)
			must(err)
			targetRepo := targetServices.Bots
			if scope == store.BotScopePersonal {
				targetRepo = targetServices.PersonalBots
			}
			recovered, err := targetRepo.Get(targetCtx, b.ID)
			must(err)
			if recovered.Home.StorageKey != home.Ref {
				t.Fatal("logical restore changed Home before explicit setup")
			}
			destination, err := backend.EnsureBotHome(targetCtx, sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: b.ID.String()})
			must(err)
			cleanup(destination)
			foreign := *destination
			foreign.Scope.TeamID = "foreign-team"
			if err := entstore.ReconcileRestoredBotWorkspaceHome(targetCtx, targetRepo, backend, archived, &foreign); err == nil {
				t.Fatal("foreign Home binding accepted")
			}
			if err := entstore.ReconcileRestoredBotWorkspaceHome(targetCtx, targetRepo, backend, archived, home); err == nil {
				t.Fatal("source Home accepted as target")
			}
			must(entstore.ReconcileRestoredBotWorkspaceHome(targetCtx, targetRepo, backend, archived, destination))
			if err := entstore.ReconcileRestoredBotWorkspaceHome(targetCtx, targetRepo, backend, archived, destination); err == nil {
				t.Fatal("duplicate/nonempty restore accepted")
			}
			after, err := backend.SnapshotBotHomeWorkspace(targetCtx, destination, "workspace", sandbox.MaxBotHomeWorkspaceBundleBytes)
			must(err)
			content := map[string][]byte{}
			for _, file := range after.Files {
				content[file.Path] = file.Data
			}
			for _, file := range before.Files {
				if strings.HasPrefix(file.Path, ".receipts/") || strings.HasSuffix(file.Path, "/.head") {
					continue
				}
				if !bytes.Equal(file.Data, content[file.Path]) {
					t.Fatalf("physical restore mismatch %s", file.Path)
				}
			}
			if len(after.Files) != len(before.Files) || !bytes.Equal(content[q.Task.HomePath+"/state/state.sqlite"], state) {
				t.Fatal("physical SQLite or inventory lost")
			}
			readback, err := backend.ReadBotHomeWorkspace(targetCtx, destination, q.Task.HomePath+"/TASK.md", 1024)
			must(err)
			if !bytes.Equal(readback, definition.Data) {
				t.Fatal("rebound receipt did not resolve immutable definition")
			}
			path := filepath.Join(targetDir, "orgs", p.OrgSlug, "teams", p.TeamSlug+".db")
			if scope == store.BotScopePersonal {
				path = filepath.Join(targetDir, "orgs", p.OrgSlug, "personal", p.Subject+".db")
			}
			inspect, err := sql.Open("sqlite", path)
			must(err)
			defer inspect.Close()
			var restoredState string
			must(inspect.QueryRow("SELECT publication_state FROM bot_task_revisions WHERE id=?", publication.ID.String()).Scan(&restoredState))
			if restoredState != string(store.BotTaskPublicationHeld) {
				t.Fatal("restore pending publication not held")
			}

		})
	}
}
