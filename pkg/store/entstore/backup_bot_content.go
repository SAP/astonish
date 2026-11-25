package entstore

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

// Restore and SQL-only export remain closed for all Bot content. File-first
// export/verification use their own typed SQL plus file closure validator.
func validateBotArchiveRow(table string, row map[string]any) error {
	if table == "bot_schema_version" {
		return nil
	}
	botOwned := table == "bots" || strings.HasPrefix(table, "bot_")
	if !botOwned {
		for _, key := range []string{"bot_id", "bot_run_id"} {
			if value := row[key]; value != nil && fmt.Sprint(value) != "" {
				botOwned = true
			}
		}
		botOwned = botOwned || row["mode"] == "bot_trigger"
	}
	if !botOwned {
		return nil
	}
	return fmt.Errorf("%w: unsupported development Bot backup format in %s; inline Bot archives cannot be migrated, and file-reference backup/restore is incomplete (SQL-only recovery is unsupported)", store.ErrBotUnsupported, table)
}

// VerifyPlatformBackup checks integrity and the supported content protocol without
// opening a target database. Checksums alone do not prove SQL/file closure.
func VerifyPlatformBackup(ctx context.Context, path string, opts backup.ReaderOptions) (backup.Summary, error) {
	if err := ctx.Err(); err != nil {
		return backup.Summary{}, err
	}
	summary, err := backup.Verify(path, opts)
	if err != nil {
		return backup.Summary{}, err
	}
	if err := verifyFileFirstPlatform(ctx, path, opts, summary); err != nil {
		return backup.Summary{}, err
	}
	if summary.Manifest.Mode != backupModeLogical {
		return backup.Summary{}, fmt.Errorf("unsupported platform backup mode %q", summary.Manifest.Mode)
	}
	return summary, ctx.Err()
}

// Run before target reset, provisioning or any imported row. Record counts in
// the manifest are informational; inspect actual records even when Records=0.
func validateBotContentArchive(ctx context.Context, path string, opts backup.ReaderOptions, summary backup.Summary) error {
	entries := make(map[string]backup.Entry, len(summary.Manifest.Entries))
	for _, entry := range summary.Manifest.Entries {
		entries[entry.Path] = entry
	}
	reader, err := backup.OpenReader(path, opts)
	if err != nil {
		return err
	}
	defer reader.Close()
	return reader.ForEachFile(func(path string, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, ok := entries[path]
		if !ok {
			return nil
		}
		if entry.Entity == botWorkspaceArchiveEntity || entry.Kind != "jsonl" {
			return validateBotArchiveRow(entry.Entity, map[string]any{})
		}
		scanner := backup.NewRecordScanner(r, entry.Entity)
		for scanner.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, err := scanner.Record()
			if err != nil {
				return err
			}
			row, err := backup.DecodeRecordData(record)
			if err != nil {
				return err
			}
			if err := validateBotArchiveRow(entry.Entity, row); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
		}
		return scanner.Err()
	})
}
