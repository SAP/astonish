package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/backup"
)

type backupSQLQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type logicalDB struct {
	DB          backupSQLQuery
	Dialect     Dialect
	Schema      string
	ArchiveDir  string
	Scope       backup.Scope
	ScopeName   string
	Description string
}

func exportLogicalDBRows(ctx context.Context, writer *backup.Writer, manifest *backup.Manifest, dbInfo logicalDB, opts PlatformBackupExportOptions) error {
	if opts.WorkspaceBackend != nil && (dbInfo.Scope.Kind == "personal" || dbInfo.Scope.Kind == "team") {
		if opts.WorkspaceFence == nil {
			return fmt.Errorf("workspace backup requires scoped writer fence")
		}
		plain := opts
		plain.WorkspaceBackend = nil
		plain.WorkspaceFence = nil
		return opts.WorkspaceFence(ctx, dbInfo.Scope, func() error {
			if err := exportLogicalDBRows(ctx, writer, manifest, dbInfo, plain); err != nil {
				return err
			}
			return exportBotWorkspaceSnapshots(ctx, writer, manifest, dbInfo, opts)
		})
	}
	if dbInfo.Dialect == DialectSQLite {
		if _, err := dbInfo.DB.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
			return fmt.Errorf("set %s query_only: %w", dbInfo.Description, err)
		}
	}
	version, err := schemaVersionForScope(ctx, string(dbInfo.Dialect), dbInfo.DB, dbInfo.Scope)
	if err != nil {
		return fmt.Errorf("read %s schema version: %w", dbInfo.Description, err)
	}
	if manifest.SchemaVersions == nil {
		manifest.SchemaVersions = make(map[string]backup.SchemaVersion)
	}
	manifest.SchemaVersions[backupScopeKey(dbInfo.Scope)] = version

	tables, err := userTables(ctx, dbInfo.DB, dbInfo.Dialect, dbInfo.Schema)
	if err != nil {
		return fmt.Errorf("list %s tables: %w", dbInfo.Description, err)
	}
	for _, table := range tables {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, records, redacted, err := exportLogicalTable(ctx, dbInfo.DB, dbInfo.Dialect, dbInfo.Schema, table, opts.RedactSecrets)
		if err != nil {
			return fmt.Errorf("export %s table %s: %w", dbInfo.Description, table, err)
		}
		archivePath := archiveJoin(dbInfo.ArchiveDir, table+".jsonl")
		if _, err := writer.AddFile(archivePath, bytes.NewReader(data)); err != nil {
			return err
		}
		manifest.Entries = append(manifest.Entries, backup.Entry{
			Path:     archivePath,
			Kind:     "jsonl",
			Scope:    dbInfo.Scope,
			Entity:   table,
			Records:  records,
			Redacted: redacted,
		})
	}
	if opts.fileInventory != nil {
		if err := opts.fileInventory.finish(); err != nil {
			return err
		}
		if opts.fileInventory.inventory != nil {
			return exportFileFirstScope(ctx, writer, manifest, dbInfo, opts)
		}
	}
	return nil
}

func userTables(ctx context.Context, db backupSQLQuery, dialect Dialect, schema string) ([]string, error) {
	switch dialect {
	case DialectSQLite:
		return sqliteUserTables(ctx, db)
	case DialectPostgres:
		if schema == "" {
			schema = "public"
		}
		rows, err := db.QueryContext(ctx, `
			SELECT table_name
			FROM information_schema.tables
			WHERE table_schema = $1 AND table_type = 'BASE TABLE'
			ORDER BY table_name`, schema)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var tables []string
		for rows.Next() {
			var table string
			if err := rows.Scan(&table); err != nil {
				return nil, err
			}
			if shouldSkipLogicalTable(table) {
				continue
			}
			tables = append(tables, table)
		}
		return tables, rows.Err()
	default:
		return nil, fmt.Errorf("unsupported dialect %s", dialect)
	}
}

func shouldSkipLogicalTable(table string) bool {
	return table == "schema_migrations" || table == "atlas_schema_revisions"
}

func exportLogicalTable(ctx context.Context, db backupSQLQuery, dialect Dialect, schema, table string, redactSecrets bool) ([]byte, int64, bool, error) {
	columns, err := tableColumns(ctx, db, dialect, schema, table)
	if err != nil {
		return nil, 0, false, err
	}
	query, err := selectAllQuery(dialect, schema, table, columns)
	if err != nil {
		return nil, 0, false, err
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()

	var buf bytes.Buffer
	writer := backup.NewRecordWriter(&buf, table)
	redactedAny := false
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, 0, false, err
		}
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, 0, false, err
		}
		record := make(map[string]any, len(columns))
		for i, col := range columns {
			value := normalizeLogicalValue(values[i])
			if redactSecrets {
				redacted, didRedact := redactBackupValue(table, col, value)
				if didRedact {
					redactedAny = true
				}
				record[col] = redacted
				continue
			}
			record[col] = value
		}
		if err := validateBotExportRow(ctx, table, record); err != nil {
			return nil, 0, false, err
		}
		id := backupRecordID(record)
		if err := writer.Write(id, record); err != nil {
			return nil, 0, false, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	return buf.Bytes(), writer.Records(), redactedAny, nil
}

func tableColumns(ctx context.Context, db backupSQLQuery, dialect Dialect, schema, table string) ([]string, error) {
	switch dialect {
	case DialectSQLite:
		return sqliteTableColumns(ctx, db, table)
	case DialectPostgres:
		if schema == "" {
			schema = "public"
		}
		rows, err := db.QueryContext(ctx, `
			SELECT column_name
			FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2
			ORDER BY ordinal_position`, schema, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var columns []string
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				return nil, err
			}
			columns = append(columns, column)
		}
		return columns, rows.Err()
	default:
		return nil, fmt.Errorf("unsupported dialect %s", dialect)
	}
}

func selectAllQuery(dialect Dialect, schema, table string, columns []string) (string, error) {
	quotedCols := make([]string, len(columns))
	for i, col := range columns {
		quotedCols[i] = quoteIdent(dialect, col)
	}
	switch dialect {
	case DialectSQLite:
		return fmt.Sprintf("SELECT %s FROM %s", strings.Join(quotedCols, ", "), quoteIdent(dialect, table)), nil //nolint:gosec // identifiers come from database metadata and are quoted.
	case DialectPostgres:
		if schema == "" {
			schema = "public"
		}
		return fmt.Sprintf("SELECT %s FROM %s.%s", strings.Join(quotedCols, ", "), quoteIdent(dialect, schema), quoteIdent(dialect, table)), nil //nolint:gosec // identifiers come from database metadata and are quoted.
	default:
		return "", fmt.Errorf("unsupported dialect %s", dialect)
	}
}

func quoteIdent(_ Dialect, s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func normalizeLogicalValue(value any) any {
	switch v := value.(type) {
	case []byte:
		if json.Valid(v) {
			var decoded any
			if err := json.Unmarshal(v, &decoded); err == nil {
				return decoded
			}
		}
		if isPrintableUTF8(v) {
			return string(v)
		}
		return map[string]string{"encoding": "base64", "value": hex.EncodeToString(v)}
	case time.Time:
		return v.UTC().Format(time.RFC3339Nano)
	default:
		return v
	}
}

func archiveJoin(elem ...string) string {
	clean := make([]string, 0, len(elem))
	for _, part := range elem {
		if part != "" {
			clean = append(clean, strings.Trim(part, "/"))
		}
	}
	return strings.Join(clean, "/")
}

func scopeInManifest(scopes []backup.Scope, scope backup.Scope) bool {
	for _, existing := range scopes {
		if existing == scope {
			return true
		}
	}
	return false
}

func backupScopeSelected(scope backup.Scope, opts PlatformBackupExportOptions) bool {
	if opts.OrgSlug == "" {
		return true
	}
	if scope.Kind == "platform" {
		return true
	}
	if scope.OrgSlug != opts.OrgSlug {
		return false
	}
	if opts.TeamSlug != "" {
		return scope.Kind == "org" || (scope.Kind == "team" && scope.TeamSlug == opts.TeamSlug)
	}
	if opts.UserID != "" {
		return scope.Kind == "org" || (scope.Kind == "personal" && scope.UserID == opts.UserID)
	}
	return true
}

func sortedManifestScopes(scopes []backup.Scope) []backup.Scope {
	out := append([]backup.Scope(nil), scopes...)
	sort.SliceStable(out, func(i, j int) bool {
		return backupScopeKey(out[i]) < backupScopeKey(out[j])
	})
	return out
}
