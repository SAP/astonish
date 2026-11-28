package docker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotHomeContentReadbackProgram(t *testing.T) {
	home, private := t.TempDir(), t.TempDir()
	body := bytes.Repeat([]byte("stored\x00bytes\n"), 10000)
	raw, err := filesProgram(t, home, private, map[string]any{"op": "put", "kind": "evidence", "revision": 1, "size": len(body), "readback": true}, body, true)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(bytes.NewReader(raw))
	if _, err = reader.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var entry store.BotFileEntry
	if err = json.Unmarshal(line, &entry); err != nil {
		t.Fatal(err)
	}
	got, err := sandbox.ReadBotHomeContentReadback(reader, int64(len(body)), entry.SHA256)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("readback", err)
	}
	disk, err := os.ReadFile(filepath.Join(private, entry.Path))
	if err != nil || !bytes.Equal(disk, got) {
		t.Fatal("not stored bytes", err)
	}
	if _, err = filesProgram(t, home, private, map[string]any{"op": "put", "kind": "evidence", "revision": 1, "size": len(body), "readback": true}, body, false); err == nil {
		t.Fatal("denied commit accepted")
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "/must-not-run"}}
	if _, _, err = db.PutBotContentReadback(t.Context(), nil, store.BotContentEvidence, sandbox.BotHomeContentReadbackLimit+1, bytes.NewReader(nil), 1, func(f func() error) error { return f() }); err == nil {
		t.Fatal("bound accepted")
	}
}
