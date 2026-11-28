package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// A scoped transport with K8s-issued generations, not a Docker concrete type.
type neutralRestoreBackend struct {
	*recoveryRestoreBackend
	wrongReturn bool
	wrongKind   bool
}

func (b *neutralRestoreBackend) Kind() sandbox.BackendKind { return sandbox.BackendKindK8s }
func (b *neutralRestoreBackend) CreateBotHomeRestoreTargetJournaled(ctx context.Context, scope sandbox.BotHomeScope, reserve func(*sandbox.BotHomeBinding) error) (*sandbox.BotHomeBinding, error) {
	h := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindK8s, Ref: "k8s-generation-" + uuid.NewString()}
	if b.wrongKind {
		h.Backend = sandbox.BackendKindDocker
	}
	if err := reserve(h.Clone()); err != nil {
		return nil, err
	}
	b.target = h.Clone()
	if b.wrongReturn {
		h.Ref += "-different"
	}
	return h, nil
}

func TestBackupFileFirstRestoreBackendNeutral(t *testing.T) {
	for _, mode := range []string{"exact", "interrupted-cleanup-retry", "wrong-return", "wrong-kind"} {
		t.Run(mode, func(t *testing.T) {
			src, _, _, source := fileFirstExportFixture(t, "personal")
			archive := filepath.Join(t.TempDir(), "backup")
			if err := src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return source, nil }}); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			_, target, err := NewPlatformServices(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			dest := &fileFirstTestBackend{contentHomeBackend: &contentHomeBackend{objects: map[string][]byte{}}, home: bytes.Clone(source.home)}
			backend := &neutralRestoreBackend{recoveryRestoreBackend: &recoveryRestoreBackend{fileFirstRestoreTestBackend: &fileFirstRestoreTestBackend{fileFirstTestBackend: dest}}, wrongReturn: mode == "wrong-return", wrongKind: mode == "wrong-kind"}
			opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }, interruptAfterFiles: mode == "interrupted-cleanup-retry"}
			_, err = target.RestorePlatformBackup(t.Context(), archive, opts)
			if mode == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(backend.restored, source.home) {
					t.Fatal("Home bytes changed")
				}
				raw, readErr := os.ReadFile(filepath.Join(dir, restoreJournalDir, "journal.json"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				var journal restoreJournal
				if err = json.Unmarshal(raw, &journal); err != nil {
					t.Fatal(err)
				}
				if len(journal.Homes) != 1 || journal.Homes[0].Target != *backend.target || journal.Phase != restoreCommitted {
					t.Fatal("exact generation not committed")
				}
				if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("committed cleanup accepted: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe restore accepted")
			}
			empty, err := target.restoreTargetEmpty(t.Context())
			if err != nil || !empty {
				t.Fatalf("platform was published: %v", err)
			}
			if mode == "wrong-kind" {
				if backend.target != nil {
					t.Fatal("foreign backend allocated")
				}
				return
			}
			if mode == "interrupted-cleanup-retry" {
				backend.deleteError = errors.New("attachment still held")
				if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err == nil {
					t.Fatal("cleanup failure suppressed")
				}
				if _, err = os.Stat(filepath.Join(dir, restoreJournalDir, "journal.json")); err != nil {
					t.Fatal("failed cleanup lost journal")
				}
				backend.deleteError = nil
			}
			if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err != nil {
				t.Fatal(err)
			}
			if len(backend.deleted) != 1 || backend.deleted[0] != *backend.target {
				t.Fatal("cleanup did not address reserved generation")
			}
		})
	}
}
