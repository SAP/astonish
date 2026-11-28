//go:build integration

package entstore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

// These acceptance tests intentionally fail without a disposable admin DSN.
// The existing harness generates and cleans up all platform/org databases.
func preBotPostgresFixture(t *testing.T) (*Store, string, []backup.Scope, backup.Manifest) {
	t.Helper()
	if os.Getenv("ASTONISH_TEST_DSN") == "" || os.Getenv("ASTONISH_BOTS_TEST_DB_ISOLATED") != "1" {
		t.Fatal("ASTONISH_TEST_DSN and ASTONISH_BOTS_TEST_DB_ISOLATED=1 required: use isolated disposable PostgreSQL, never production")
	}
	ctx := context.Background()
	s := setupPostgresBackupRestoreStore(t, "prebot_source")
	owner, orgID := uuid.NewString(), uuid.NewString()
	if err := s.Users().Create(ctx, &store.User{ID: owner, Email: "prebot@example.test", DisplayName: "Legacy", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Organizations().Create(ctx, &store.Organization{ID: orgID, Slug: "restorepg", Name: "Legacy", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Organizations().AddMember(ctx, owner, orgID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.ProvisionOrg(ctx, orgID, "restorepg"); err != nil {
		t.Fatal(err)
	}
	org, err := s.ForOrg("restorepg")
	if err != nil {
		t.Fatal(err)
	}
	if err := org.Teams().CreateTeam(ctx, &store.Team{ID: uuid.NewString(), Slug: "ops", Name: "Ops", SchemaName: teamSchemaName("ops")}); err != nil {
		t.Fatal(err)
	}
	if err := org.ProvisionTeam(ctx, "ops"); err != nil {
		t.Fatal(err)
	}
	if err := org.ProvisionPersonalSchema(ctx, owner); err != nil {
		t.Fatal(err)
	}
	for _, sessions := range []store.SessionStore{org.ForTeam("ops").Sessions(), org.ForUser(owner).Sessions()} {
		if err := sessions.AddSessionMeta(ctx, store.SessionMeta{ID: "legacy-chat", Title: "Keep this conversation", AppName: "astonish"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, jobs := range []store.SchedulerStore{org.ForTeam("ops").ScheduledJobs(), org.ForUser(owner).ScheduledJobs()} {
		if err := jobs.Add(ctx, &store.ScheduledJob{ID: uuid.NewString(), Name: "legacy-adaptive", Mode: "adaptive", Enabled: true, Schedule: store.JobSchedule{Cron: "0 9 * * *"}, Payload: store.JobPayload{Instructions: "Keep this job"}}); err != nil {
			t.Fatal(err)
		}
	}
	scopes := []backup.Scope{{Kind: "team", OrgSlug: "restorepg", TeamSlug: "ops"}, {Kind: "personal", OrgSlug: "restorepg", UserID: owner}}
	db, err := s.openRestorePostgresDB("restorepg", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manifest := backup.Manifest{SchemaVersions: map[string]backup.SchemaVersion{}}
	for _, scope := range scopes {
		schema := quoteIdent(DialectPostgres, postgresSchemaForScope(scope)) + "."
		// Reconstruct an actual immediate pre-Bot database, not just its hash.
		for _, table := range []string{"bot_skill_revisions", "bot_skills", "bot_approvals", "bot_runs", "bot_tracked_items", "bot_goals", "bot_versions", "bot_outbox", "bots", "bot_schema_version"} {
			execPreBotPostgres(t, db, "DROP TABLE "+schema+quoteIdent(DialectPostgres, table))
		}
		for _, column := range []string{"bot_id", "bot_run_id", "purpose", "retention"} {
			execPreBotPostgres(t, db, "ALTER TABLE "+schema+"sessions DROP COLUMN "+quoteIdent(DialectPostgres, column))
		}
		if scope.Kind == "team" {
			execPreBotPostgres(t, db, "DROP TABLE "+schema+"bot_templates")
			execPreBotPostgres(t, db, "ALTER TABLE "+schema+"sandbox_sessions DROP COLUMN bot_home_binding")
		}
		version, err := schemaVersionForScope(ctx, "postgres", db, scope)
		if err != nil {
			t.Fatal(err)
		}
		manifest.SchemaVersions[backupScopeKey(scope)] = version
	}
	return s, owner, scopes, manifest
}

func execPreBotPostgres(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), statement); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

func reopenPreBotPostgres(t *testing.T, s *Store) *Store {
	t.Helper()
	cfg := Config{DSN: s.platformDSN, InstanceSuffix: s.instanceSuffix}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened
}

func assertPreBotPostgresRows(t *testing.T, s *Store, owner string, scopes []backup.Scope, enabled bool) {
	t.Helper()
	ctx := context.Background()
	org, err := s.ForOrg("restorepg")
	if err != nil {
		t.Fatal(err)
	}
	// Access through the regular router triggers scoped reconciliation on reopen.
	for _, sessions := range []store.SessionStore{org.ForTeam("ops").Sessions(), org.ForUser(owner).Sessions()} {
		meta, err := sessions.GetSessionMeta(ctx, "legacy-chat")
		if err != nil || meta == nil || meta.Title != "Keep this conversation" || meta.HasBotLinkage() {
			t.Fatalf("legacy Chat: %+v %v", meta, err)
		}
	}
	for _, jobs := range []store.SchedulerStore{org.ForTeam("ops").ScheduledJobs(), org.ForUser(owner).ScheduledJobs()} {
		job := jobs.GetByName(ctx, "legacy-adaptive")
		if job == nil || job.Mode != "adaptive" || job.Payload.Instructions != "Keep this job" || job.Schedule.Cron != "0 9 * * *" || job.Enabled != enabled {
			t.Fatalf("legacy job: %+v; want enabled=%v", job, enabled)
		}
	}
	db, err := s.openRestorePostgresDB("restorepg", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, scope := range scopes {
		schema := postgresSchemaForScope(scope)
		assertPostgresTableContains(t, db, schema, "sessions", "purpose", "chat")
		assertPostgresTableContains(t, db, schema, "sessions", "retention", "standard")
		var version, bots int
		if err := db.QueryRowContext(ctx, "SELECT version FROM "+quoteIdent(DialectPostgres, schema)+".bot_schema_version WHERE id='bots'").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(DialectPostgres, schema)+".bots").Scan(&bots); err != nil {
			t.Fatal(err)
		}
		if version != botMigrationVersion || bots != 0 {
			t.Fatalf("%s: version=%d bots=%d", scope.Kind, version, bots)
		}
	}
}

func TestIntegrationBotPostgresPreBotMigration(t *testing.T) {
	ctx := context.Background()
	s, owner, scopes, manifest := preBotPostgresFixture(t)
	for i := 0; i < 2; i++ {
		s = reopenPreBotPostgres(t, s)
		assertPreBotPostgresRows(t, s, owner, scopes, true)
	}
	db, err := s.openRestorePostgresDB("restorepg", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, scope := range scopes {
		t.Run(scope.Kind, func(t *testing.T) {
			legacy := manifest.SchemaVersions[backupScopeKey(scope)]
			target, err := schemaVersionForScope(ctx, "postgres", db, scope)
			if err != nil {
				t.Fatal(err)
			}
			if checkSchemaCompatible(legacy, target) == nil {
				t.Fatal("global hash policy was weakened")
			}
			if err := checkPostgresScopeSchemaCompatible(ctx, manifest, db, scope); err != nil {
				t.Fatalf("immediate pre-Bot rejected: %v", err)
			}
			for _, entity := range []string{"bots", "bot_schema_version", "bot_versions", "bot_goals", "bot_tracked_items", "bot_runs", "bot_approvals", "bot_outbox", "bot_templates"} {
				if !preBotAddedTable(entity, scope.Kind) {
					continue
				}
				bad := manifest
				bad.Entries = []backup.Entry{{Scope: scope, Entity: entity}}
				if checkPostgresScopeSchemaCompatible(ctx, bad, db, scope) == nil {
					t.Fatalf("Bot entry %s accepted as legacy", entity)
				}
			}
			for _, version := range []string{"postgres:future", "sqlite:" + backup.ArchiveFormat, ""} {
				bad := legacy
				bad.Version = version
				ok, err := postgresPreBotBackupCompatible(ctx, db, manifest, scope, bad)
				if err != nil || ok {
					t.Fatalf("invalid version accepted: %q %v", version, err)
				}
			}
			bad := legacy
			bad.Hash = ""
			if ok, err := postgresPreBotBackupCompatible(ctx, db, manifest, scope, bad); err != nil || ok {
				t.Fatalf("empty legacy hash: %v %v", ok, err)
			}
			schema := quoteIdent(DialectPostgres, postgresSchemaForScope(scope)) + "."
			for _, drift := range []struct{ add, remove string }{
				{"ALTER TABLE " + schema + "sessions ADD COLUMN unrelated TEXT", "ALTER TABLE " + schema + "sessions DROP COLUMN unrelated"},
				{"CREATE TABLE " + schema + "unrelated (id TEXT)", "DROP TABLE " + schema + "unrelated"},
				{"ALTER TABLE " + schema + "sessions RENAME COLUMN title TO unrelated", "ALTER TABLE " + schema + "sessions RENAME COLUMN unrelated TO title"},
			} {
				execPreBotPostgres(t, db, drift.add)
				if checkPostgresScopeSchemaCompatible(ctx, manifest, db, scope) == nil {
					t.Fatalf("unrelated drift accepted: %s", drift.add)
				}
				execPreBotPostgres(t, db, drift.remove)
			}
			if err := checkPostgresScopeSchemaCompatible(ctx, manifest, db, scope); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, kind := range []string{"platform", "org"} {
		if ok, err := postgresPreBotBackupCompatible(ctx, db, manifest, backup.Scope{Kind: kind}, manifest.SchemaVersions[backupScopeKey(scopes[0])]); err != nil || ok {
			t.Fatalf("unsupported scope %s accepted: %v", kind, err)
		}
	}
}

func TestIntegrationBotPostgresPreBotBackupRestore(t *testing.T) {
	ctx := context.Background()
	source, owner, scopes, legacyManifest := preBotPostgresFixture(t)
	archive := filepath.Join(t.TempDir(), "pre-bot.astonish-backup")
	if err := source.ExportPlatformBackup(ctx, archive, PlatformBackupExportOptions{Backend: "postgres"}); err != nil {
		t.Fatal(err)
	}
	summary, err := backup.Verify(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range summary.Manifest.Entries {
		if preBotAddedTable(entry.Entity, entry.Scope.Kind) {
			t.Fatalf("fixture not pre-Bot: %s", entry.Entity)
		}
	}
	for _, scope := range scopes {
		key := backupScopeKey(scope)
		if summary.Manifest.SchemaVersions[key].Hash != legacyManifest.SchemaVersions[key].Hash || summary.Manifest.SchemaVersions[key].Version != legacyManifest.SchemaVersions[key].Version {
			t.Fatalf("export changed legacy schema hash for %s", key)
		}
	}
	target := setupPostgresBackupRestoreStore(t, "prebot_target")
	result, err := target.RestorePlatformBackup(ctx, archive, PlatformRestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RestoredRecords == 0 {
		t.Fatal("no records restored")
	}
	for i := 0; i < 2; i++ {
		target = reopenPreBotPostgres(t, target)
		assertPreBotPostgresRows(t, target, owner, scopes, false)
	}
}
