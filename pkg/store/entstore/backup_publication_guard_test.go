package entstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

func TestPublicBackupPublicationPreservesDestination(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := Config{DSN: "file:" + filepath.Join(dir, "platform.db"), DataDir: dir}
	if err := BootstrapPlatform(ctx, cfg, nil); err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	outputDir := t.TempDir()
	path := filepath.Join(outputDir, "backup")
	if err := os.WriteFile(path, []byte("last-good"), 0600); err != nil {
		t.Fatal(err)
	}
	// The refusal occurs after archive construction starts, not just argument parsing.
	if _, err := s.platformDB.ExecContext(ctx, `CREATE TABLE bot_export_refusal (id TEXT); INSERT INTO bot_export_refusal VALUES ('blocked')`); err != nil {
		t.Fatal(err)
	}
	for _, passphrase := range []string{"", "private-test-passphrase"} {
		err := s.ExportPlatformBackup(ctx, path, PlatformBackupExportOptions{Passphrase: passphrase})
		if !errors.Is(err, store.ErrBotUnsupported) {
			t.Fatalf("want fail-closed Bot refusal, got %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "last-good" {
			t.Fatalf("previous backup changed: %q %v", data, err)
		}
		entries, err := os.ReadDir(outputDir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("partial/plaintext files remain: %v %v", entries, err)
		}
	}
}

func TestPublicBackupPublicationVerifiedPrivateFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := Config{DSN: "file:" + filepath.Join(dir, "platform.db"), DataDir: dir}
	if err := BootstrapPlatform(ctx, cfg, nil); err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, passphrase := range []string{"", "private-test-passphrase"} {
		path := filepath.Join(t.TempDir(), "backup")
		if err := s.ExportPlatformBackup(ctx, path, PlatformBackupExportOptions{Passphrase: passphrase}); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyPlatformBackup(ctx, path, backup.ReaderOptions{Passphrase: passphrase}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("backup permissions: %v %v", info, err)
		}
	}
}

func TestPublicBackupVerifyRejectsChecksummedBotRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "unsupported")
	w, err := backup.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := backup.NewRecordWriter(&data, "bots").Write("bot", map[string]any{"id": "bot"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.AddFile("bots.jsonl", &data); err != nil {
		t.Fatal(err)
	}
	m := backup.NewManifest("sqlite", backupModeLogical, []backup.Scope{{Kind: "platform"}})
	m.Entries = append(m.Entries, backup.Entry{Path: "bots.jsonl", Entity: "bots", Kind: "jsonl", Scope: m.Scopes[0]})
	if err := w.CloseWithManifest(m); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Verify(path); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPlatformBackup(ctx, path, backup.ReaderOptions{}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("want content refusal, got %v", err)
	}
}
