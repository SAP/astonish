package astonish

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

// This is refusal coverage, not file-first backup/restore acceptance.
func TestPlatformBackupVerifyRefusesBotContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.backup")
	w, err := backup.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := backup.NewRecordWriter(&data, "bots").Write("test", map[string]any{"id": "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.AddFile("bots.jsonl", &data); err != nil {
		t.Fatal(err)
	}
	m := backup.NewManifest("sqlite", "logical", []backup.Scope{{Kind: "platform"}})
	m.Entries = append(m.Entries, backup.Entry{Path: "bots.jsonl", Entity: "bots", Kind: "jsonl", Scope: m.Scopes[0]})
	if err := w.CloseWithManifest(m); err != nil {
		t.Fatal(err)
	}
	if err := handlePlatformBackupCommand([]string{"verify", path, "--json"}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("verify must refuse Bot content, got %v", err)
	}
}
