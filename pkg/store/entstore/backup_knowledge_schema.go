package entstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"github.com/SAP/astonish/pkg/backup"
	"sort"
	"strings"
)

// Reconstruct exactly marker 3, retaining Tasks and excluding only marker 4 tables.
func preKnowledgeBackupCompatible(ctx context.Context, db *sql.DB, dialect Dialect, manifest backup.Manifest, scope backup.Scope, archive backup.SchemaVersion) (bool, error) {
	excluded := func(t string) bool { return t == "bot_knowledge_extractions" || t == "bot_knowledge_file_heads" }
	for _, e := range manifest.Entries {
		if e.Scope == scope && excluded(e.Entity) {
			return false, nil
		}
	}
	schema := ""
	if dialect == DialectPostgres {
		schema = postgresSchemaForScope(scope)
	}
	tables, e := userTables(ctx, db, dialect, schema)
	if e != nil {
		return false, e
	}
	parts := []string{}
	for _, t := range tables {
		if excluded(t) {
			continue
		}
		var cols []string
		if dialect == DialectSQLite {
			cols, e = sqliteTableSchema(ctx, db, t)
		} else {
			cols, e = tableColumns(ctx, db, dialect, schema, t)
		}
		if e != nil {
			return false, e
		}
		parts = append(parts, t+"("+strings.Join(cols, ",")+")")
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:]) == archive.Hash, nil
}
