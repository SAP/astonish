//go:build linux

package sandbox

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCompiledWorkspaceSharedReadLock(t *testing.T) {
	root := knowledgeCompiledRoot(t)
	raw, _ := json.Marshal(map[string]any{"op": "write", "binding": "scope", "path": "tasks/task/state/value", "data": []byte("old")})
	if err := RunBotHomeWorkspace(root, strings.NewReader(string(raw)+"\ncommit\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	called := false
	out := &workspaceExchangeWriter{hook: func(b []byte) {
		called = true
		if !bytes.Equal(b, []byte("\"b2xk\"\n")) {
			t.Fatalf("read bytes: %s", b)
		}
		lock, err := os.OpenFile(filepath.Join(root.Name(), ".workspace-lock"), os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			t.Fatal("legacy writer not excluded while read streams")
		}
		if err = unix.Flock(int(lock.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
			t.Fatalf("readers not shared: %v", err)
		}
	}}
	raw, _ = json.Marshal(map[string]any{"op": "read", "binding": "scope", "path": "tasks/task/state/value", "limit": 1024})
	if err := RunBotHomeWorkspace(root, strings.NewReader(string(raw)+"\n"), out); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("read did not stream")
	}
}
