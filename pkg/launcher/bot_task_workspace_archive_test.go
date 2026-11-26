package launcher

import (
	"bytes"
	"context"
	"database/sql"
	"io"
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
			backend := newTaskWorkspaceDockerBackend(t)
			must(source.InstallBotContentHomeBackend(backend))
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
			b, err := repo.Create(ctx, store.BotCreateRequest{Name: "Archive task", OwnerID: uuid.MustParse(p.Subject), Version: f.req.Version, IdempotencyKey: "archive"})
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
			b, err = repo.Get(ctx, b.ID)
			must(err)
			// This is supported first-time Home selection for a file-first draft,
			// not legacy adoption or archive migration.
			must(entstore.SelectBotHomeConfiguration(ctx, repo, *home, backend))
			prepareTaskWorkspaceDockerSession(t, ctx, backend, home, b.HomeSessionID.String())
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
			// Quiesce through public lifecycle APIs; pending candidates remain
			// held historical bytes and never advance the committed task head.
			must(repo.(store.BotTaskRecoveryStore).HoldTaskPublications(ctx, b.ID, "offline archive"))
			b, err = repo.Get(ctx, b.ID)
			must(err)
			// Fresh offline fixture state, matching the public CLI archive fixture.
			// No activation/readiness override or Bot execution is involved.
			sourcePath := filepath.Join(dir, "orgs", p.OrgSlug, "teams", p.TeamSlug+".db")
			if scope == store.BotScopePersonal {
				sourcePath = filepath.Join(dir, "orgs", p.OrgSlug, "personal", p.Subject+".db")
			}
			fixtureSQL, err := sql.Open("sqlite", sourcePath)
			must(err)
			_, err = fixtureSQL.ExecContext(ctx, "UPDATE bots SET status='paused' WHERE id=? AND status='draft'", b.ID.String())
			must(err)
			must(fixtureSQL.Close())
			b, err = repo.Get(ctx, b.ID)
			must(err)
			before, err := backend.SnapshotBotHomeWorkspace(ctx, home, "workspace", sandbox.MaxBotHomeWorkspaceBundleBytes)
			must(err)
			// Stop configured compute before the file-first exporter acquires
			// its own SQL/attachment fences; export must not resume it.
			must(backend.StopSession(ctx, b.HomeSessionID.String()))
			archivePath := filepath.Join(t.TempDir(), "workspace.astonish-backup")
			options := entstore.PlatformBackupExportOptions{OrgSlug: p.OrgSlug, FileBackend: func(context.Context) (entstore.BotBackupBackend, error) { return backend, nil }}
			must(source.ExportPlatformBackup(ctx, archivePath, options))
			_, err = entstore.VerifyPlatformBackup(ctx, archivePath, backup.ReaderOptions{})
			must(err)
			targetDir := t.TempDir()
			_, target, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(targetDir, "platform.db"), DataDir: targetDir})
			must(err)
			defer target.Close()
			targetBackend := newTaskWorkspaceDockerBackend(t)
			must(target.InstallBotContentHomeBackend(targetBackend))
			ordering := &taskWorkspaceArchiveRestoreBackend{DockerBackend: targetBackend, beforeSQL: func() {
				inspect, err := sql.Open("sqlite", filepath.Join(targetDir, "platform.db"))
				must(err)
				defer inspect.Close()
				var selected int
				must(inspect.QueryRowContext(ctx, "SELECT COUNT(*) FROM organizations").Scan(&selected))
				if selected != 0 {
					t.Fatal("target SQL selected before Home file installation")
				}
			}}
			restoreOptions := entstore.PlatformRestoreOptions{MapOrg: map[string]string{p.OrgSlug: "archive-target"}, FileBackend: func(context.Context) (entstore.BotRestoreBackend, error) { return ordering, nil }}
			_, err = target.RestorePlatformBackup(ctx, archivePath, restoreOptions)
			must(err)
			if ordering.installed != 1 {
				t.Fatalf("expected one physical Home installation before SQL, got %d", ordering.installed)
			}
			if _, err := target.RestorePlatformBackup(ctx, archivePath, restoreOptions); err == nil {
				t.Fatal("duplicate/nonempty restore accepted")
			}
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
			if recovered.Home.StorageKey == home.Ref || recovered.Home.ID != b.Home.ID || recovered.Home.Revision != b.Home.Revision+1 || recovered.Status != store.BotPaused {
				t.Fatal("file-first restore reused source storage, lost identity or resumed execution")
			}
			destination := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: b.ID.String()}, Backend: sandbox.BackendKind(recovered.Home.Backend), Ref: recovered.Home.StorageKey}
			t.Cleanup(func() {
				c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := targetBackend.DeleteBotHome(c, destination); err != nil {
					t.Error(err)
				}
			})
			// Recovery already installed files before selecting SQL. Configure fresh
			// disposable target compute only for workspace receipt/read verification.
			prepareTaskWorkspaceDockerSession(t, targetCtx, targetBackend, destination, uuid.NewString())
			foreign := *destination
			foreign.Scope.TeamID = "foreign-team"
			if _, err := targetBackend.SnapshotBotHomeWorkspace(targetCtx, &foreign, "workspace", sandbox.MaxBotHomeWorkspaceBundleBytes); err == nil {
				t.Fatal("foreign Home binding accepted")
			}
			if _, err := targetBackend.SnapshotBotHomeWorkspace(targetCtx, home, "workspace", sandbox.MaxBotHomeWorkspaceBundleBytes); err == nil {
				t.Fatal("source Home accepted as target")
			}
			after, err := targetBackend.SnapshotBotHomeWorkspace(targetCtx, destination, "workspace", sandbox.MaxBotHomeWorkspaceBundleBytes)
			must(err)
			content := map[string][]byte{}
			for _, file := range after.Files {
				content[file.Path] = file.Data
			}
			for _, file := range before.Files {
				if _, ok := content[file.Path]; !ok {
					t.Fatalf("physical restore lost path %s", file.Path)
				}
				// Mapping scrubs execution authority in heads/receipts; both remain
				// in the inventory, but must not be asserted as fresh permission.
				if strings.HasPrefix(file.Path, ".receipts/") || strings.HasSuffix(file.Path, "/.head") {
					continue
				}
				if !bytes.Equal(file.Data, content[file.Path]) {
					t.Fatalf("physical restore mismatch %s", file.Path)
				}
			}
			// File-first recovery adds exactly one private install-completion marker.
			if len(after.Files) != len(before.Files)+1 || string(content[".archive-state"]) != "restoring\nverified\n" || !bytes.Equal(content[q.Task.HomePath+"/state/state.sqlite"], state) {
				t.Fatalf("physical SQLite or inventory lost: before=%d after=%d marker=%q", len(before.Files), len(after.Files), content[".archive-state"])
			}
			restoredTask, err := targetRepo.(store.BotTaskStore).GetTask(targetCtx, b.ID, taskID)
			must(err)
			if restoredTask.Head == nil || restoredTask.Head.TaskID != task.Head.TaskID || restoredTask.Head.Revision != task.Head.Revision || restoredTask.Head.Revision == pending.Candidate.Ref.Revision {
				t.Fatal("restore advanced or lost committed task head")
			}
			// Scope mapping deliberately keeps old receipts historical. They cannot
			// authorize an ordinary workspace read in the restored scope before
			// explicit reconciliation; immutable definition bytes remain exact.
			if _, err := targetBackend.ReadBotHomeWorkspace(targetCtx, destination, q.Task.HomePath+"/TASK.md", 1024); err == nil || !strings.Contains(err.Error(), "workspace receipt identity") {
				t.Fatalf("historical receipt unexpectedly authorized restored read: %v", err)
			}
			definitionCount := 0
			for _, file := range after.Files {
				if strings.HasPrefix(file.Path, q.Task.HomePath+"/.revisions/") && strings.HasSuffix(file.Path, "/TASK.md") {
					definitionCount++
					if !bytes.Equal(file.Data, definition.Data) {
						t.Fatal("restored immutable definition bytes changed")
					}
				}
			}
			if definitionCount != 1 {
				t.Fatalf("expected exactly one committed definition, got %d", definitionCount)
			}
			must(backend.StartSession(ctx, b.HomeSessionID.String()))
			must(backend.WaitForSessionReady(ctx, b.HomeSessionID.String()))
			sourceAfter, err := backend.SnapshotBotHomeWorkspace(ctx, home, "workspace", sandbox.MaxBotHomeWorkspaceBundleBytes)
			must(err)
			if sourceAfter.Receipt.Digest != before.Receipt.Digest {
				t.Fatal("target restore modified independently owned source Home")
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

// Observe the public importer's physical installation without changing its
// journaling, ownership checks, barriers, or SQL publication path.
type taskWorkspaceArchiveRestoreBackend struct {
	*docker.DockerBackend
	beforeSQL func()
	installed int
}

func (b *taskWorkspaceArchiveRestoreBackend) RestoreBotHomeArchive(ctx context.Context, home *sandbox.BotHomeBinding, manifest *sandbox.BotHomeArchiveManifest, r io.Reader, barrier sandbox.BotHomeArchiveBarrier) error {
	if err := b.DockerBackend.RestoreBotHomeArchive(ctx, home, manifest, r, barrier); err != nil {
		return err
	}
	b.beforeSQL()
	b.installed++
	return nil
}
