package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

var errFileFirstSQLInterrupted = errors.New("test SQL import interrupted after files")

// BotRestoreBackend reserves an independently disposable physical generation.
// Scope and immutable object identities do not change. Ordinary Ensure is NOT
// sufficient: its idempotent adoption could modify the source Home.
type BotRestoreBackend interface {
	BotBackupBackend
	CreateBotHomeRestoreTarget(context.Context, sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error)
}

// Concrete backends expose Kind through sandbox.Backend. Lightweight scoped
// adapters may omit it: they still must validate storage ownership on restore
// and deletion, and the importer compares the reserved and returned binding.
func restoreBackendAcceptsBinding(backend BotRestoreBackend, binding *sandbox.BotHomeBinding) bool {
	if binding == nil || binding.Validate() != nil {
		return false
	}
	if typed, ok := backend.(interface{ Kind() sandbox.BackendKind }); ok {
		return binding.Backend == typed.Kind()
	}
	return true
}

type fileFirstRestoreState struct {
	mapping      *fileFirstMapping
	files        map[string][]byte
	homes        map[store.BotID]fileFirstBot
	interruptSQL bool
}

func hasFileFirstArchive(m backup.Manifest) bool {
	for _, e := range m.Entries {
		if e.Entity == fileFirstScopeEntity {
			return true
		}
	}
	return false
}

// Freeze before verification: Reader reopens its path between passes. Neither a
// replaced input nor a failed validation may change the target.
func (s *Store) restoreFileFirstBackup(ctx context.Context, source string, opts PlatformRestoreOptions) (result *backup.RestoreResult, retErr error) {
	dir, err := os.MkdirTemp("", "astonish-platform-restore-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	snapshot := filepath.Join(dir, "archive")
	if err = snapshotBotFileArchive(ctx, source, snapshot); err != nil {
		return nil, err
	}
	plan, err := s.PlanPlatformRestore(ctx, snapshot, opts)
	if err != nil {
		return nil, err
	}
	if len(plan.Blockers) != 0 {
		return nil, fmt.Errorf("restore blocked: %s", plan.Blockers[0])
	}
	if opts.DryRun {
		return &backup.RestoreResult{Plan: *plan, Warnings: plan.Warnings}, nil
	}
	lock, err := lockFileFirstRestore(s.dataDir)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	// The writer lock protects the empty-target check and file installation. The
	// platform SQL import transaction rechecks emptiness before inserting rows.
	conn, err := s.platformDB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err = beginFileFirstPlatformExclusion(ctx, conn, s.dialect); err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	for _, table := range []string{"users", "organizations"} {
		var n int
		if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return nil, err
		}
		if n != 0 {
			return nil, store.ErrBotConflict
		}
	}
	// Reject leftover tenant databases even when platform rows are empty. Import
	// must never merge a restored scope into an unregistered previous database.
	if children, err := os.ReadDir(filepath.Join(s.dataDir, "orgs")); err == nil {
		if len(children) != 0 {
			return nil, fmt.Errorf("restore requires empty tenant root: %w", store.ErrBotConflict)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Reserve the whole staging root. A prior interrupted import is never adopted.
	contentDir := filepath.Join(s.dataDir, "bot-content")
	// Identity mappings still scrub imported authority and track logical digests.
	state := &fileFirstRestoreState{files: map[string][]byte{}, homes: map[store.BotID]fileFirstBot{}, mapping: newFileFirstMapping()}
	descriptors := map[backup.Scope]fileFirstScopeArchive{}
	spooled := map[string]string{}
	reader, err := backup.OpenReader(snapshot, backup.ReaderOptions{Passphrase: opts.Passphrase})
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	entries := map[string]backup.Entry{}
	for _, e := range plan.Archive.Manifest.Entries {
		entries[e.Path] = e
	}
	err = reader.ForEachFile(func(p string, r io.Reader) error {
		e, ok := entries[p]
		if !ok {
			return nil
		}
		switch e.Entity {
		case fileFirstScopeEntity:
			var d fileFirstScopeArchive
			if err := botBackupDecode(io.LimitReader(r, backup.MaxMetadataBytes+1), &d); err != nil {
				return err
			}
			descriptors[e.Scope] = d
		case fileFirstHomeEntity, fileFirstContentEntity:
			f, err := os.CreateTemp(dir, "object-*")
			if err != nil {
				return err
			}
			_, copyErr := io.CopyBuffer(f, botBackupContextReader{ctx, r}, make([]byte, 32<<10))
			if err = errors.Join(copyErr, f.Close()); err != nil {
				return err
			}
			spooled[p] = f.Name()
		default:
			if e.Kind != "jsonl" {
				return nil
			}
			data, err := io.ReadAll(botBackupContextReader{ctx, r})
			if err != nil {
				return err
			}
			state.files[p] = data
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// All authority conditions are checked before provisioning any Home.
	for _, e := range plan.Archive.Manifest.Entries {
		if e.Kind != "jsonl" {
			continue
		}
		scan := backup.NewRecordScanner(strings.NewReader(string(state.files[e.Path])), e.Entity)
		for scan.Next() {
			rec, err := scan.Record()
			if err != nil {
				return nil, err
			}
			row, err := backup.DecodeRecordData(rec)
			if err != nil {
				return nil, err
			}
			if err = preflightFileFirstRestoreRow(e.Entity, row); err != nil {
				return nil, err
			}
		}
		if err = scan.Err(); err != nil {
			return nil, err
		}
	}
	// The SQL row hook is UUID-addressed. Until it carries an archive scope,
	// reject ambiguous identities before creating any physical or SQL target.
	sourceHomes := map[store.BotID]sandbox.BotHomeBinding{}
	sourceRefs := map[string]bool{}
	seenBots := map[store.BotID]backup.Scope{}
	for scope, d := range descriptors {
		for _, h := range d.Homes {
			id := h.Bot.Bot.ID
			if prior, ok := seenBots[id]; ok {
				return nil, fmt.Errorf("duplicate Bot identity %s across scopes %v and %v: %w", id, prior, scope, store.ErrBotUnsupported)
			}
			seenBots[id] = scope
			sourceHomes[id] = h.Bot.Selection.Binding
			sourceRefs[h.Bot.Selection.Binding.Ref] = true
		}
	}
	backend, err := opts.FileBackend(ctx)
	if err != nil {
		return nil, err
	}
	if backend == nil {
		return nil, store.ErrBotUnsupported
	}
	if err = os.MkdirAll(contentDir, 0700); err != nil {
		return nil, err
	}
	children, err := os.ReadDir(contentDir)
	if err != nil {
		return nil, err
	}
	if len(children) != 0 {
		return nil, fmt.Errorf("restore requires empty content root: %w", store.ErrBotConflict)
	}
	journalBackend, ok := backend.(journaledBotRestoreBackend)
	if !ok {
		return nil, fmt.Errorf("restore requires journaled allocation and exact cleanup: %w", store.ErrBotUnsupported)
	}
	root, journal, err := newRestoreJournal(ctx, s, snapshot, plan.Archive.Manifest.Scopes, opts)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	defer func() {
		if retErr != nil {
			if journal.Phase == restoreCommitted {
				retErr = fmt.Errorf("%w; platform SQL committed: preserve recovery journal %s and do not run cleanup", retErr, filepath.Join(s.dataDir, restoreJournalDir, "journal.json"))
				return
			}
			if journal.Phase != restoreCommitted {
				journal.Phase = restoreFailed
				journal.Failure = retErr.Error()
				retErr = errors.Join(retErr, writeRestoreJournal(root, journal))
			}
			retErr = fmt.Errorf("%w; recovery journal %s: stop target writers, then run astonish platform restore cleanup <same-archive> --confirm before retrying", retErr, filepath.Join(s.dataDir, restoreJournalDir, "journal.json"))
		}
	}()
	if err = restoreMarker(root, "bot-content/.restore-reservation", journal.ID); err != nil {
		return nil, err
	}
	for _, d := range descriptors {
		originalScope := d.Scope
		d, err = mapFileFirstDescriptor(d, opts, state.mapping)
		if err != nil {
			return nil, err
		}
		for _, h := range d.Homes {
			b := h.Bot
			reserved := false
			target, err := journalBackend.CreateBotHomeRestoreTargetJournaled(ctx, b.Selection.Binding.Scope, func(candidate *sandbox.BotHomeBinding) error {
				if reserved || candidate == nil || candidate.Validate() != nil || candidate.Scope != b.Selection.Binding.Scope || !restoreBackendAcceptsBinding(backend, candidate) || sourceRefs[candidate.Ref] {
					return store.ErrBotConflict
				}
				reserved = true
				journal.Homes = append(journal.Homes, restoreJournalHome{Source: sourceHomes[b.Bot.ID], Target: *candidate})
				return writeRestoreJournal(root, journal)
			})
			if err != nil {
				return nil, err
			}
			if !reserved || target == nil || journal.Homes[len(journal.Homes)-1].Target != *target {
				return nil, store.ErrBotConflict
			}
			journal.Homes[len(journal.Homes)-1].Allocated = true
			if err = writeRestoreJournal(root, journal); err != nil {
				return nil, err
			}
			if target == nil || target.Validate() != nil || target.Scope != b.Selection.Binding.Scope || target.Ref == b.Selection.Binding.Ref {
				return nil, store.ErrBotConflict
			}
			barrier := func(live context.Context, exact *sandbox.BotHomeBinding, op func(context.Context) error) error {
				if exact == nil || *exact != *target || !locked {
					return store.ErrBotConflict
				}
				return op(live)
			}
			f, err := os.Open(spooled[h.Path])
			if err != nil {
				return nil, err
			}
			err = errors.Join(backend.RestoreBotHomeArchive(ctx, target, &h.Manifest, f, barrier), f.Close())
			if err != nil {
				return nil, fmt.Errorf("restore Home %s: %w", b.Bot.ID, err)
			}
			// Historical publications retain logical Home identity. A new physical
			// generation and incremented revision fence the old compute binding.
			b.Bot.Home.Revision++
			b.Bot.Home.StorageKey = target.Ref
			b.Selection = botContentHomeSelection{Home: b.Bot.Home, Binding: *target}
			state.homes[b.Bot.ID] = b
		}
		err = mapFileFirstContent(ctx, spooled[d.ContentPath], contentDir, dir, state, backend, originalScope, opts)
		if err != nil {
			return nil, fmt.Errorf("install content %s: %w", d.ContentPath, err)
		}
	}
	// Installation and target readback succeeded. Opaque content remains exact;
	// typed references and logical digests reflect scrubbed authority.
	state.interruptSQL = opts.interruptAfterFiles
	opts.fileFirst = state
	// The logical importer owns a platform transaction. The exclusive staging root
	// is the cross-import reservation; SQL's empty-target check remains mandatory.
	if _, err = conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		return nil, err
	}
	locked = false
	if err = conn.Close(); err != nil {
		return nil, err
	}
	journal.Phase = restoreFilesReady
	if err = writeRestoreJournal(root, journal); err != nil {
		return nil, err
	}
	if s.dialect == DialectPostgres {
		stage := &receiptPostgresStage{s: s, root: root, journal: journal, plan: *plan}
		return s.importFileFirstPostgresStaged(ctx, opts, *plan, root, journal, stage)
	}
	return s.importFileFirstStaged(ctx, snapshot, opts, *plan, root, journal)
}

func compareFileFirstStreams(ctx context.Context, a, b io.Reader, size int64) error {
	left, right := make([]byte, 32<<10), make([]byte, 32<<10)
	for size > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := int64(len(left))
		if n > size {
			n = size
		}
		if _, err := io.ReadFull(a, left[:n]); err != nil {
			return err
		}
		if _, err := io.ReadFull(b, right[:n]); err != nil {
			return err
		}
		for i := int64(0); i < n; i++ {
			if left[i] != right[i] {
				return store.ErrBotInvalid
			}
		}
		size -= n
	}
	// Join the backend's EOF/exit acknowledgement rather than cancelling its
	// still-running Docker attachment immediately after the last expected byte.
	var extra [1]byte
	for _, r := range []io.Reader{a, b} {
		if n, err := io.ReadFull(r, extra[:]); n != 0 || err != io.EOF {
			return errors.Join(store.ErrBotInvalid, err)
		}
	}
	return nil
}
func preflightFileFirstRestoreRow(table string, row map[string]any) error {
	switch table {
	case "bot_approvals":
		if s := store.ApprovalStatus(backupString(row, "status")); s == store.ApprovalPending || s == store.ApprovalApproved {
			return fmt.Errorf("restore of live approval authority is unsupported: %w", store.ErrBotUnsupported)
		}
	case "bot_task_revisions":
		s := store.BotTaskPublicationState(backupString(row, "publication_state"))
		if s == store.BotTaskPublicationPrepared || s == store.BotTaskPublicationFilesReady {
			return fmt.Errorf("restore requires settled task publication: %w", store.ErrBotUnsupported)
		}
	}
	return nil
}
func quiesceFileFirstRestoreRow(table string, row map[string]any, state *fileFirstRestoreState) error {
	switch table {
	case "bots":
		b, ok := state.homes[backupUUID(row, "id")]
		if !ok {
			return store.ErrBotInvalid
		}
		row["status"] = string(store.BotPaused)
		row["active_run_id"] = nil
		row["lease_expires_at"] = nil
		if err := advanceRestoredBotCounter(row, "fence"); err != nil {
			return err
		}
		if err := advanceRestoredBotCounter(row, "revision"); err != nil {
			return err
		}
		metadata, err := backupDocument(row, "data")
		if err != nil {
			return err
		}
		metadata[botContentHomeSelectionKey] = b.Selection
		metadata["restore_reconciliation_required"] = true
		row["data"] = metadata
		rawHome, err := json.Marshal(b.Bot.Home)
		if err != nil {
			return err
		}
		row["home_binding"] = string(rawHome)
	case "bot_runs":
		// Only terminal runs pass archive verification. Typed content and its
		// SQL envelope have already had admission/lease authority scrubbed.
		row["worker_id"] = ""
		row["lease_expires_at"] = nil
		return advanceRestoredBotCounter(row, "fence")
	case "bot_outbox":
		if row["delivered_at"] == nil {
			row["delivered_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		}
	case "scheduled_jobs":
		if row["mode"] == "bot_trigger" {
			row["status"] = "paused"
		}
	}
	return nil
}
