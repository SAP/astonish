//go:build integration

package entstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/pgutil"
)

func TestIntegrationPostgresQuerySurvivesResultTypeChange(t *testing.T) {
	ctx := context.Background()
	s := setupPostgresBackupRestoreStore(t, "result_type_change")

	conn, err := s.platformDB.Conn(ctx)
	if err != nil {
		t.Fatalf("platform DB connection error = %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE query_mode_regression (value integer NOT NULL)`); err != nil {
		t.Fatalf("create table error = %v", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO query_mode_regression (value) VALUES (42)`); err != nil {
		t.Fatalf("insert row error = %v", err)
	}

	var intValue int
	if err := conn.QueryRowContext(ctx, `SELECT value FROM query_mode_regression`).Scan(&intValue); err != nil {
		t.Fatalf("initial query error = %v", err)
	}
	if intValue != 42 {
		t.Fatalf("initial value = %d, want 42", intValue)
	}

	if _, err := conn.ExecContext(ctx, `ALTER TABLE query_mode_regression ALTER COLUMN value TYPE text USING value::text`); err != nil {
		t.Fatalf("alter result type error = %v", err)
	}

	var textValue string
	if err := conn.QueryRowContext(ctx, `SELECT value FROM query_mode_regression`).Scan(&textValue); err != nil {
		t.Fatalf("query after result type change error = %v", err)
	}
	if textValue != "42" {
		t.Fatalf("value after result type change = %q, want 42", textValue)
	}
}

func TestIntegrationPostgresBackupRestoreLogical(t *testing.T) {
	ctx := context.Background()
	source := setupPostgresBackupRestoreStore(t, "source")
	userID := uuid.NewString()
	orgID := uuid.NewString()
	if err := source.Users().Create(ctx, &store.User{ID: userID, Email: "pg-restore@example.com", DisplayName: "PG Restore", PasswordHash: "hash", Status: "active"}); err != nil {
		t.Fatalf("Create user error = %v", err)
	}
	if err := source.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Postgres Restore", Slug: "restorepg", Status: "active"}); err != nil {
		t.Fatalf("Create org error = %v", err)
	}
	if err := source.Organizations().AddMember(ctx, userID, orgID, "owner"); err != nil {
		t.Fatalf("AddMember error = %v", err)
	}
	if err := source.ProvisionOrg(ctx, orgID, "restorepg"); err != nil {
		t.Fatalf("ProvisionOrg error = %v", err)
	}
	orgStore, err := source.ForOrg("restorepg")
	if err != nil {
		t.Fatalf("ForOrg error = %v", err)
	}
	team := &store.Team{ID: uuid.NewString(), Name: "Ops", Slug: "ops", SchemaName: teamSchemaName("ops")}
	if err := orgStore.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatalf("CreateTeam error = %v", err)
	}
	if err := orgStore.ProvisionTeam(ctx, "ops"); err != nil {
		t.Fatalf("ProvisionTeam error = %v", err)
	}
	orgDB, err := source.openRestorePostgresDB("restorepg", "")
	if err != nil {
		t.Fatalf("open source org DB error = %v", err)
	}
	seedPostgresTeamBackupRows(t, orgDB, teamSchemaName("ops"))
	if err := orgDB.Close(); err != nil {
		t.Fatalf("close source org DB error = %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "postgres.astonish-backup")
	if err := source.ExportPlatformBackup(ctx, archivePath, PlatformBackupExportOptions{Backend: "postgres"}); err != nil {
		t.Fatalf("ExportPlatformBackup() error = %v", err)
	}

	target := setupPostgresBackupRestoreStore(t, "target")
	result, err := target.RestorePlatformBackup(ctx, archivePath, PlatformRestoreOptions{})
	if err != nil {
		t.Fatalf("RestorePlatformBackup() error = %v", err)
	}
	if result.RestoredRecords == 0 {
		t.Fatal("RestoredRecords = 0, want records")
	}
	assertPostgresTableContains(t, target.platformDB, "public", "users", "email", "pg-restore@example.com")
	targetOrgDB, err := target.openRestorePostgresDB("restorepg", "")
	if err != nil {
		t.Fatalf("open target org DB error = %v", err)
	}
	defer targetOrgDB.Close()
	assertPostgresTableContains(t, targetOrgDB, teamSchemaName("ops"), "apps", "slug", "backup-app")
	assertPostgresTableContains(t, targetOrgDB, teamSchemaName("ops"), "flows", "name", "backup-flow")
}

func setupPostgresBackupRestoreStore(t *testing.T, name string) *Store {
	t.Helper()
	baseDSN := os.Getenv("ASTONISH_TEST_DSN")
	if baseDSN == "" {
		t.Skip("ASTONISH_TEST_DSN not set; skipping PostgreSQL backup/restore integration test")
	}
	ctx := context.Background()
	suffix := fmt.Sprintf("backup_%s_%s", sanitizePostgresBackupTestName(name), strings.ReplaceAll(uuid.NewString()[:8], "-", "_"))
	platformDBName := fmt.Sprintf("astonish_%s_platform", suffix)
	platformDSN, err := pgutil.ReplaceDSNDatabase(baseDSN, platformDBName)
	if err != nil {
		t.Fatalf("ReplaceDSNDatabase error = %v", err)
	}
	dropPostgresBackupTestDB(t, baseDSN, platformDBName)
	t.Cleanup(func() { dropPostgresBackupTestDB(t, baseDSN, platformDBName) })
	for _, orgSlug := range []string{"restorepg"} {
		orgDBName := orgDBNameForSuffix(suffix, orgSlug)
		dropPostgresBackupTestDB(t, baseDSN, orgDBName)
		t.Cleanup(func() { dropPostgresBackupTestDB(t, baseDSN, orgDBName) })
	}

	cfg := Config{DSN: platformDSN, InstanceSuffix: suffix}
	if err := BootstrapPlatform(ctx, cfg, nil); err != nil {
		t.Fatalf("BootstrapPlatform error = %v", err)
	}
	_, es, err := NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatalf("NewPlatformServices error = %v", err)
	}
	t.Cleanup(func() { es.Close() })
	return es
}

func dropPostgresBackupTestDB(t *testing.T, baseDSN, dbName string) {
	t.Helper()
	adminDSN, err := pgutil.ReplaceDSNDatabase(baseDSN, "postgres")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return
	}
	defer conn.Close(ctx)
	_, _ = conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, dbName)
	query := "DROP DATABASE IF EXISTS " + pgx.Identifier{dbName}.Sanitize()
	_, _ = conn.Exec(ctx, query) //nolint:gosec // dbName is generated by this test and quoted as a PostgreSQL identifier.
}

func orgDBNameForSuffix(suffix, orgSlug string) string {
	return fmt.Sprintf("astonish_%s_%s", suffix, sanitizeSlug(orgSlug))
}

func sanitizePostgresBackupTestName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 24 {
		return out[:24]
	}
	return out
}

func seedPostgresTeamBackupRows(t *testing.T, db *sql.DB, schema string) {
	t.Helper()
	statements := []struct {
		table string
		cols  string
		args  []any
	}{
		{table: "apps", cols: "id, slug, name, description, code, version, created_at, updated_at", args: []any{uuid.NewString(), "backup-app", "Backup App", "app for backup", `{"name":"Backup App"}`, 1}},
		{table: "flows", cols: "id, name, definition, yaml_content, type, created_at, updated_at", args: []any{uuid.NewString(), "backup-flow", `{"nodes":[]}`, "nodes: []", ""}},
	}
	for _, stmt := range statements {
		query := fmt.Sprintf("INSERT INTO %s.%s (%s) VALUES ($1, $2, $3, $4, $5, $6, now(), now())", quoteIdent(DialectPostgres, schema), quoteIdent(DialectPostgres, stmt.table), stmt.cols) //nolint:gosec // schema and table names are generated/hardcoded and quoted; values are parameters.
		if _, err := db.Exec(query, stmt.args...); err != nil {
			t.Fatalf("seed %s failed: %v", stmt.table, err)
		}
	}
}

func assertPostgresTableContains(t *testing.T, db *sql.DB, schema, table, column, want string) {
	t.Helper()
	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s.%s WHERE %s = $1", quoteIdent(DialectPostgres, schema), quoteIdent(DialectPostgres, table), quoteIdent(DialectPostgres, column)) //nolint:gosec // identifiers are test constants and quoted; value is parameterized.
	if err := db.QueryRow(query, want).Scan(&count); err != nil {
		t.Fatalf("query %s.%s error = %v", table, column, err)
	}
	if count != 1 {
		t.Fatalf("%s.%s=%q count = %d, want 1", table, column, want, count)
	}
}
