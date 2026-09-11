package entstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

func TestExportPlatformBackup(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dsn := "file:" + filepath.Join(dataDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: dsn, DataDir: dataDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform() error = %v", err)
	}
	es, err := New(ctx, Config{DSN: dsn, DataDir: dataDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer es.Close()

	userID := uuid.NewString()
	orgID := uuid.NewString()
	if err := es.Users().Create(ctx, &store.User{
		ID:           userID,
		Email:        "alice@example.com",
		DisplayName:  "Alice",
		PasswordHash: "super-secret-hash",
		Status:       "active",
	}); err != nil {
		t.Fatalf("Create user error = %v", err)
	}
	if err := es.Organizations().Create(ctx, &store.Organization{
		ID:     orgID,
		Name:   "Acme",
		Slug:   "acme",
		Status: "active",
	}); err != nil {
		t.Fatalf("Create org error = %v", err)
	}
	if err := es.Organizations().AddMember(ctx, userID, orgID, "owner"); err != nil {
		t.Fatalf("AddMember error = %v", err)
	}
	if err := es.ProvisionOrg(ctx, orgID, "acme"); err != nil {
		t.Fatalf("ProvisionOrg error = %v", err)
	}
	orgStore, err := es.ForOrg("acme")
	if err != nil {
		t.Fatalf("ForOrg error = %v", err)
	}
	team := &store.Team{ID: uuid.NewString(), Name: "SRE", Slug: "sre", SchemaName: "sre"}
	if err := orgStore.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatalf("CreateTeam error = %v", err)
	}
	if err := orgStore.ProvisionTeam(ctx, "sre"); err != nil {
		t.Fatalf("ProvisionTeam error = %v", err)
	}
	if err := orgStore.ProvisionPersonalSchema(ctx, userID); err != nil {
		t.Fatalf("ProvisionPersonalSchema error = %v", err)
	}
	seedTeamBackupRows(t, filepath.Join(dataDir, "orgs", "acme", "teams", "sre.db"))

	provider := &store.OIDCProvider{
		Name:         "Acme SSO",
		IssuerURL:    "https://issuer.example.com",
		ClientID:     "client-id",
		ClientSecret: "plain-secret",
		Scopes:       []string{"openid", "email"},
		Enabled:      true,
	}
	if err := es.OIDCProviders().Create(ctx, provider); err != nil {
		t.Fatalf("Create OIDC provider error = %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "platform.astonish-backup")
	if err := es.ExportPlatformBackup(ctx, archivePath, PlatformBackupExportOptions{Backend: "sqlite"}); err != nil {
		t.Fatalf("ExportPlatformBackup() error = %v", err)
	}

	summary, err := backup.Verify(archivePath)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if summary.Manifest.Mode != "logical" {
		t.Fatalf("mode = %q, want logical", summary.Manifest.Mode)
	}
	if summary.Manifest.Compression != string(backup.CompressionGzip) {
		t.Fatalf("compression = %q, want gzip", summary.Manifest.Compression)
	}
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(archiveBytes) < 2 || archiveBytes[0] != 0x1f || archiveBytes[1] != 0x8b {
		t.Fatal("archive is not gzip-compressed")
	}
	if len(summary.Manifest.Entries) < 4 {
		t.Fatalf("entries = %d, want at least 4", len(summary.Manifest.Entries))
	}

	files, err := backup.ReadArchiveFiles(archivePath)
	if err != nil {
		t.Fatalf("ReadArchiveFiles() error = %v", err)
	}
	users := string(files["platform/users.jsonl"])
	if !strings.Contains(users, "super-secret-hash") {
		t.Fatal("recovery backup does not contain password hash")
	}
	oidcProviders := string(files["platform/oidc_providers.jsonl"])
	if !strings.Contains(oidcProviders, "plain-secret") {
		t.Fatal("recovery backup does not contain OIDC client secret")
	}

	var record backup.Record
	firstLine := strings.Split(strings.TrimSpace(users), "\n")[0]
	if err := json.Unmarshal([]byte(firstLine), &record); err != nil {
		t.Fatalf("Unmarshal user record error = %v", err)
	}
	if record.Entity != "users" || record.ID != userID {
		t.Fatalf("record = %+v, want users/%s", record, userID)
	}
	assertArchiveContains(t, files, "orgs/acme/teams/sre/apps.jsonl", "backup-app")
	assertArchiveContains(t, files, "orgs/acme/teams/sre/flows.jsonl", "backup-flow")
	assertArchiveContains(t, files, "orgs/acme/teams/sre/fleet_templates.jsonl", "backup-template")
	assertArchiveContains(t, files, "orgs/acme/teams/sre/drill_reports.jsonl", "backup-drill")
}

func TestExportPlatformBackupRedactSecrets(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dsn := "file:" + filepath.Join(dataDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: dsn, DataDir: dataDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform() error = %v", err)
	}
	es, err := New(ctx, Config{DSN: dsn, DataDir: dataDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer es.Close()

	if err := es.Users().Create(ctx, &store.User{ID: uuid.NewString(), Email: "bob@example.com", DisplayName: "Bob", PasswordHash: "secret-hash", Status: "active"}); err != nil {
		t.Fatalf("Create user error = %v", err)
	}
	provider := &store.OIDCProvider{Name: "SSO", IssuerURL: "https://issuer.example.com", ClientID: "client-id", ClientSecret: "plain-secret", Enabled: true}
	if err := es.OIDCProviders().Create(ctx, provider); err != nil {
		t.Fatalf("Create OIDC provider error = %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "redacted.astonish-backup")
	if err := es.ExportPlatformBackup(ctx, archivePath, PlatformBackupExportOptions{Backend: "sqlite", RedactSecrets: true}); err != nil {
		t.Fatalf("ExportPlatformBackup() error = %v", err)
	}
	files, err := backup.ReadArchiveFiles(archivePath)
	if err != nil {
		t.Fatalf("ReadArchiveFiles() error = %v", err)
	}
	if strings.Contains(string(files["platform/users.jsonl"]), "secret-hash") {
		t.Fatal("redacted backup contains password hash")
	}
	if strings.Contains(string(files["platform/oidc_providers.jsonl"]), "plain-secret") {
		t.Fatal("redacted backup contains OIDC client secret")
	}
	if !strings.Contains(string(files["platform/oidc_providers.jsonl"]), "[REDACTED]") {
		t.Fatal("redacted backup does not mark OIDC client secret")
	}
}

func seedTeamBackupRows(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open team db error = %v", err)
	}
	defer db.Close()
	statements := []string{
		`INSERT INTO apps (id, slug, name, description, code, version, created_at, updated_at) VALUES ('` + uuid.NewString() + `', 'backup-app', 'Backup App', 'app for backup', '{"name":"Backup App"}', 1, datetime('now'), datetime('now'))`,
		`INSERT INTO flows (id, name, definition, yaml_content, type, created_at, updated_at) VALUES ('` + uuid.NewString() + `', 'backup-flow', '{"nodes":[]}', 'nodes: []', '', datetime('now'), datetime('now'))`,
		`INSERT INTO fleet_templates (id, key, name, definition, created_at, updated_at) VALUES ('` + uuid.NewString() + `', 'backup-template', 'Backup Template', '{"agents":[]}', datetime('now'), datetime('now'))`,
		`INSERT INTO drill_reports (id, suite, status, summary, duration_ms, report_data, started_at, finished_at, created_at) VALUES ('` + uuid.NewString() + `', 'backup-drill', 'passed', 'summary', 10, '{}', datetime('now'), datetime('now'), datetime('now'))`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed statement failed: %v\n%s", err, stmt)
		}
	}
}

func assertArchiveContains(t *testing.T, files map[string][]byte, path, want string) {
	t.Helper()
	data, ok := files[path]
	if !ok {
		t.Fatalf("archive missing %s", path)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("archive file %s does not contain %q; data: %s", path, want, string(data))
	}
}
