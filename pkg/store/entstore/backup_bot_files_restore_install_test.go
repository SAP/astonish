package entstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotFileRestoreInstallationNoOverwrite(t *testing.T) {
	scope := botFileTestScope()
	disk, err := newBotDiskContent(t.TempDir(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	root, err := disk.scopeRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	body := []byte("original immutable bytes")
	ref := botFileTestRef(scope, store.BotContentDocument, body, false)
	if err = installBotBackupObject(t.Context(), disk, ref, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err = installBotBackupObject(t.Context(), disk, ref, bytes.NewReader(body)); !errors.Is(err, os.ErrExist) {
		t.Fatalf("no overwrite error: %v", err)
	}
	stream, err := disk.Open(t.Context(), ref, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, body) {
		t.Fatal("overwrote original")
	}
	for _, body := range [][]byte{[]byte("corrupt"), append(append([]byte(nil), body...), '!')} {
		bad := botFileTestRef(scope, store.BotContentDocument, []byte("expected"), false)
		if err = installBotBackupObject(t.Context(), disk, bad, bytes.NewReader(body)); err == nil {
			t.Fatal("published corrupt object")
		}
	}
	entries, err := os.ReadDir(filepath.Join(disk.root.Name(), disk.namespace()))
	if err != nil || len(entries) != 1 {
		t.Fatal("left private partial objects", entries, err)
	}
}

func TestBotFileRestoreRequiresReadback(t *testing.T) {
	archive, opts, refs := botRestoreFixture(t)
	target := opts.Targets[refs[0].Scope]
	calls := 0
	// Authorization checks happen at preflight, reservation, install, publication,
	// and repository readback. Fail only once an object has actually been linked.
	target.authorize = func(context.Context) error {
		calls++
		root, err := target.scopeRoot(false)
		if err != nil {
			return nil
		}
		defer root.Close()
		name, err := diskContentName(refs[0])
		if err != nil {
			return err
		}
		if _, err = root.Lstat(name); err == nil {
			return store.ErrBotForbidden
		}
		return nil
	}
	mapping, err := restoreBotFileContent(t.Context(), archive, opts)
	if !errors.Is(err, store.ErrBotForbidden) || mapping != nil || calls == 0 {
		t.Fatalf("returned unverified map: %v %v", mapping, err)
	}
	target.authorize = nil
	// Failure does not remove valid original/installed bytes or claim SQL success.
	stream, err := target.Open(t.Context(), refs[0], store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
}
