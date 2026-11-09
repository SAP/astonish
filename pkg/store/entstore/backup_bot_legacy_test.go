package entstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

// This creates a real pre-Bot archive, rather than merely changing its declared
// hash. The source has no Bot tables/columns; the target is a current deployment.
func TestRestorePlatformBackupSQLitePreBotArchive(t *testing.T) {
	ctx := context.Background()
	open := func(dir string) *Store {
		t.Helper()
		cfg := Config{DSN: "file:" + filepath.Join(dir, "platform.db"), DataDir: dir}
		if err := BootstrapPlatform(ctx, cfg, nil); err != nil {
			t.Fatal(err)
		}
		s, err := New(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	sourceDir := t.TempDir()
	source := open(sourceDir)
	owner, orgID := uuid.NewString(), uuid.NewString()
	if err := source.Users().Create(ctx, &store.User{ID: owner, Email: "legacy@example.test", DisplayName: "Legacy", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := source.Organizations().Create(ctx, &store.Organization{ID: orgID, Slug: "legacy", Name: "Legacy", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := source.Organizations().AddMember(ctx, owner, orgID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := source.ProvisionOrg(ctx, orgID, "legacy"); err != nil {
		t.Fatal(err)
	}
	org, err := source.ForOrg("legacy")
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "ops", Name: "Ops", SchemaName: "ops"}
	if err := org.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := org.ProvisionTeam(ctx, "ops"); err != nil {
		t.Fatal(err)
	}
	if err := org.ProvisionPersonalSchema(ctx, owner); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join("orgs", "legacy", "teams", "ops.db"), filepath.Join("orgs", "legacy", "personal", owner+".db")}
	for i, path := range paths {
		full := filepath.Join(sourceDir, path)
		if i == 0 {
			seedSessionBackupRows(t, full)
		}
		seedScheduledJob(t, full)
		db, err := sql.Open("sqlite", full)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if _, err := db.Exec(`INSERT INTO sessions (id,title,message_count,metadata,created_at,updated_at) VALUES ('backup-session-id','backup-session',1,'{}',datetime('now'),datetime('now'))`); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
		}
		statements := []string{
			"DROP TABLE bot_approvals", "DROP TABLE bot_runs", "DROP TABLE bot_tracked_items", "DROP TABLE bot_goals", "DROP TABLE bot_versions", "DROP TABLE bot_outbox", "DROP TABLE bots", "DROP TABLE bot_schema_version",
			"ALTER TABLE sessions DROP COLUMN bot_id", "ALTER TABLE sessions DROP COLUMN bot_run_id", "ALTER TABLE sessions DROP COLUMN purpose", "ALTER TABLE sessions DROP COLUMN retention",
		}
		if i == 0 {
			statements = append(statements, "DROP TABLE bot_templates", "ALTER TABLE sandbox_sessions DROP COLUMN bot_home_binding")
		}
		for _, stmt := range statements {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "pre-bot.astonish-backup")
	if err := source.ExportPlatformBackup(ctx, archive, PlatformBackupExportOptions{Backend: "sqlite"}); err != nil {
		t.Fatal(err)
	}
	summary, err := backup.Verify(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range summary.Manifest.Entries {
		if preBotAddedTable(entry.Entity, entry.Scope.Kind) {
			t.Fatalf("fixture is not pre-Bot: %s", entry.Entity)
		}
	}
	targetDir := t.TempDir()
	target := open(targetDir)
	if _, err := target.RestorePlatformBackup(ctx, archive, PlatformRestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	// Reopening routes through the regular additive migration path again.
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := open(targetDir)
	reopenedOrg, err := reopened.ForOrg("legacy")
	if err != nil {
		t.Fatal(err)
	}
	for _, sessions := range []store.SessionStore{reopenedOrg.ForTeam("ops").Sessions(), reopenedOrg.ForUser(owner).Sessions()} {
		meta, err := sessions.GetSessionMeta(ctx, "backup-session-id")
		if err != nil || meta == nil || meta.Title != "backup-session" || meta.HasBotLinkage() {
			t.Fatalf("legacy Chat changed on reopen: %+v %v", meta, err)
		}
	}
	for _, path := range paths {
		full := filepath.Join(targetDir, path)
		assertRestoredTableContains(t, full, "sessions", "title", "backup-session")
		assertRestoredTableContains(t, full, "sessions", "purpose", "chat")
		assertRestoredTableContains(t, full, "sessions", "retention", "standard")
		assertRestoredScheduledJobPaused(t, full)
		db, err := sql.Open("sqlite", full)
		if err != nil {
			t.Fatal(err)
		}
		var version, bots int
		if err := db.QueryRow(`SELECT version FROM bot_schema_version WHERE id='bots'`).Scan(&version); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM bots`).Scan(&bots); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if version != store.BotSchemaVersion || bots != 0 {
			t.Fatalf("upgrade state: version=%d bots=%d", version, bots)
		}
	}
}
