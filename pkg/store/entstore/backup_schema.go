package entstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/backup"
)

func backupScopeKey(scope backup.Scope) string {
	switch scope.Kind {
	case "platform":
		return "platform"
	case "org":
		return "org:" + scope.OrgSlug
	case "team":
		return "team:" + scope.OrgSlug + ":" + scope.TeamSlug
	case "personal":
		return "personal:" + scope.OrgSlug + ":" + scope.UserID
	default:
		return scope.Kind
	}
}

func schemaVersionForScope(ctx context.Context, backend string, db *sql.DB, scope backup.Scope) (backup.SchemaVersion, error) {
	dialect := Dialect(backend)
	schema := ""
	if dialect == DialectPostgres {
		schema = postgresSchemaForScope(scope)
	}
	hash, err := schemaHash(ctx, db, dialect, schema)
	if err != nil {
		return backup.SchemaVersion{}, err
	}
	return backup.SchemaVersion{
		Scope:   backupScopeKey(scope),
		Version: backend + ":" + backup.ArchiveFormat,
		Hash:    hash,
	}, nil
}

func schemaHash(ctx context.Context, db *sql.DB, dialect Dialect, schema string) (string, error) {
	if dialect == DialectSQLite {
		return sqliteSchemaHash(ctx, db)
	}
	return logicalSchemaHash(ctx, db, dialect, schema)
}

func sqliteSchemaHash(ctx context.Context, db *sql.DB) (string, error) {
	tables, err := sqliteUserTables(ctx, db)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, table := range tables {
		columns, err := sqliteTableSchema(ctx, db, table)
		if err != nil {
			return "", err
		}
		parts = append(parts, table+"("+strings.Join(columns, ",")+")")
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// sqlitePreBotSchemaHash reconstructs only the known additive Bot v1 delta.
// It retains every other table, column type, nullability, primary key and order;
// this is not a general subset-schema compatibility check.
func sqlitePreBotSchemaHash(ctx context.Context, db *sql.DB, scope backup.Scope) (string, error) {
	tables, err := sqliteUserTables(ctx, db)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, table := range tables {
		if preBotAddedTable(table, scope.Kind) {
			continue
		}
		columns, err := sqliteTableSchema(ctx, db, table)
		if err != nil {
			return "", err
		}
		var kept []string
		for _, column := range columns {
			fields := strings.SplitN(column, ":", 3)
			if len(fields) != 3 {
				return "", fmt.Errorf("invalid schema column %q", column)
			}
			name := fields[1]
			if table == "sessions" && (name == "bot_id" || name == "bot_run_id" || name == "purpose" || name == "retention") {
				continue
			}
			if scope.Kind == "team" && table == "sandbox_sessions" && name == "bot_home_binding" {
				continue
			}
			kept = append(kept, fmt.Sprintf("%03d:%s:%s", len(kept), name, fields[2]))
		}
		parts = append(parts, table+"("+strings.Join(kept, ",")+")")
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

func preBotAddedTable(table, scope string) bool {
	switch table {
	case "bots", "bot_schema_version", "bot_versions", "bot_goals", "bot_tracked_items", "bot_runs", "bot_approvals", "bot_outbox", "bot_skills", "bot_skill_revisions":
		return scope == "team" || scope == "personal"
	case "bot_templates":
		return scope == "team"
	}
	return false
}

func sqlitePreBotBackupCompatible(ctx context.Context, db *sql.DB, manifest backup.Manifest, scope backup.Scope, archive backup.SchemaVersion) (bool, error) {
	if scope.Kind != "team" && scope.Kind != "personal" {
		return false, nil
	}
	if archive.Version != "sqlite:"+backup.ArchiveFormat || archive.Hash == "" {
		return false, nil
	}
	if ok, err := preSkillBackupCompatible(ctx, db, DialectSQLite, manifest, scope, archive); ok || err != nil {
		return ok, err
	}
	for _, entry := range manifest.Entries {
		if entry.Scope == scope && preBotAddedTable(entry.Entity, scope.Kind) {
			return false, nil
		}
	}
	hash, err := sqlitePreBotSchemaHash(ctx, db, scope)
	return err == nil && hash == archive.Hash, err
}

// postgresPreBotSchemaHash uses the existing PostgreSQL archive representation
// (table names and ordered column names), removing the known Bot additions.
// It is deliberately not a subset-schema compatibility check.
func postgresPreBotSchemaHash(ctx context.Context, db *sql.DB, scope backup.Scope) (string, error) {
	schema := postgresSchemaForScope(scope)
	tables, err := userTables(ctx, db, DialectPostgres, schema)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, table := range tables {
		if preBotAddedTable(table, scope.Kind) {
			continue
		}
		columns, err := tableColumns(ctx, db, DialectPostgres, schema, table)
		if err != nil {
			return "", err
		}
		var kept []string
		for _, name := range columns {
			if table == "sessions" && (name == "bot_id" || name == "bot_run_id" || name == "purpose" || name == "retention") {
				continue
			}
			if scope.Kind == "team" && table == "sandbox_sessions" && name == "bot_home_binding" {
				continue
			}
			kept = append(kept, name)
		}
		parts = append(parts, table+"("+strings.Join(kept, ",")+")")
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

func postgresPreBotBackupCompatible(ctx context.Context, db *sql.DB, manifest backup.Manifest, scope backup.Scope, archive backup.SchemaVersion) (bool, error) {
	if scope.Kind != "team" && scope.Kind != "personal" {
		return false, nil
	}
	if archive.Version != "postgres:"+backup.ArchiveFormat || archive.Hash == "" {
		return false, nil
	}
	if ok, err := preSkillBackupCompatible(ctx, db, DialectPostgres, manifest, scope, archive); ok || err != nil {
		return ok, err
	}
	for _, entry := range manifest.Entries {
		if entry.Scope == scope && preBotAddedTable(entry.Entity, scope.Kind) {
			return false, nil
		}
	}
	hash, err := postgresPreBotSchemaHash(ctx, db, scope)
	return err == nil && hash == archive.Hash, err
}

// preSkillBackupCompatible recognizes exactly the Bot v1 schema: only the two
// v2 skill tables are absent. No arbitrary missing columns/tables are accepted.
func preSkillBackupCompatible(ctx context.Context, db *sql.DB, dialect Dialect, manifest backup.Manifest, scope backup.Scope, archive backup.SchemaVersion) (bool, error) {
	for _, entry := range manifest.Entries {
		if entry.Scope == scope && (entry.Entity == "bot_skills" || entry.Entity == "bot_skill_revisions") {
			return false, nil
		}
	}
	schema := ""
	if dialect == DialectPostgres {
		schema = postgresSchemaForScope(scope)
	}
	tables, err := userTables(ctx, db, dialect, schema)
	if err != nil {
		return false, err
	}
	var parts []string
	for _, table := range tables {
		if table == "bot_skills" || table == "bot_skill_revisions" {
			continue
		}
		var columns []string
		if dialect == DialectSQLite {
			columns, err = sqliteTableSchema(ctx, db, table)
		} else {
			columns, err = tableColumns(ctx, db, dialect, schema, table)
		}
		if err != nil {
			return false, err
		}
		parts = append(parts, table+"("+strings.Join(columns, ",")+")")
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:]) == archive.Hash, nil
}

func sqliteTableSchema(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteSQLiteIdent(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, fmt.Sprintf("%03d:%s:%s:%d:%d", cid, name, strings.ToUpper(typ), notNull, pk))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func logicalSchemaHash(ctx context.Context, db *sql.DB, dialect Dialect, schema string) (string, error) {
	tables, err := userTables(ctx, db, dialect, schema)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, table := range tables {
		columns, err := tableColumns(ctx, db, dialect, schema, table)
		if err != nil {
			return "", err
		}
		parts = append(parts, table+"("+strings.Join(columns, ",")+")")
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

func postgresSchemaForScope(scope backup.Scope) string {
	switch scope.Kind {
	case "team":
		return teamSchemaName(scope.TeamSlug)
	case "personal":
		return personalSchemaName(scope.UserID)
	default:
		return "public"
	}
}

func checkSchemaCompatible(archive, target backup.SchemaVersion) error {
	if archive.Hash == "" || target.Hash == "" {
		return nil
	}
	archiveBackend := strings.SplitN(archive.Version, ":", 2)[0]
	targetBackend := strings.SplitN(target.Version, ":", 2)[0]
	if archiveBackend != "" && targetBackend != "" && archiveBackend != targetBackend {
		return nil
	}
	if archive.Hash != target.Hash {
		return fmt.Errorf("target schema for scope %q does not match archive schema; run migrations, use a matching Astonish version, or restore a backup created from a compatible schema", archive.Scope)
	}
	return nil
}
