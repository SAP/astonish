//go:build integration

package entstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This exercises the real public restore entrypoint and private PostgreSQL stage,
// using an in-memory retained-Home backend. Physical Docker/K8s evidence remains
// a separate required fixture, not something this test pretends to provide.
func TestBackupFileFirstPostgresRestoreStagingBlocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DSN") == "" {
		t.Fatal("BLOCKED: ASTONISH_TEST_DSN required; isolated disposable PostgreSQL only")
	}
	for _, kind := range []string{"personal", "team"} {
		t.Run(kind, func(t *testing.T) {
			target := setupPostgresBackupRestoreStore(t, "filefirst_target")
			target.dataDir = t.TempDir()
			if err := prepareBotContentDirectory(target.dataDir); err != nil {
				t.Fatal(err)
			}
			src, _, _, source := fileFirstExportFixture(t, kind)
			archive := filepath.Join(t.TempDir(), "backup")
			if err := src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return source, nil }}); err != nil {
				t.Fatal(err)
			}
			backend := &fileFirstRestoreTestBackend{fileFirstTestBackend: source}
			opts := PlatformRestoreOptions{MapOrg: map[string]string{"archive": "restored"}, MapTeam: map[string]string{"archive/original": "restored/renamed"}, FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }}
			// Only exact generated receipts from this test's trusted local journal may
			// be cleaned. Committed test rows are removed in the test-owned platform
			// database first, after all private and published handles have been closed.
			t.Cleanup(func() {
				raw, err := os.ReadFile(filepath.Join(target.dataDir, restoreJournalDir, "journal.json"))
				if os.IsNotExist(err) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				var j restoreJournal
				if err = json.Unmarshal(raw, &j); err != nil {
					t.Error(err)
					return
				}
				if _, err = target.platformDB.ExecContext(context.Background(), "DELETE FROM public.org_memberships"); err != nil {
					t.Error(err)
					return
				}
				if _, err = target.platformDB.ExecContext(context.Background(), "DELETE FROM public.organizations"); err != nil {
					t.Error(err)
					return
				}
				root, err := os.OpenRoot(target.dataDir)
				if err != nil {
					t.Error(err)
					return
				}
				defer root.Close()
				j.Phase = restoreFailed // test-owned committed fixture only, not product cleanup
				if err = target.cleanupPostgresRestoreDatabases(context.Background(), root, &j); err != nil {
					t.Error(err)
				}
			})
			plan, err := target.PlanPlatformRestore(t.Context(), archive, opts)
			if err != nil || len(plan.Blockers) != 0 {
				t.Fatal(err, plan)
			}
			result, err := target.RestorePlatformBackup(t.Context(), archive, opts)
			if err != nil || result.RestoredRecords == 0 {
				t.Fatal(err, result)
			}
			var route string
			if err = target.platformDB.QueryRowContext(t.Context(), "SELECT db_name FROM public.organizations WHERE slug = 'restored'").Scan(&route); err != nil || !strings.HasPrefix(route, "astonish_restore_") {
				t.Fatal(err, route)
			}
			o, err := target.ForOrg("restored")
			if err != nil {
				t.Fatal(err)
			}
			defer o.Close()
			var n int
			if err = o.(*orgDataStore).db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM public.teams WHERE slug = 'renamed'`).Scan(&n); err != nil || n != 1 {
				t.Fatal(err, n)
			}
			if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err == nil {
				t.Fatal("committed fixture cleanup admitted")
			}
		})
	}
}
