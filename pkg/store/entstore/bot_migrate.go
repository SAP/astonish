package entstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/store"
)

// botMigrationVersion tracks DDL independently of Bot content codecs. Marker 5
// adds indexed transcript event identity; it does not certify or convert inline
// development content, which remains rejected without blocking ordinary Chat.
const botMigrationVersion = 5

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
	if err := backfillBotSessionEventIDs(ctx, db); err != nil {
		return fmt.Errorf("backfill Bot transcript event identity: %w", err)
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

// backfillBotSessionEventIDs reads only the small SQL metadata envelope. It never
// opens retained Home content. Ordinary Chat rows have no bot_record_v2 envelope
// and intentionally keep a NULL event_id.
func backfillBotSessionEventIDs(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "SELECT id,session_id,event_data FROM session_events WHERE event_id IS NULL ORDER BY id")
	if err != nil && (strings.Contains(strings.ToLower(err.Error()), "no such table") || strings.Contains(strings.ToLower(err.Error()), "does not exist")) {
		// Compatibility fixtures may intentionally reconcile only migration
		// metadata. A real scoped database gets session_events from Ent first.
		return nil
	}
	if err != nil {
		return err
	}
	type update struct {
		id        int64
		sessionID string
		eventID   string
	}
	var updates []update
	for rows.Next() {
		var id int64
		var sessionID string
		var raw []byte
		if err := rows.Scan(&id, &sessionID, &raw); err != nil {
			rows.Close()
			return err
		}
		var data map[string]any
		if json.Unmarshal(raw, &data) != nil {
			continue
		}
		value, ok := data["bot_record_v2"].(string)
		if !ok || len(data) != 1 {
			continue
		}
		var record struct {
			EventID string `json:"event_id"`
		}
		if json.Unmarshal([]byte(value), &record) != nil || strings.TrimSpace(record.EventID) == "" || len(record.EventID) > 256 {
			continue
		}
		updates = append(updates, update{id: id, sessionID: sessionID, eventID: record.EventID})
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	reserved := make(map[string]bool)
	existing, err := db.QueryContext(ctx, "SELECT session_id,event_id FROM session_events WHERE event_id IS NOT NULL")
	if err != nil {
		return err
	}
	for existing.Next() {
		var sessionID, eventID string
		if err := existing.Scan(&sessionID, &eventID); err != nil {
			existing.Close()
			return err
		}
		reserved[sessionID+"\x00"+eventID] = true
	}
	if err := errors.Join(existing.Err(), existing.Close()); err != nil {
		return err
	}
	for _, item := range updates {
		key := item.sessionID + "\x00" + item.eventID
		if reserved[key] {
			// Historical mirrored duplicates remain nullable legacy rows. Durable
			// replay still reads them; only one canonical row owns indexed identity.
			continue
		}
		if _, err := db.ExecContext(ctx, "UPDATE session_events SET event_id=$1 WHERE id=$2 AND event_id IS NULL", item.eventID, item.id); err != nil {
			return err
		}
		reserved[key] = true
	}
	return nil
}
