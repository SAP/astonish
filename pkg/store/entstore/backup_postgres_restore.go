package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"

	"github.com/SAP/astonish/pkg/backup"
)

func (s *Store) postgresRestoreTargetEmpty(ctx context.Context) (bool, error) {
	if s.dialect != DialectPostgres {
		return true, nil
	}
	for _, table := range []string{"users", "organizations", "org_memberships", "oidc_providers", "platform_settings", "platform_secrets"} {
		exists, err := postgresTableExists(ctx, s.platformDB, "public", table)
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		var count int
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s.%s", quoteIdent(DialectPostgres, "public"), quoteIdent(DialectPostgres, table)) //nolint:gosec // table names are hardcoded and quoted.
		if err := s.platformDB.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return false, nil
		}
	}
	return true, nil
}

func (s *Store) restorePostgresLogicalBackup(ctx context.Context, archivePath string, opts PlatformRestoreOptions, plan backup.RestorePlan) (*backup.RestoreResult, error) {
	manifest := mappedManifestForRestore(plan.Archive.Manifest, opts)
	if err := checkPostgresScopeSchemaCompatible(ctx, manifest, s.platformDB, backup.Scope{Kind: "platform"}); err != nil {
		return nil, err
	}
	files, err := backup.ReadArchiveFiles(archivePath, backup.ReaderOptions{Passphrase: opts.Passphrase})
	if err != nil {
		return nil, err
	}
	result := &backup.RestoreResult{Plan: plan, Warnings: append([]string(nil), plan.Warnings...)}

	platformScope := backup.Scope{Kind: "platform"}
	if err := restoreLogicalEntries(ctx, s.platformDB, DialectPostgres, "public", platformScope, files, entriesForScopeForRestore(plan.Archive.Manifest.Entries, platformScope, opts), opts, result); err != nil {
		return nil, fmt.Errorf("restore platform entries: %w", err)
	}

	for _, scope := range scopesOfKind(manifest.Scopes, "org") {
		orgID := restoredOrgID(files, scope.OrgSlug, opts)
		if err := s.ProvisionOrg(ctx, orgID, scope.OrgSlug); err != nil {
			return nil, fmt.Errorf("provision org %s: %w", scope.OrgSlug, err)
		}
		orgDB, err := s.openRestorePostgresDB(scope.OrgSlug, restoredOrgDBName(files, scope.OrgSlug, opts))
		if err != nil {
			return nil, err
		}
		if err := checkPostgresScopeSchemaCompatible(ctx, manifest, orgDB, scope); err != nil {
			_ = orgDB.Close()
			return nil, err
		}
		if err := restoreLogicalEntries(ctx, orgDB, DialectPostgres, "public", scope, files, entriesForScopeForRestore(plan.Archive.Manifest.Entries, scope, opts), opts, result); err != nil {
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
		teamDB, err := s.openRestorePostgresDB(scope.OrgSlug, restoredOrgDBName(files, scope.OrgSlug, opts))
		if err != nil {
			return nil, err
		}
		schema := teamSchemaName(scope.TeamSlug)
		if err := checkPostgresScopeSchemaCompatible(ctx, manifest, teamDB, scope); err != nil {
			_ = teamDB.Close()
			return nil, err
		}
		if err := restoreLogicalEntries(ctx, teamDB, DialectPostgres, schema, scope, files, entriesForScopeForRestore(plan.Archive.Manifest.Entries, scope, opts), opts, result); err != nil {
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
		personalDB, err := s.openRestorePostgresDB(scope.OrgSlug, restoredOrgDBName(files, scope.OrgSlug, opts))
		if err != nil {
			return nil, err
		}
		schema := personalSchemaName(scope.UserID)
		if err := checkPostgresScopeSchemaCompatible(ctx, manifest, personalDB, scope); err != nil {
			_ = personalDB.Close()
			return nil, err
		}
		if err := restoreLogicalEntries(ctx, personalDB, DialectPostgres, schema, scope, files, entriesForScopeForRestore(plan.Archive.Manifest.Entries, scope, opts), opts, result); err != nil {
			_ = personalDB.Close()
			return nil, fmt.Errorf("restore personal %s/%s entries: %w", scope.OrgSlug, scope.UserID, err)
		}
		if err := personalDB.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) openRestorePostgresDB(orgSlug, dbName string) (*sql.DB, error) {
	if dbName == "" {
		dbName = s.orgDBName(orgSlug)
	}
	dsn, err := s.deriveDSN(dbName)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(s.maxOpenConns)
	db.SetMaxIdleConns(s.maxIdleConns)
	db.SetConnMaxLifetime(s.connMaxLifetime)
	return db, nil
}

func checkPostgresScopeSchemaCompatible(ctx context.Context, manifest backup.Manifest, db *sql.DB, scope backup.Scope) error {
	archiveVersion, ok := manifest.SchemaVersions[backupScopeKey(scope)]
	if !ok {
		return nil
	}
	targetVersion, err := schemaVersionForScope(ctx, "postgres", db, scope)
	if err != nil {
		return fmt.Errorf("read target schema for %s: %w", backupScopeKey(scope), err)
	}
	if mismatch := checkSchemaCompatible(archiveVersion, targetVersion); mismatch != nil {
		compatible, err := postgresPreBotBackupCompatible(ctx, db, manifest, scope, archiveVersion)
		if err != nil {
			return fmt.Errorf("read pre-Bot target schema for %s: %w", backupScopeKey(scope), err)
		}
		if !compatible {
			return mismatch
		}
	}
	return nil
}

func postgresTableExists(ctx context.Context, db *sql.DB, schema, table string) (bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2)`, schema, table).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func restoredOrgDBName(files map[string][]byte, orgSlug string, opts PlatformRestoreOptions) string {
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
			return ""
		}
	}
	return ""
}
