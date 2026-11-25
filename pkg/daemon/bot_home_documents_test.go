package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// The setup HTTP fixture now has an explicit on-disk file backend, rather than
// succeeding with an unimplemented embedded file capability.
func (s *setupHomeFilesBackend) documentRoot() (string, error) {
	if s.documentsRoot == "" {
		return "", store.ErrBotUnsupported
	}
	return s.documentsRoot, nil
}
func (s *setupHomeFilesBackend) StatBotHomeFile(_ context.Context, h *sandbox.BotHomeBinding, path string) (*store.BotFileEntry, error) {
	root, err := s.documentRoot()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, h.Scope.BotID, path))
	if os.IsNotExist(err) {
		return nil, store.ErrBotNotFound
	}
	if err != nil {
		return nil, err
	}
	return &store.BotFileEntry{Path: path, Kind: store.BotFileRegular, SizeBytes: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}, nil
}
func (s *setupHomeFilesBackend) WriteBotHomeFile(ctx context.Context, h *sandbox.BotHomeBinding, w store.BotFileWrite) (*store.BotFileEntry, error) {
	root, err := s.documentRoot()
	if err != nil {
		return nil, err
	}
	if w.BeforeCommit == nil {
		return nil, store.ErrBotForbidden
	}
	data, err := io.ReadAll(w.Body)
	if err != nil {
		return nil, err
	}
	err = w.BeforeCommit(func() error {
		path := filepath.Join(root, h.Scope.BotID, w.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if !w.CreateOnly {
			return store.ErrBotForbidden
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			return store.ErrBotConflict
		}
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		return errors.Join(err, file.Close())
	})
	if err != nil {
		return nil, err
	}
	return s.StatBotHomeFile(ctx, h, w.Path)
}

func TestBotFileFirstSetupPreservesOwnerFiles(t *testing.T) {
	backend := &setupHomeFilesBackend{documentsRoot: t.TempDir()}
	home := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{BotID: "test"}}
	version := store.DefaultBotVersion()
	version.Persona.Soul = "reviewed initial soul"
	commits := 0
	authorize := func(commit func() error) error { commits++; return commit() }
	if err := initializeBotHomeDocuments(context.Background(), backend, home, version, authorize); err != nil {
		t.Fatal(err)
	}
	if commits != 5 {
		t.Fatalf("commits=%d", commits)
	}
	path := filepath.Join(backend.documentsRoot, "test", "SOUL.md")
	if err := os.WriteFile(path, []byte("owner edited soul"), 0600); err != nil {
		t.Fatal(err)
	}
	version.Persona.Soul = "new config, not permission to overwrite"
	if err := initializeBotHomeDocuments(context.Background(), backend, home, version, authorize); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "owner edited soul" || commits != 5 {
		t.Fatalf("overwritten: %q %v commits=%d", data, err, commits)
	}
}
func TestBotFileFirstSetupDeniedCommit(t *testing.T) {
	backend := &setupHomeFilesBackend{documentsRoot: t.TempDir()}
	home := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{BotID: "test"}}
	err := initializeBotHomeDocuments(context.Background(), backend, home, store.DefaultBotVersion(), func(func() error) error { return store.ErrBotForbidden })
	if !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	if _, err := backend.StatBotHomeFile(context.Background(), home, "SOUL.md"); !errors.Is(err, store.ErrBotNotFound) {
		t.Fatalf("denied write created bytes: %v", err)
	}
}
