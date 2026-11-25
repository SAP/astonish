package astonish

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

func TestParseBackupReadArgs(t *testing.T) {
	path, jsonOut, passphrase, err := parseBackupReadArgs([]string{"backup.astonish-backup", "--json", "--passphrase", "secret"})
	if err != nil {
		t.Fatalf("parseBackupReadArgs() error = %v", err)
	}
	if path != "backup.astonish-backup" {
		t.Fatalf("path = %q, want backup.astonish-backup", path)
	}
	if !jsonOut {
		t.Fatal("jsonOut = false, want true")
	}
	if passphrase != "secret" {
		t.Fatalf("passphrase = %q, want secret", passphrase)
	}
}

func TestParseBackupReadArgsRequiresArchive(t *testing.T) {
	if _, _, _, err := parseBackupReadArgs([]string{"--json"}); err == nil {
		t.Fatal("parseBackupReadArgs() error = nil, want missing archive error")
	}
}

func TestParseBackupCreateArgs(t *testing.T) {
	opts, err := parseBackupCreateArgs([]string{"--output", "backup.astonish-backup"})
	if err != nil {
		t.Fatalf("parseBackupCreateArgs() error = %v", err)
	}
	if opts.output != "backup.astonish-backup" {
		t.Fatalf("output = %q, want backup.astonish-backup", opts.output)
	}
	if opts.compression != backup.CompressionGzip {
		t.Fatalf("compression = %q, want gzip", opts.compression)
	}
}

func TestParseBackupCreateArgsCompressionNone(t *testing.T) {
	opts, err := parseBackupCreateArgs([]string{"--output", "backup.astonish-backup", "--compression", "none"})
	if err != nil {
		t.Fatalf("parseBackupCreateArgs() error = %v", err)
	}
	if opts.compression != backup.CompressionNone {
		t.Fatalf("compression = %q, want none", opts.compression)
	}
}

func TestParseBackupCreateArgsRedactSecrets(t *testing.T) {
	opts, err := parseBackupCreateArgs([]string{"--output", "backup.astonish-backup", "--redact-secrets", "--passphrase", "secret"})
	if err != nil {
		t.Fatalf("parseBackupCreateArgs() error = %v", err)
	}
	if !opts.redactSecrets {
		t.Fatal("redactSecrets = false, want true")
	}
	if opts.passphrase != "secret" {
		t.Fatalf("passphrase = %q, want secret", opts.passphrase)
	}
}

func TestParseBackupCreateArgsRejectsUnknownCompression(t *testing.T) {
	if _, err := parseBackupCreateArgs([]string{"--output", "backup.astonish-backup", "--compression", "zip"}); err == nil {
		t.Fatal("parseBackupCreateArgs() error = nil, want unsupported compression error")
	}
}

func TestParseBackupCreateArgsScopedBackup(t *testing.T) {
	opts, err := parseBackupCreateArgs([]string{"--org", "acme", "--team", "sre", "--output", "backup.astonish-backup"})
	if err != nil {
		t.Fatalf("parseBackupCreateArgs() error = %v", err)
	}
	if opts.orgSlug != "acme" || opts.teamSlug != "sre" {
		t.Fatalf("scope = %s/%s, want acme/sre", opts.orgSlug, opts.teamSlug)
	}
}

func TestParseBackupCreateArgsTeamRequiresOrg(t *testing.T) {
	if _, err := parseBackupCreateArgs([]string{"--team", "sre", "--output", "backup.astonish-backup"}); err == nil {
		t.Fatal("parseBackupCreateArgs() error = nil, want --team requires --org")
	}
}

func TestBackupEntstoreConfigDefaultsEmptyBackendToSQLite(t *testing.T) {
	cfg, backend, err := backupEntstoreConfig(&config.AppConfig{})
	if err != nil {
		t.Fatalf("backupEntstoreConfig() error = %v", err)
	}
	if backend != "sqlite" {
		t.Fatalf("backend = %q, want sqlite", backend)
	}
	if cfg.DSN == "" || cfg.DataDir == "" {
		t.Fatalf("config = %+v, want sqlite DSN and DataDir", cfg)
	}
}

func TestBackupEntstoreConfigDefaultsFileBackendToSQLite(t *testing.T) {
	cfg, backend, err := backupEntstoreConfig(&config.AppConfig{Storage: config.StorageConfig{Backend: "file"}})
	if err != nil {
		t.Fatalf("backupEntstoreConfig() error = %v", err)
	}
	if backend != "sqlite" {
		t.Fatalf("backend = %q, want sqlite", backend)
	}
	if cfg.DSN == "" || cfg.DataDir == "" {
		t.Fatalf("config = %+v, want sqlite DSN and DataDir", cfg)
	}
}

func TestHandlePlatformBackupVerify(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.astonish-backup")
	writer, err := backup.Create(archivePath)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	var records bytes.Buffer
	if err := backup.NewRecordWriter(&records, "users").Write("test-user", map[string]any{"id": "test-user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.AddFile("platform/users.jsonl", &records); err != nil {
		t.Fatalf("AddFile() error = %v", err)
	}
	manifest := backup.NewManifest("sqlite", "logical", []backup.Scope{{Kind: "platform"}})
	manifest.Entries = []backup.Entry{{Path: "platform/users.jsonl", Kind: "jsonl", Scope: backup.Scope{Kind: "platform"}, Entity: "users", Records: 1}}
	if err := writer.CloseWithManifest(manifest); err != nil {
		t.Fatalf("CloseWithManifest() error = %v", err)
	}

	if err := handlePlatformBackupVerify([]string{archivePath, "--json"}); err != nil {
		t.Fatalf("handlePlatformBackupVerify() error = %v", err)
	}
}

// Execute the real CLI handlers in an isolated process/config. No live daemon or
// backend is contacted: an invalid backend proves lazy ordinary-backup behavior
// and the file-first path must reach that configured backend (not SQL-only success).
func TestPlatformBackupCLIIsolated(t *testing.T) {
	if mode := os.Getenv("ASTONISH_BACKUP_CLI_CHILD"); mode != "" {
		path := os.Getenv("ASTONISH_BACKUP_CLI_ARCHIVE")
		err := handlePlatformBackupCommand([]string{"create", "--output", path})
		if mode == "unsupported" {
			if err == nil || !strings.Contains(err.Error(), "unsupported backend kind") {
				t.Fatalf("configured file backend was not required: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := handlePlatformBackupCommand([]string{"verify", path, "--json"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	_, s, err := entstore.NewPlatformServices(context.Background(), entstore.Config{DSN: filepath.Join(data, "platform.db"), DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "cli", Name: "CLI", Status: "active"}
	if err = s.Organizations().Create(t.Context(), org); err != nil {
		t.Fatal(err)
	}
	if err = s.ProvisionOrg(t.Context(), org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := s.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Name: "CLI", Slug: "cli", SchemaName: "cli"}
	if err = ods.Teams().CreateTeam(t.Context(), team); err != nil {
		t.Fatal(err)
	}
	if err = ods.ProvisionTeam(t.Context(), team.Slug); err != nil {
		t.Fatal(err)
	}
	cfgdir := filepath.Join(dir, "config", "astonish")
	if err = os.MkdirAll(cfgdir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := "storage:\n  backend: sqlite\n  sqlite:\n    data_dir: " + data + "\nsandbox:\n  backend: deliberately-unsupported\n"
	if err = os.WriteFile(filepath.Join(cfgdir, "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "backup.archive")
	run := func(mode string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestPlatformBackupCLIIsolated$", "-test.v")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "XDG_CONFIG_HOME=" + filepath.Join(dir, "config"), "ASTONISH_BACKUP_CLI_CHILD=" + mode, "ASTONISH_BACKUP_CLI_ARCHIVE=" + archive}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("CLI %s: %v\n%s", mode, err, out)
		}
	}
	run("ordinary")
	before, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	// A file-first independently-owned template forces backend admission before
	// opening its intentionally absent object. Its SQL envelope is not inline.
	id := uuid.New()
	ref := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{OrgID: "cli", TeamID: "cli"}, Kind: store.BotContentTemplate, Revision: 1, Key: "staged-v1/template-1-" + uuid.NewString(), SizeBytes: 2, SHA256: strings.Repeat("a", 64)}
	record, _ := json.Marshal(map[string]any{"format": 2, "id": id, "content": ref})
	definition, _ := json.Marshal(map[string]any{"bot_record_v2": string(record)})
	db, err := sql.Open("sqlite", filepath.Join(data, "orgs", "cli", "teams", "cli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO bot_templates (id,name,revision,definition,published_by,published_at) VALUES (?, 'CLI', 1, ?, ?, CURRENT_TIMESTAMP)`, id.String(), string(definition), uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	run("unsupported")
	after, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed CLI export damaged prior backup: %v", err)
	}
}
