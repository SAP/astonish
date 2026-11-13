package entstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/SAP/astonish/pkg/store"
)

// botMigrationVersion tracks DDL independently of immutable BotVersion JSON.
const botMigrationVersion = 2

// migrateBotSchema wraps scoped Ent reconciliation with a monotonic Bot schema
// marker. SQL here manages migration metadata only, never application/audit data.
// The callback owns Ent's dialect-specific additive migration options. A failed
// callback is never stamped; rerunning resumes Ent's idempotent reconciliation.
func migrateBotSchema(ctx context.Context, db *sql.DB, d Dialect, create func() error) error {
	var exists bool
	switch d {
	case DialectSQLite:
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'bot_schema_version'").Scan(&count); err != nil {
			return fmt.Errorf("inspect bot migration metadata: %w", err)
		}
		exists = count > 0
	case DialectPostgres:
		if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'bot_schema_version')").Scan(&exists); err != nil {
			return fmt.Errorf("inspect bot migration metadata: %w", err)
		}
	default:
		return store.ErrBotUnsupported
	}
	if exists {
		var version int
		err := db.QueryRowContext(ctx, "SELECT version FROM bot_schema_version WHERE id = 'bots'").Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read bot migration metadata: %w", err)
		}
		if version > botMigrationVersion {
			return fmt.Errorf("%w: database bot schema %d exceeds supported %d", store.ErrBotUnsupported, version, botMigrationVersion)
		}
	}
	if err := create(); err != nil {
		return fmt.Errorf("migrate bot schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS bot_schema_version (id TEXT PRIMARY KEY, version INTEGER NOT NULL)"); err != nil {
		return fmt.Errorf("create bot migration metadata: %w", err)
	}
	// Both drivers accept $1, and the conditional update cannot downgrade a
	// concurrently installed newer marker.
	result, err := db.ExecContext(ctx, "INSERT INTO bot_schema_version (id,version) VALUES ('bots',$1) ON CONFLICT (id) DO UPDATE SET version = excluded.version WHERE bot_schema_version.version <= excluded.version", botMigrationVersion)
	if err != nil {
		return fmt.Errorf("record bot migration version: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: concurrent newer bot migration", store.ErrBotUnsupported)
	}
	return nil
}
