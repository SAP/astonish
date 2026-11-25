package entstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
)

func TestPostgresFileFirstTenantTransactionAndFailure(t *testing.T) {
	for _, mode := range []string{"success", "second-scope-error", "commit-error", "missing-entry"} {
		t.Run(mode, func(t *testing.T) {
			m := &postgresReceiptSQL{}
			db := sql.OpenDB(m)
			defer db.Close()
			scopes := []backup.Scope{{Kind: "org", OrgSlug: "mapped"}, {Kind: "team", OrgSlug: "mapped", TeamSlug: "target"}, {Kind: "personal", OrgSlug: "mapped", UserID: "00000000-0000-0000-0000-000000000002"}}
			entries := []backup.Entry{{Path: "org/teams", Kind: "jsonl", Entity: "teams", Scope: scopes[0]}, {Path: "team/apps", Kind: "jsonl", Entity: "apps", Scope: scopes[1]}, {Path: "personal/apps", Kind: "jsonl", Entity: "apps", Scope: scopes[2]}}
			files := map[string][]byte{}
			for _, e := range entries {
				files[e.Path] = []byte(`{"entity":"` + e.Entity + `","data":{"id":"00000000-0000-0000-0000-000000000003","name":"disk-reference"}}` + "\n")
			}
			if mode == "missing-entry" {
				delete(files, "personal/apps")
			}
			if mode == "commit-error" {
				m.commitError = errors.New("lost tenant commit acknowledgment")
			}
			m.exec = func(q string, _ []driver.NamedValue) error {
				if mode == "second-scope-error" && strings.Contains(q, `"team_target"."apps"`) {
					return errors.New("tenant insert failure")
				}
				return nil
			}
			opts := PlatformRestoreOptions{fileFirst: &fileFirstRestoreState{mapping: newFileFirstMapping(), files: files}}
			var result backup.RestoreResult
			err := restoreFileFirstPostgresTenantRows(t.Context(), db, scopes, opts, backup.RestorePlan{Archive: backup.Summary{Manifest: backup.Manifest{Entries: entries}}}, &result)
			events := strings.Join(m.events, ";")
			if mode == "success" {
				if err != nil || result.RestoredRecords != 3 || strings.Count(events, "begin") != 1 || strings.Count(events, "commit") != 1 {
					t.Fatal(err, result, events)
				}
				for _, scope := range scopes {
					if !strings.Contains(events, "SET LOCAL search_path TO "+quoteIdent(DialectPostgres, postgresSchemaForScope(scope))) {
						t.Fatal(events)
					}
				}
			} else {
				if err == nil || result.RestoredRecords != 0 {
					t.Fatal(err, result)
				}
				if mode != "commit-error" && (!strings.Contains(events, "rollback") || strings.Contains(events, "commit")) {
					t.Fatal(events)
				}
			}
		})
	}
}

func TestPostgresFileFirstExclusionAndGrantFailures(t *testing.T) {
	for _, mode := range []string{"success", "lock-error", "populated", "grant-error"} {
		t.Run(mode, func(t *testing.T) {
			m := &postgresReceiptSQL{}
			db := sql.OpenDB(m)
			defer db.Close()
			m.exec = func(q string, _ []driver.NamedValue) error {
				if mode == "lock-error" && strings.HasPrefix(q, "LOCK") {
					return errors.New("lock denied")
				}
				if mode == "grant-error" && strings.HasPrefix(q, "GRANT") {
					return errors.New("grant denied")
				}
				return nil
			}
			if mode == "populated" {
				m.query = func(string, []driver.NamedValue) ([]driver.Value, error) { return []driver.Value{int64(1)}, nil }
			}
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			err = beginFileFirstPlatformExclusion(t.Context(), conn, DialectPostgres)
			if mode == "lock-error" || mode == "populated" {
				if err == nil || !strings.Contains(strings.Join(m.events, ";"), "ROLLBACK") {
					t.Fatal(err, m.events)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = conn.ExecContext(t.Context(), "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			conn.Close()
			err = grantStagedPostgresSchema(t.Context(), db, "team_target", true)
			if (err != nil) != (mode == "grant-error") {
				t.Fatal(err)
			}
			events := strings.Join(m.events, ";")
			if strings.Contains(events, "BEGIN IMMEDIATE") || !strings.Contains(events, "SHARE ROW EXCLUSIVE") || !strings.Contains(events, `GRANT USAGE ON SCHEMA "team_target"`) {
				t.Fatal(events)
			}
		})
	}
}

func TestPostgresFileFirstPublicCleanupReceiptRetry(t *testing.T) {
	s, root, j, m := postgresReceiptFixture(t)
	s.dataDir, _ = filepath.EvalSymlinks(root.Name())
	archive := filepath.Join(t.TempDir(), "archive")
	if err := os.WriteFile(archive, []byte("owned-archive"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := restoreArchiveDigest(t.Context(), archive)
	if err != nil {
		t.Fatal(err)
	}
	j.Root = s.dataDir
	j.ArchiveSHA256 = digest
	if err = root.Mkdir("bot-content", 0700); err != nil {
		t.Fatal(err)
	}
	if err = restoreMarker(root, "bot-content/.restore-reservation", j.ID); err != nil {
		t.Fatal(err)
	}
	acknowledgePostgresCreate(t, root, j, m)
	if _, err = s.allocatePostgresRestoreDatabase(t.Context(), root, j, "owned"); err != nil {
		t.Fatal(err)
	}
	j.Phase = restoreFailed
	if err = writeRestoreJournal(root, j); err != nil {
		t.Fatal(err)
	}
	m.query = func(q string, _ []driver.NamedValue) ([]driver.Value, error) {
		if strings.Contains(q, "FROM pg_database") {
			return []driver.Value{int64(123), postgresRestoreReceipt(j, j.PostgresDatabases[0])}, nil
		}
		return []driver.Value{int64(0)}, nil
	}
	backend := &fileFirstRestoreTestBackend{}
	opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }}
	m.exec = func(q string, _ []driver.NamedValue) error {
		if strings.HasPrefix(q, "DROP DATABASE") {
			return errors.New("database attached")
		}
		return nil
	}
	if err = s.CleanupFileFirstRestore(t.Context(), archive, opts); err == nil {
		t.Fatal("attachment cleanup admitted")
	}
	saved := readPostgresReceiptJournal(t, root)
	if saved.PostgresDatabases[0].Cleaned {
		t.Fatal("failed drop discarded retry receipt")
	}
	m.exec = nil
	if err = s.CleanupFileFirstRestore(t.Context(), archive, opts); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Stat(restoreJournalDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("journal not removed", err)
	}
	if !strings.Contains(strings.Join(m.events, ";"), "SHARE ROW EXCLUSIVE") {
		t.Fatal("cleanup not serialized")
	}
}

func TestPostgresFileFirstCapabilityPreflightReadOnly(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		m := &postgresReceiptSQL{query: func(string, []driver.NamedValue) ([]driver.Value, error) { return []driver.Value{allowed}, nil }}
		db := sql.OpenDB(m)
		s := &Store{dialect: DialectPostgres, platformDB: db, platformDSN: "mock", dataDir: t.TempDir()}
		err := s.preflightFileFirstPostgres(t.Context(), backup.Manifest{}, PlatformRestoreOptions{})
		db.Close()
		if (err == nil) != allowed || len(m.events) != 1 || !strings.HasPrefix(m.events[0], "SELECT") {
			t.Fatal(err, m.events)
		}
	}
}
