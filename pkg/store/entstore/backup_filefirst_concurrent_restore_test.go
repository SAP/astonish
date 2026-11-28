package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"golang.org/x/sys/unix"
)

type concurrentRestoreBackend struct {
	*recoveryRestoreBackend
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	check   func() error
}

func (b *concurrentRestoreBackend) RestoreBotHomeArchive(ctx context.Context, h *sandbox.BotHomeBinding, m *sandbox.BotHomeArchiveManifest, r io.Reader, barrier sandbox.BotHomeArchiveBarrier) error {
	if err := b.check(); err != nil {
		return err
	}
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := b.recoveryRestoreBackend.RestoreBotHomeArchive(ctx, h, m, r, barrier); err != nil {
		return err
	}
	return b.check()
}

func TestBackupFileFirstSQLiteConcurrentRestoreArbitration(t *testing.T) {
	for _, kind := range []string{"personal", "team"} {
		t.Run(kind, func(t *testing.T) {
			src, sourcePath, sourceDB, source := fileFirstExportFixture(t, kind)
			archive := filepath.Join(t.TempDir(), "backup")
			if err := src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return source, nil }}); err != nil {
				t.Fatal(err)
			}
			for _, outcome := range []string{"commit", "interrupt-sql-cleanup-retry"} {
				t.Run(outcome, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
					defer cancel()
					dir := t.TempDir()
					cfg := Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
					_, target, err := NewPlatformServices(ctx, cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer target.Close()
					// A separate router/pool makes the competing attempt reach the OS
					// restore lock, rather than just queue on target's sole connection.
					competitor, err := New(ctx, cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer competitor.Close()
					check := func() error {
						for _, table := range []string{"users", "organizations", "org_memberships"} {
							var n int
							if err := competitor.platformDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteSQLiteIdent(table)).Scan(&n); err != nil {
								return err
							}
							if n != 0 {
								return fmt.Errorf("%s published %d SQL rows before files", table, n)
							}
						}
						for _, path := range []string{filepath.Join(dir, "orgs", "archive"), filepath.Join(dir, restoreJournalDir, "sql")} {
							if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
								return fmt.Errorf("tenant SQL exists before files: %s: %v", path, err)
							}
						}
						return nil
					}
					backend := &concurrentRestoreBackend{recoveryRestoreBackend: &recoveryRestoreBackend{fileFirstRestoreTestBackend: &fileFirstRestoreTestBackend{fileFirstTestBackend: &fileFirstTestBackend{contentHomeBackend: &contentHomeBackend{objects: map[string][]byte{}}}}}, entered: make(chan struct{}), release: make(chan struct{}), check: check}
					opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }, interruptAfterFiles: outcome != "commit"}
					var release sync.Once
					unblock := func() { release.Do(func() { close(backend.release) }) }
					defer unblock()
					first := make(chan error, 1)
					go func() { _, err := target.RestorePlatformBackup(ctx, archive, opts); first <- err }()
					select {
					case <-backend.entered:
					case err := <-first:
						t.Fatalf("restore never entered files: %v", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					journalPath := filepath.Join(dir, restoreJournalDir, "journal.json")
					before, err := os.ReadFile(journalPath)
					if err != nil {
						t.Fatal(err)
					}
					var backendCalls atomic.Int32
					loserOpts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { backendCalls.Add(1); return backend, nil }}
					second := make(chan error, 1)
					go func() { _, err := competitor.RestorePlatformBackup(ctx, archive, loserOpts); second <- err }()
					select {
					case err := <-second:
						if !errors.Is(err, unix.EWOULDBLOCK) {
							t.Fatalf("second restore did not fail on active writer lock: %v", err)
						}
					case <-ctx.Done():
						t.Fatal("competing restore blocked behind SQL instead of refusing storage lock")
					}
					if err := competitor.CleanupFileFirstRestore(ctx, archive, loserOpts); !errors.Is(err, unix.EWOULDBLOCK) {
						t.Fatalf("cleanup entered active restore: %v", err)
					}
					if backendCalls.Load() != 0 {
						t.Fatalf("losing restore/cleanup touched backend %d times", backendCalls.Load())
					}
					after, err := os.ReadFile(journalPath)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("losing attempt changed reservation journal: %v", err)
					}
					if err := check(); err != nil {
						t.Fatal(err)
					}
					unblock()
					select {
					case err = <-first:
					case <-ctx.Done():
						t.Fatal("winning restore failed to join")
					}
					if outcome != "commit" {
						if !errors.Is(err, errFileFirstSQLInterrupted) {
							t.Fatalf("wrong injected SQL failure: %v", err)
						}
						raw, err := os.ReadFile(journalPath)
						if err != nil {
							t.Fatal(err)
						}
						var journal restoreJournal
						if err := json.Unmarshal(raw, &journal); err != nil {
							t.Fatal(err)
						}
						if journal.Phase != restoreFailed || len(journal.Homes) != 1 || journal.Homes[0].Target != *backend.target {
							t.Fatalf("failure lost exact cleanup ownership: %+v", journal)
						}
						if err := competitor.CleanupFileFirstRestore(ctx, archive, loserOpts); err != nil {
							t.Fatal(err)
						}
						if len(backend.deleted) != 1 || backend.deleted[0] != journal.Homes[0].Target || backend.deleted[0].Ref == journal.Homes[0].Source.Ref {
							t.Fatalf("cleanup did not delete only fresh generation: %+v", backend.deleted)
						}
						if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("cleanup retained journal: %v", err)
						}
						children, err := os.ReadDir(filepath.Join(dir, "bot-content"))
						if err != nil || len(children) != 0 {
							t.Fatalf("cleanup retained staged files: %d %v", len(children), err)
						}
						opts.interruptAfterFiles = false
						if _, err := target.RestorePlatformBackup(ctx, archive, opts); err != nil {
							t.Fatalf("retry after cleanup: %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(source.home, backend.restored) {
						t.Fatal("retained bytes changed")
					}
					// The committed receipt/reservation intentionally remain as a
					// no-cleanup barrier; only private SQL staging is removed.
					raw, err := os.ReadFile(journalPath)
					if err != nil {
						t.Fatal(err)
					}
					var committed restoreJournal
					if err := json.Unmarshal(raw, &committed); err != nil {
						t.Fatal(err)
					}
					if committed.Phase != restoreCommitted || len(committed.Homes) != 1 || committed.Homes[0].Target != *backend.target {
						t.Fatalf("missing committed generation receipt: %+v", committed)
					}
					if _, err := os.Stat(filepath.Join(dir, restoreJournalDir, "sql")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("committed restore leaked private SQL staging: %v", err)
					}
					deleted := len(backend.deleted)
					if err := competitor.CleanupFileFirstRestore(ctx, archive, loserOpts); !errors.Is(err, store.ErrBotConflict) {
						t.Fatalf("committed cleanup accepted: %v", err)
					}
					if len(backend.deleted) != deleted {
						t.Fatal("committed cleanup deleted selected generation")
					}
					if backend.allocations != 1 && (outcome == "commit" || backend.allocations != 2) {
						t.Fatalf("unexpected generations allocated: %d", backend.allocations)
					}
					rel, err := filepath.Rel(src.dataDir, sourcePath)
					if err != nil {
						t.Fatal(err)
					}
					db, err := sql.Open("sqlite", filepath.Join(dir, rel))
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					var status, homeRaw, definition, sourceDefinition string
					if err := db.QueryRowContext(ctx, "SELECT status,home_binding FROM bots").Scan(&status, &homeRaw); err != nil {
						t.Fatal(err)
					}
					var home store.BotHomeBinding
					if err := json.Unmarshal([]byte(homeRaw), &home); err != nil {
						t.Fatal(err)
					}
					if status != string(store.BotPaused) || home.StorageKey != backend.target.Ref {
						t.Fatalf("winning generation not selected paused: %s %+v", status, home)
					}
					if err := db.QueryRowContext(ctx, "SELECT definition FROM bot_versions").Scan(&definition); err != nil {
						t.Fatal(err)
					}
					if err := sourceDB.QueryRowContext(ctx, "SELECT definition FROM bot_versions").Scan(&sourceDefinition); err != nil {
						t.Fatal(err)
					}
					if definition != sourceDefinition {
						t.Fatal("immutable SQL content reference changed")
					}
				})
			}
		})
	}
}
