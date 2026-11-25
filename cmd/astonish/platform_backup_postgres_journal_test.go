package astonish

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

func TestPlatformBackupPostgresExplicitJournalRoot(t *testing.T) {
	cfg := &config.AppConfig{}
	cfg.Storage.Backend = "postgres"
	cfg.Storage.Postgres.PlatformDSN = "postgres://test@localhost/test"
	cfg.Storage.SQLite.DataDir = t.TempDir()
	entCfg, backend, err := backupEntstoreConfig(cfg)
	if err != nil || backend != "postgres" || entCfg.DataDir != cfg.Storage.SQLite.DataDir {
		t.Fatal(err, entCfg, backend)
	}
	cfg.Storage.SQLite.DataDir = ""
	entCfg, _, err = backupEntstoreConfig(cfg)
	if err != nil || entCfg.DataDir != "" {
		t.Fatal("must not invent an offline content/journal root", err, entCfg)
	}
}

func TestPlatformBackupVerifyUsageFreshTarget(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original; reader.Close(); writer.Close() }()
	printPlatformBackupVerifyUsage()
	writer.Close()
	os.Stdout = original
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "fresh target and a journaled retained-Home backend") || strings.Contains(text, "restore remains unsupported") {
		t.Fatal(text)
	}
}
