package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SAP/astonish/pkg/backup"
)

func (s *Store) restoreTargetEmpty(ctx context.Context) (bool, error) {
	if s.dialect == DialectPostgres {
		return s.postgresRestoreTargetEmpty(ctx)
	}
	return s.sqliteRestoreTargetEmpty(ctx)
}

func (s *Store) sqliteRestoreTargetEmpty(ctx context.Context) (bool, error) {
	if s.dialect != DialectSQLite {
		return true, nil
	}
	for _, table := range []string{"users", "organizations", "org_memberships", "oidc_providers", "platform_settings", "platform_secrets"} {
		exists, err := sqliteTableExists(ctx, s.platformDB, table)
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		var count int
		if err := s.platformDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteSQLiteIdent(table)).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return false, nil
		}
	}
	return true, nil
}

func (s *Store) resetSQLiteRestoreTarget(ctx context.Context) error {
	if s.dialect != DialectSQLite {
		return fmt.Errorf("reset-target restore requires sqlite backend, got %s", s.dialect)
	}
	dataDir := s.dataDir
	if dataDir == "" {
		dataDir = sqliteDataDirFromDSN(s.platformDSN)
	}
	if dataDir == "" {
		return fmt.Errorf("sqlite data directory is required for reset-target restore")
	}

	embedFunc := s.embedFunc
	cfg := Config{
		DSN:             s.platformDSN,
		DataDir:         dataDir,
		MaxOpenConns:    s.maxOpenConns,
		MaxIdleConns:    s.maxIdleConns,
		ConnMaxLifetime: s.connMaxLifetime,
	}
	s.orgClients.Range(func(key, value any) bool {
		if ds, ok := value.(*orgDataStore); ok {
			_ = ds.Close()
		}
		s.orgClients.Delete(key)
		return true
	})
	if err := s.Close(); err != nil {
		return fmt.Errorf("close sqlite target before reset: %w", err)
	}

	for _, path := range sqliteRestoreResetPaths(dataDir) {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("create sqlite data directory: %w", err)
	}
	if err := BootstrapPlatform(ctx, cfg, nil); err != nil {
		return fmt.Errorf("bootstrap reset sqlite target: %w", err)
	}

	s.platformClient = nil
	s.platformDB = nil
	s.platformDSN = cfg.DSN
	s.dataDir = dataDir
	s.maxOpenConns = cfg.MaxOpenConns
	s.maxIdleConns = cfg.MaxIdleConns
	s.connMaxLifetime = cfg.ConnMaxLifetime
	s.embedFunc = embedFunc
	if err := s.openSQLite(ctx, cfg.DSN); err != nil {
		return fmt.Errorf("reopen reset sqlite target: %w", err)
	}
	s.sandboxTemplates = &sandboxTemplateStore{client: s.platformClient}
	return nil
}

func sqliteRestoreResetPaths(dataDir string) []string {
	return []string{
		filepath.Join(dataDir, "platform.db"),
		filepath.Join(dataDir, "platform.db-wal"),
		filepath.Join(dataDir, "platform.db-shm"),
		filepath.Join(dataDir, "platform.db-journal"),
		filepath.Join(dataDir, "orgs"),
		filepath.Join(dataDir, "apps"),
		filepath.Join(dataDir, "fleet_state"),
		filepath.Join(dataDir, "thread_index.json"),
		filepath.Join(dataDir, "sandbox_sessions.json"),
		filepath.Join(dataDir, "sessions.json"),
	}
}

func (s *Store) restoreSQLiteLogicalBackup(ctx context.Context, archivePath string, opts PlatformRestoreOptions, plan backup.RestorePlan) (*backup.RestoreResult, error) {
	manifest := mappedManifestForRestore(plan.Archive.Manifest, opts)
	if err := checkSQLiteScopeSchemaCompatible(ctx, manifest, s.platformDB, backup.Scope{Kind: "platform"}); err != nil {
		return nil, err
	}
	files, err := backup.ReadArchiveFiles(archivePath, backup.ReaderOptions{Passphrase: opts.Passphrase})
	if err != nil {
		return nil, err
	}
	result := &backup.RestoreResult{Plan: plan, Warnings: append([]string(nil), plan.Warnings...)}

	if err := restoreLogicalEntries(ctx, s.platformDB, DialectSQLite, "", backup.Scope{Kind: "platform"}, files, entriesForScopeForRestore(plan.Archive.Manifest.Entries, backup.Scope{Kind: "platform"}, opts), opts, result); err != nil {
		return nil, fmt.Errorf("restore platform entries: %w", err)
	}

	orgScopes := scopesOfKind(manifest.Scopes, "org")
	for _, scope := range orgScopes {
		if err := s.ProvisionOrg(ctx, restoredOrgID(files, scope.OrgSlug, opts), scope.OrgSlug); err != nil {
			return nil, fmt.Errorf("provision org %s: %w", scope.OrgSlug, err)
		}
		orgDB, err := openRestoreSQLiteDB(filepath.Join(s.dataDir, "orgs", scope.OrgSlug, "org.db"))
		if err != nil {
			return nil, err
		}
		if err := checkSQLiteScopeSchemaCompatible(ctx, manifest, orgDB, scope); err != nil {
			_ = orgDB.Close()
			return nil, err
		}
		if err := restoreLogicalEntries(ctx, orgDB, DialectSQLite, "", scope, files, entriesForScopeForRestore(plan.Archive.Manifest.Entries, scope, opts), opts, result); err != nil {
			_ = orgDB.Close()
			return nil, fmt.Errorf("restore org %s entries: %w", scope.OrgSlug, err)
		}
		if err := orgDB.Close(); err != nil {
			return nil, err
		}
	}

	for _, scope := range scopesOfKind(manifest.Scopes, "team") {
		orgStore, err := s.ForOrg(scope.OrgSlug)
		if err != nil {
			return nil, fmt.Errorf("open org %s: %w", scope.OrgSlug, err)
		}
		if err := orgStore.ProvisionTeam(ctx, scope.TeamSlug); err != nil {
			return nil, fmt.Errorf("provision team %s/%s: %w", scope.OrgSlug, scope.TeamSlug, err)
		}
		teamDB, err := openRestoreSQLiteDB(filepath.Join(s.dataDir, "orgs", scope.OrgSlug, "teams", scope.TeamSlug+".db"))
		if err != nil {
			return nil, err
		}
		if err := checkSQLiteScopeSchemaCompatible(ctx, manifest, teamDB, scope); err != nil {
			_ = teamDB.Close()
			return nil, err
		}
		if err := restoreLogicalEntries(ctx, teamDB, DialectSQLite, "", scope, files, entriesForScopeForRestore(plan.Archive.Manifest.Entries, scope, opts), opts, result); err != nil {
			_ = teamDB.Close()
			return nil, fmt.Errorf("restore team %s/%s entries: %w", scope.OrgSlug, scope.TeamSlug, err)
		}
		if err := teamDB.Close(); err != nil {
			return nil, err
		}
	}

	for _, scope := range scopesOfKind(manifest.Scopes, "personal") {
		orgStore, err := s.ForOrg(scope.OrgSlug)
		if err != nil {
			return nil, fmt.Errorf("open org %s: %w", scope.OrgSlug, err)
		}
		if err := orgStore.ProvisionPersonalSchema(ctx, scope.UserID); err != nil {
			return nil, fmt.Errorf("provision personal %s/%s: %w", scope.OrgSlug, scope.UserID, err)
		}
		personalDB, err := openRestoreSQLiteDB(filepath.Join(s.dataDir, "orgs", scope.OrgSlug, "personal", scope.UserID+".db"))
		if err != nil {
			return nil, err
		}
		if err := checkSQLiteScopeSchemaCompatible(ctx, manifest, personalDB, scope); err != nil {
			_ = personalDB.Close()
			return nil, err
		}
		if err := restoreLogicalEntries(ctx, personalDB, DialectSQLite, "", scope, files, entriesForScopeForRestore(plan.Archive.Manifest.Entries, scope, opts), opts, result); err != nil {
			_ = personalDB.Close()
			return nil, fmt.Errorf("restore personal %s/%s entries: %w", scope.OrgSlug, scope.UserID, err)
		}
		if err := personalDB.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func checkSQLiteScopeSchemaCompatible(ctx context.Context, manifest backup.Manifest, db *sql.DB, scope backup.Scope) error {
	archiveVersion, ok := manifest.SchemaVersions[backupScopeKey(scope)]
	if !ok {
		return nil
	}
	targetVersion, err := schemaVersionForScope(ctx, "sqlite", db, scope)
	if err != nil {
		return fmt.Errorf("read target schema for %s: %w", backupScopeKey(scope), err)
	}
	if mismatch := checkSchemaCompatible(archiveVersion, targetVersion); mismatch != nil {
		compatible, err := sqlitePreBotBackupCompatible(ctx, db, manifest, scope, archiveVersion)
		if err != nil {
			return err
		}
		if !compatible {
			return mismatch
		}
	}
	return nil
}

func openRestoreSQLiteDB(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=10000"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func sqliteForeignKeyCheck(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("sqlite foreign key check failed")
	}
	return rows.Err()
}

func sqliteTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func entriesForScopeForRestore(entries []backup.Entry, targetScope backup.Scope, opts PlatformRestoreOptions) []backup.Entry {
	var out []backup.Entry
	for _, entry := range entries {
		if mappedRestoreScope(entry.Scope, opts) == targetScope {
			out = append(out, entry)
		}
	}
	return out
}

func scopesOfKind(scopes []backup.Scope, kind string) []backup.Scope {
	var out []backup.Scope
	for _, scope := range scopes {
		if scope.Kind == kind {
			out = append(out, scope)
		}
	}
	return out
}

func restoreTablePriority(entity string) int {
	switch entity {
	case "users", "organizations", "teams":
		return 0
	case "org_memberships", "team_memberships":
		return 1
	case "sessions":
		return 2
	case "bot_schema_version", "bots", "bot_templates":
		return 4
	case "bot_versions", "bot_goals", "bot_runs":
		return 5
	case "bot_tracked_items", "bot_approvals", "bot_outbox":
		return 6
	case "bot_skills":
		return 7
	case "bot_skill_revisions":
		return 8
	case "session_events", "chat_session_events", "sandbox_sessions":
		return 3
	default:
		return 10
	}
}

func restoredOrgID(files map[string][]byte, orgSlug string, opts PlatformRestoreOptions) string {
	data, ok := files["platform/organizations.jsonl"]
	if !ok {
		return ""
	}
	scanner := backup.NewRecordScanner(bytes.NewReader(data), "organizations")
	for scanner.Next() {
		record, err := scanner.Record()
		if err != nil {
			continue
		}
		row, err := backup.DecodeRecordData(record)
		if err != nil {
			continue
		}
		targetSlug := fmt.Sprint(row["slug"])
		if mapped := opts.MapOrg[targetSlug]; mapped != "" {
			targetSlug = mapped
		}
		if targetSlug == orgSlug {
			return record.ID
		}
	}
	return ""
}
