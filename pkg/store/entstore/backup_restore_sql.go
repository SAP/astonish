package entstore

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

func restoreLogicalEntries(ctx context.Context, db *sql.DB, dialect Dialect, schema string, targetScope backup.Scope, files map[string][]byte, entries []backup.Entry, opts PlatformRestoreOptions, result *backup.RestoreResult) error {
	ordered := append([]backup.Entry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return restoreTablePriority(ordered[i].Entity) < restoreTablePriority(ordered[j].Entity)
	})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, entry := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		action, _ := restoreActionForEntry(entry, opts)
		if action == "skip" {
			result.SkippedEntries++
			continue
		}
		data, ok := files[entry.Path]
		if !ok {
			return fmt.Errorf("archive missing %s", entry.Path)
		}
		records, err := restoreLogicalEntry(ctx, tx, dialect, schema, targetScope, entry, data, action, opts)
		if err != nil {
			return err
		}
		result.RestoredEntries++
		result.RestoredRecords += records
	}
	if dialect == DialectSQLite {
		if err := sqliteForeignKeyCheck(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func restoreLogicalEntry(ctx context.Context, tx *sql.Tx, dialect Dialect, schema string, targetScope backup.Scope, entry backup.Entry, data []byte, action string, opts PlatformRestoreOptions) (int64, error) {
	scanner := backup.NewRecordScanner(strings.NewReader(string(data)), entry.Entity)
	var count int64
	for scanner.Next() {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		record, err := scanner.Record()
		if err != nil {
			return 0, err
		}
		row, err := backup.DecodeRecordData(record)
		if err != nil {
			return 0, err
		}
		if action == "restore_disabled" && entry.Entity == "scheduled_jobs" {
			row["status"] = "paused"
		}
		row = remapRestoreRow(entry.Scope, targetScope, entry.Entity, row, opts)
		if err := quiesceRestoredBotRow(entry.Entity, row); err != nil {
			return 0, fmt.Errorf("quiesce %s record %s: %w", entry.Entity, record.ID, err)
		}
		if err := validateRestoredBotSkillRow(entry.Entity, row); err != nil {
			return 0, fmt.Errorf("validate %s record %s: %w", entry.Entity, record.ID, err)
		}
		if entry.Entity == "bot_skill_revisions" {
			table := quoteIdent(dialect, "bot_skills")
			if dialect == DialectPostgres {
				table = quoteIdent(dialect, schema) + "." + table
			}
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id="+placeholder(dialect, 1)+" AND bot_id="+placeholder(dialect, 2), normalizeRestoreValue(row["skill_id"]), normalizeRestoreValue(row["bot_id"])).Scan(&count); err != nil {
				return 0, err
			}
			if count != 1 {
				return 0, fmt.Errorf("skill revision references a missing or foreign Bot skill")
			}
		}
		if err := insertLogicalRow(ctx, tx, dialect, schema, entry.Entity, row); err != nil {
			return 0, fmt.Errorf("insert %s record %s: %w", entry.Entity, record.ID, err)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func validateRestoredBotSkillRow(table string, row map[string]any) error {
	if table != "bot_skills" && table != "bot_skill_revisions" {
		return nil
	}
	doc, err := restoredBotDocument(row["data"])
	if err != nil {
		return err
	}
	if table == "bot_skill_revisions" {
		var v store.BotSkillRevision
		if err := botDecode(doc, &v); err != nil {
			return err
		}
		if v.ID.String() != fmt.Sprint(row["id"]) || v.BotID.String() != fmt.Sprint(row["bot_id"]) || v.SkillID.String() != fmt.Sprint(row["skill_id"]) || fmt.Sprint(v.Revision) != fmt.Sprint(row["revision"]) || v.Digest != fmt.Sprint(row["digest"]) {
			return store.ErrBotInvalid
		}
		return v.Validate()
	}
	var v botStoredSkill
	if err := botDecode(doc, &v); err != nil {
		return err
	}
	if v.Skill.ID.String() != fmt.Sprint(row["id"]) || v.Skill.BotID.String() != fmt.Sprint(row["bot_id"]) || v.Skill.Key != fmt.Sprint(row["key"]) || fmt.Sprint(v.Skill.Revision) != fmt.Sprint(row["revision"]) || string(v.Skill.Status) != fmt.Sprint(row["status"]) {
		return store.ErrBotInvalid
	}
	return nil
}

// Restoring a control-plane archive is not consent to resume execution. These
// rules are unconditional, including --include-transient/--enable-scheduled-jobs.
// Both dialects apply them inside the scope import transaction.
func quiesceRestoredBotRow(table string, row map[string]any) error {
	const reason = "backup restore: held for reconciliation; external effects and physical cleanup are unverified"
	switch table {
	case "bots":
		if err := advanceRestoredBotCounter(row, "fence"); err != nil {
			return err
		}
		row["lease_expires_at"] = nil
		row["cleanup_complete"] = false
		// Keep active_run_id as a teardown barrier: import cannot prove that the
		// source deployment's container has stopped or that its home was recovered.
		if store.BotStatus(fmt.Sprint(row["status"])) == store.BotActive {
			row["status"] = string(store.BotPaused)
			if err := advanceRestoredBotCounter(row, "revision"); err != nil {
				return err
			}
		}
		data, err := restoredBotDocument(row["data"])
		if err != nil {
			return err
		}
		data["restore_reconciliation_required"] = true
		row["data"] = data
	case "bot_runs":
		if err := advanceRestoredBotCounter(row, "fence"); err != nil {
			return err
		}
		row["worker_id"], row["lease_expires_at"] = "", nil
		status := store.RunStatus(fmt.Sprint(row["status"]))
		switch status {
		case store.RunQueued, store.RunRunning, store.RunWaitingApproval:
			status = store.RunInterrupted
		case store.RunInterrupted, store.RunSucceeded, store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled:
		default:
			return fmt.Errorf("unknown Bot run status %q", status)
		}
		row["status"] = string(status)
		data, save, err := restoredBotStoredDocument(row["data"])
		if err != nil {
			return err
		}
		run, err := restoredBotDocument(data["Run"])
		if err != nil {
			return err
		}
		run["status"] = string(status)
		run["lease"] = map[string]any{"bot_id": row["bot_id"], "run_id": row["id"], "fence": row["fence"], "worker_id": "", "expires_at": time.Time{}.UTC().Format(time.RFC3339Nano)}
		if status == store.RunInterrupted {
			report, err := restoredBotDocument(run["report"])
			if err != nil {
				return err
			}
			unverified, _ := report["unverified"].([]any)
			report["unverified"] = append(unverified, reason)
			run["report"] = report
		}
		if rawWork, present := data["work"]; present && rawWork != nil {
			raw, err := json.Marshal(rawWork)
			if err != nil {
				return err
			}
			var work store.BotWorkState
			if err := json.Unmarshal(raw, &work); err != nil {
				return err
			}
			if err := work.Validate(); err != nil {
				return fmt.Errorf("restore Bot work: %w", err)
			}
			if status == store.RunInterrupted {
				interruptBotWork(&work, reason)
			}
			if err := work.Validate(); err != nil {
				return fmt.Errorf("restore interrupted Bot work: %w", err)
			}
			data["work"] = &work
			if work.Protocol == store.BotWorkProtocolAgentLedV1 {
				projection := store.BuildBotWorkProjection(&work)
				run["work"] = projection
				report, err := restoredBotDocument(run["report"])
				if err != nil {
					return err
				}
				if report["work"] != nil {
					report["work"] = projection
				}
				run["report"] = report
			}
		}
		data["Run"] = run
		row["data"], err = save()
		if err != nil {
			return err
		}
	case "bot_approvals":
		status := store.ApprovalStatus(fmt.Sprint(row["status"]))
		if status == store.ApprovalPending || status == store.ApprovalApproved {
			status = store.ApprovalRevoked
		}
		data, save, err := restoredBotStoredDocument(row["data"])
		if err != nil {
			return err
		}
		row["status"], data["status"] = string(status), string(status)
		row["data"], err = save()
		if err != nil {
			return err
		}
	case "bot_outbox":
		if err := advanceRestoredBotCounter(row, "fence"); err != nil {
			return err
		}
		row["worker_id"], row["lease_expires_at"] = "", nil
		if row["delivered_at"] == nil {
			// Preserve activity, payload, deduplication keys and receipts. Do not
			// invent a delivered receipt or erase uncertain dispatch/notify intents.
			row["available_at"] = "9999-12-31T23:59:59Z"
			row["last_error"] = reason
		}
	case "scheduled_jobs":
		if fmt.Sprint(row["mode"]) == "bot_trigger" {
			row["status"], row["next_run_at"] = "paused", nil
		}
	}
	return nil
}

// restoredBotStoredDocument preserves the precision-protecting bot_document
// envelope used by botJSONMap. Only the returned document may be edited; save
// writes it back without exposing counters to Ent's float64 JSON-map decoding.
func restoredBotStoredDocument(value any) (map[string]any, func() (map[string]any, error), error) {
	outer, err := restoredBotDocument(value)
	if err != nil {
		return nil, nil, err
	}
	encoded, wrapped := outer["bot_document"]
	if !wrapped {
		return outer, func() (map[string]any, error) { return outer, nil }, nil
	}
	text, ok := encoded.(string)
	if !ok {
		return nil, nil, fmt.Errorf("invalid bot_document envelope")
	}
	doc, err := restoredBotDocument(text)
	if err != nil {
		return nil, nil, err
	}
	return doc, func() (map[string]any, error) {
		encoded, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		outer["bot_document"] = string(encoded)
		return outer, nil
	}, nil
}

// JSON columns are text in SQLite exports and objects in PostgreSQL exports.
// Decode with UseNumber so counters and evidence are not rounded through float64.
func restoredBotDocument(value any) (map[string]any, error) {
	if doc, ok := value.(map[string]any); ok && doc != nil {
		return doc, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("missing Bot JSON object")
	}
	var doc map[string]any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("null Bot JSON object")
	}
	return doc, nil
}

func advanceRestoredBotCounter(row map[string]any, key string) error {
	value, err := strconv.ParseInt(fmt.Sprint(row[key]), 10, 64)
	if err != nil || value < 0 || value == 1<<63-1 {
		return fmt.Errorf("invalid Bot %s counter", key)
	}
	row[key] = value + 1
	return nil
}

func insertLogicalRow(ctx context.Context, tx *sql.Tx, dialect Dialect, schema, table string, row map[string]any) error {
	if len(row) == 0 {
		return nil
	}
	columns := make([]string, 0, len(row))
	for col := range row {
		columns = append(columns, col)
	}
	sort.Strings(columns)
	placeholders := make([]string, len(columns))
	args := make([]any, len(columns))
	for i, col := range columns {
		placeholders[i] = placeholder(dialect, i+1)
		args[i] = normalizeRestoreValue(row[col])
	}
	query, err := insertLogicalQuery(dialect, schema, table, columns, placeholders)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, query, args...)
	return err
}

func insertLogicalQuery(dialect Dialect, schema, table string, columns, placeholders []string) (string, error) {
	quotedColumns := make([]string, len(columns))
	for i, col := range columns {
		quotedColumns[i] = quoteIdent(dialect, col)
	}
	target := quoteIdent(dialect, table)
	if dialect == DialectPostgres {
		if schema == "" {
			schema = "public"
		}
		target = quoteIdent(dialect, schema) + "." + quoteIdent(dialect, table)
	}
	switch dialect {
	case DialectSQLite:
		return fmt.Sprintf("INSERT OR REPLACE INTO %s (%s) VALUES (%s)", target, strings.Join(quotedColumns, ", "), strings.Join(placeholders, ", ")), nil //nolint:gosec // identifiers come from verified archive metadata and are quoted; values are bound parameters.
	case DialectPostgres:
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING", target, strings.Join(quotedColumns, ", "), strings.Join(placeholders, ", ")), nil //nolint:gosec // identifiers come from verified archive metadata and are quoted; values are bound parameters.
	default:
		return "", fmt.Errorf("unsupported dialect %s", dialect)
	}
}

func placeholder(dialect Dialect, index int) string {
	if dialect == DialectPostgres {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func normalizeRestoreValue(value any) any {
	switch v := value.(type) {
	case nil, string, bool:
		return v
	case json.Number:
		if strings.Contains(v.String(), ".") {
			if f, err := v.Float64(); err == nil {
				return f
			}
		}
		if i, err := v.Int64(); err == nil {
			return i
		}
		return v.String()
	case map[string]any:
		if encoding, ok := v["encoding"].(string); ok && encoding == "base64" {
			if encoded, ok := v["value"].(string); ok {
				if decoded, err := hex.DecodeString(encoded); err == nil {
					return decoded
				}
			}
		}
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(data)
	case []any:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(data)
	default:
		return v
	}
}
