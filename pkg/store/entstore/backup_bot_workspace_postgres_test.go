//go:build integration

package entstore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// The generated PostgreSQL databases and Docker volumes belong only to this test.
func TestBotTaskWorkspaceArchivePostgresDocker(t *testing.T) {
	requireTaskPostgres(t)
	if os.Getenv("ASTONISH_TEST_TASK_WORKSPACE") != "1" {
		t.Fatal("owned Docker workspace gate required")
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newPostgresBotServicesFixture(t, scope, 100, 100)
			must := func(e error) {
				t.Helper()
				if e != nil {
					t.Fatal(e)
				}
			}
			owner, _ := execution.PrincipalFromContext(f.owner)
			v := store.DefaultBotVersion()
			v.Charter = "Retain archive checkpoint"
			b, e := f.bots.CreateDraft(f.owner, store.BotCreateRequest{Name: "Archive workspace", OwnerID: uuid.MustParse(owner.Subject), Version: v, IdempotencyKey: "archive-draft"})
			must(e)
			backend, e := docker.New(docker.Config{Sessions: sandbox.NewSessionRegistryFromStore(nil), LayersDir: filepath.Join(t.TempDir(), "layers"), UppersDir: filepath.Join(t.TempDir(), "uppers"), SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE")})
			must(e)
			ensure := func(s sandbox.BotHomeScope) *sandbox.BotHomeBinding {
				t.Helper()
				h, e := backend.EnsureBotHome(f.owner, s)
				must(e)
				t.Cleanup(func() {
					c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if e := backend.DeleteBotHome(c, h); e != nil {
						t.Error(e)
					}
				})
				return h
			}
			home := ensure(sandbox.BotHomeScope{OrgID: owner.OrgSlug, TeamID: owner.TeamSlug, UserID: owner.Subject, BotID: b.ID.String()})
			service, e := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "owned-archive", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: owner.OrgSlug, TeamSlug: owner.TeamSlug, Authenticated: true, Scopes: []string{BotHomeRegistrationScope}})
			must(e)
			must(RegisterBotHomeBinding(service, f.bots, BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: string(home.Backend), StorageKey: home.Ref, Revision: 1}}, func(context.Context, BotHomeRegistration) error { return nil }))
			var r *botRepository
			switch s := f.bots.(type) {
			case *personalBotStore:
				r = s.taskRepository()
			case *teamBotStore:
				r = s.taskRepository()
			}
			q := taskTestCandidate(b.ID)
			q.Mutation = store.BotTaskMutation{BotID: b.ID, ActorID: b.OwnerID, IdempotencyKey: "archive-task", AuthorityConstraintRef: "owner-request"}
			publication, e := r.PrepareTaskPublication(f.owner, q)
			must(e)
			var receipt *sandbox.BotHomeWorkspaceReceipt
			must(WithBotTaskWorkspaceCommitGuard(f.owner, f.bots, q.Mutation, func(check func(context.Context) error) error {
				c := sandbox.WithBotHomeWorkspaceCommitGuard(f.owner, check)
				files := []sandbox.BotHomeWorkspaceFile{}
				for path, data := range q.Candidate.Bundle {
					files = append(files, sandbox.BotHomeWorkspaceFile{Path: path, Data: data})
				}
				var err error
				receipt, err = backend.PublishBotHomeWorkspace(c, home, sandbox.BotHomeWorkspacePublishRequest{PublicationID: publication.ID.String(), TaskPath: q.Task.HomePath, Files: files})
				return err
			}))
			r.taskVerifier = func(c context.Context, p store.BotTaskPublication, ref string) error {
				_, err := backend.ResolveBotHomeWorkspaceReceipt(c, home, ref)
				return err
			}
			task, e := r.CommitTaskPublication(f.owner, store.BotTaskCommitRequest{Mutation: q.Mutation, PublicationID: publication.ID, WorkspaceReceiptRef: receipt.ReceiptRef})
			must(e)
			must(WithBotTaskWorkspaceCommitGuard(f.owner, f.bots, q.Mutation, func(check func(context.Context) error) error {
				return backend.WriteBotHomeWorkspace(sandbox.WithBotHomeWorkspaceCommitGuard(f.owner, check), home, sandbox.BotHomeWorkspaceFile{Path: q.Task.HomePath + "/state/checkpoint.json", Data: []byte(`{"observations":["nonempty-postgres"]}`)})
			}))
			archivePath := filepath.Join(t.TempDir(), "workspace.astonish-backup")
			org, e := f.platform.ForOrg(owner.OrgSlug)
			must(e)
			must(f.platform.ExportPlatformBackup(f.owner, archivePath, PlatformBackupExportOptions{WorkspaceBackend: backend, WorkspaceBinding: func(_ backup.Scope, id store.BotID, h store.BotHomeBinding) (*sandbox.BotHomeBinding, error) {
				return &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: owner.OrgSlug, TeamID: owner.TeamSlug, UserID: owner.Subject, BotID: id.String()}, Backend: sandbox.BackendKind(h.Backend), Ref: h.StorageKey}, nil
			}, WorkspaceFence: func(c context.Context, s backup.Scope, fn func() error) error {
				bots := org.ForUser(owner.Subject).Bots()
				if s.Kind == "team" {
					bots = org.ForTeam(owner.TeamSlug).Bots()
				}
				return WithBotWorkspaceArchiveFence(c, bots, fn)
			}}))
			sourceScope := backup.Scope{Kind: "team", OrgSlug: owner.OrgSlug, TeamSlug: owner.TeamSlug}
			if scope == store.BotScopePersonal {
				sourceScope = backup.Scope{Kind: "personal", OrgSlug: owner.OrgSlug, UserID: owner.Subject}
			}
			a, e := ReadBotWorkspaceArchive(archivePath, backup.ReaderOptions{}, sourceScope, b.ID)
			must(e)
			target := setupPostgresBackupRestoreStore(t, "task_archive_target")
			t.Cleanup(func() {
				dropPostgresBackupTestDB(t, os.Getenv("ASTONISH_TEST_DSN"), orgDBNameForSuffix(target.instanceSuffix, "archivepg"))
			})
			_, e = target.RestorePlatformBackup(f.owner, archivePath, PlatformRestoreOptions{MapOrg: map[string]string{owner.OrgSlug: "archivepg"}})
			must(e)
			owner.OrgSlug = "archivepg"
			targetCtx, e := execution.WithPrincipal(context.Background(), owner)
			must(e)
			targetOrg, e := target.ForOrg(owner.OrgSlug)
			must(e)
			bots := targetOrg.ForUser(owner.Subject).Bots()
			if scope == store.BotScopeTeam {
				bots = targetOrg.ForTeam(owner.TeamSlug).Bots()
			}
			// An independently generated volume is explicitly provisioned and still carries
			// the exact destination scope. SQL import alone retains the source opaque key.
			destinationScope := home.Scope
			destinationScope.OrgID = owner.OrgSlug
			destination := ensure(destinationScope)
			if destination.Ref == home.Ref {
				t.Fatal("backend returned source volume for new provisioning")
			}
			must(ReconcileRestoredBotWorkspaceHome(targetCtx, bots, backend, a, destination))
			after, e := backend.SnapshotBotHomeWorkspace(f.owner, destination, "workspace", sandbox.MaxBotHomeWorkspaceBundleBytes)
			must(e)
			content := map[string][]byte{}
			for _, f := range after.Files {
				content[f.Path] = f.Data
			}
			for _, f := range a.Snapshot.Files {
				if f.Path == q.Task.HomePath+"/.head" || len(f.Path) >= 10 && f.Path[:10] == ".receipts/" {
					continue
				}
				if !bytes.Equal(content[f.Path], f.Data) {
					t.Fatalf("physical PostgreSQL archive mismatch %s", f.Path)
				}
			}
			if len(after.Files) != len(a.Snapshot.Files) {
				t.Fatal("physical inventory lost")
			}
			restored, e := bots.(store.BotTaskStore).GetTask(targetCtx, b.ID, task.ID)
			must(e)
			if restored.Head == nil || *restored.Head != *task.Head {
				t.Fatal("PostgreSQL registry head lost")
			}
			target = reopenPreBotPostgres(t, target)
		})
	}
}
