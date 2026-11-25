package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

const fileFirstScopeEntity = "bot_filefirst_scope_v1"
const fileFirstContentEntity = "bot_filefirst_content_v1"
const fileFirstHomeEntity = "bot_filefirst_home_v1"

type fileFirstHomeArchive struct {
	Bot      fileFirstBot                   `json:"bot"`
	Manifest sandbox.BotHomeArchiveManifest `json:"manifest"`
	Path     string                         `json:"path"`
}
type fileFirstScopeArchive struct {
	Format      int                    `json:"format"`
	Scope       backup.Scope           `json:"scope"`
	ContentPath string                 `json:"content_path"`
	Homes       []fileFirstHomeArchive `json:"homes"`
}

func exportFileFirstScope(ctx context.Context, w *backup.Writer, m *backup.Manifest, db logicalDB, opts PlatformBackupExportOptions) error {
	i := opts.fileInventory
	backend, err := opts.FileBackend(ctx)
	if err != nil {
		return err
	}
	if backend == nil {
		return store.ErrBotUnsupported
	}
	dir, err := os.MkdirTemp(opts.spoolDirectory, ".filefirst-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	descriptor := fileFirstScopeArchive{Format: 1, Scope: db.Scope, ContentPath: archiveJoin(db.ArchiveDir, "bot-files.archive")}
	open := func(ctx context.Context, ref store.BotContentRef) (*store.BotFileStream, error) {
		if !i.inventory.allowed[ref.Scope] {
			return nil, store.ErrBotForbidden
		}
		if strings.HasPrefix(ref.Key, "staged-v1/") {
			disk, err := newBotDiskContent(opts.contentDirectory, ref.Scope)
			if err != nil {
				return nil, fmt.Errorf("stage directory %q: %w", opts.contentDirectory, err)
			}
			stream, openErr := disk.Open(ctx, ref, store.BotFileRange{})
			closeErr := disk.Close()
			if err = errors.Join(openErr, closeErr); err != nil {
				if stream != nil {
					_ = stream.Close()
				}
				return nil, fmt.Errorf("stage open: %w", err)
			}
			return stream, nil
		}
		b, ok := i.bots[ref.Scope.BotID]
		if !ok {
			return nil, store.ErrBotForbidden
		}
		expected := store.BotContentScope{OrgID: b.Selection.Binding.Scope.OrgID, TeamID: b.Selection.Binding.Scope.TeamID, OwnerID: b.Bot.OwnerID.String(), BotID: b.Bot.ID}
		if ref.Scope != expected {
			return nil, store.ErrBotForbidden
		}
		binding := b.Selection.Binding
		return backend.OpenBotContent(ctx, &binding, ref, store.BotFileRange{})
	}
	roots := append([]botBackupObject(nil), i.inventory.objects...)
	scopes := make([]store.BotContentScope, 0, len(i.inventory.allowed))
	for scope := range i.inventory.allowed {
		scopes = append(scopes, scope)
	}
	inventory, err := newBotBackupInventory(scopes)
	if err != nil {
		return err
	}
	contentPath := filepath.Join(dir, "content.archive")
	// This internal writer runs inside the production BEGIN IMMEDIATE transaction;
	// there is deliberately no user-provided replacement fence here.
	err = writeBotFileContent(ctx, contentPath, inventory, botFileBackupExportOptions{Scopes: scopes, Compression: backup.CompressionNone, Open: open, Collect: func(ctx context.Context, inv *botBackupInventory) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, r := range roots {
			if err := inv.add(r); err != nil {
				return err
			}
		}
		return nil
	}})
	if err != nil {
		return err
	}
	// Production codecs additionally validate logical IDs/digests, not just refs.
	codec := &botContentCodec{Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
		b, ok := i.bots[id]
		if !ok {
			return nil, store.ErrBotForbidden
		}
		disk, err := newBotDiskContent(opts.contentDirectory, b.Stage)
		if err != nil {
			return nil, err
		}
		home, err := newBotHomeContent(disk, b.Stage, b.Bot, b.Selection.Binding, backend, func(func() error) error { return store.ErrBotForbidden })
		if err != nil {
			_ = disk.Close()
		}
		return home, err
	}}
	for id, records := range i.records {
		for _, r := range records {
			if err = validateHomeRecord(ctx, codec, id, r); err != nil {
				return err
			}
		}
	}
	for _, row := range i.rows {
		if row.table != "bot_templates" && row.table != "bot_intakes" {
			continue
		}
		field := "definition"
		if row.table == "bot_intakes" {
			field = "content"
		}
		if row.row[field] == nil && row.row["discarded"] == true {
			continue
		}
		data, err := backupDocument(row.row, field)
		if err != nil {
			return err
		}
		var record botIndependentRecord
		if err = decodeBotRecord(data, &record); err != nil {
			return err
		}
		stream, err := open(ctx, record.Content)
		if err != nil {
			return err
		}
		var decodeErr error
		if row.table == "bot_templates" {
			var body store.BotTemplateDefinition
			decodeErr = botBackupDecode(io.LimitReader(stream, botBackupMaxTypedBytes+1), &body)
			if decodeErr == nil {
				decodeErr = validateBotTemplate(body)
			}
			if decodeErr == nil && body.Name != backupString(row.row, "name") {
				decodeErr = store.ErrBotInvalid
			}
		} else {
			var body store.BotUnfinishedContent
			decodeErr = botBackupDecode(io.LimitReader(stream, botBackupMaxTypedBytes+1), &body)
		}
		if err = errors.Join(decodeErr, stream.Close()); err != nil {
			return err
		}
	}
	if err = addFileFirstFile(ctx, w, m, descriptor.ContentPath, fileFirstContentEntity, db.Scope, contentPath); err != nil {
		return err
	}
	for _, b := range i.orderedBots() {
		homeFile, err := os.CreateTemp(dir, "home-*")
		if err != nil {
			return err
		}
		binding := b.Selection.Binding
		// BEGIN IMMEDIATE is held by db.DB for the entire export. Checking through
		// this same connection proves the exact root is still selected; no re-entry
		// into the outer Ent repository (or new transaction) is permitted.
		barrier := func(live context.Context, exact *sandbox.BotHomeBinding, body func(context.Context) error) error {
			if exact == nil || *exact != binding || live.Err() != nil {
				return store.ErrBotConflict
			}
			rows, err := db.DB.QueryContext(live, "SELECT revision, status, active_run_id, lease_expires_at FROM bots WHERE id = ?", b.Bot.ID.String())
			if err != nil {
				return err
			}
			var revision int64
			var status string
			var active, lease any
			if !rows.Next() {
				_ = rows.Close()
				return store.ErrBotConflict
			}
			err = rows.Scan(&revision, &status, &active, &lease)
			err = errors.Join(err, rows.Close())
			if err != nil {
				return err
			}
			if revision != b.Bot.Revision || status != string(b.Bot.Status) || active != nil || lease != nil {
				return store.ErrBotConflict
			}
			return body(live)
		}
		manifest, exportErr := backend.ExportBotHomeArchive(ctx, &binding, homeFile, barrier)
		if exportErr == nil && (manifest == nil || manifest.Validate() != nil || manifest.Binding != binding.Scope.Key()) {
			exportErr = store.ErrBotInvalid
		}
		if exportErr == nil {
			_, exportErr = homeFile.Seek(0, io.SeekStart)
		}
		if exportErr == nil {
			exportErr = verifyFileFirstHome(ctx, manifest, homeFile)
		}
		closeErr := homeFile.Close()
		if err = errors.Join(exportErr, closeErr); err != nil {
			return err
		}
		p := archiveJoin(db.ArchiveDir, "bot-homes", b.Bot.ID.String()+".stream")
		if err = addFileFirstFile(ctx, w, m, p, fileFirstHomeEntity, db.Scope, homeFile.Name()); err != nil {
			return err
		}
		descriptor.Homes = append(descriptor.Homes, fileFirstHomeArchive{Bot: b, Manifest: *manifest, Path: p})
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	if len(raw) > backup.MaxMetadataBytes {
		return store.ErrBotInvalid
	}
	p := archiveJoin(db.ArchiveDir, "bot-files-index.json")
	if _, err = w.AddFileSized(ctx, p, int64(len(raw)), bytes.NewReader(raw)); err != nil {
		return err
	}
	m.Entries = append(m.Entries, backup.Entry{Path: p, Kind: "file", Scope: db.Scope, Entity: fileFirstScopeEntity})
	return nil
}
func addFileFirstFile(ctx context.Context, w *backup.Writer, m *backup.Manifest, p, entity string, scope backup.Scope, source string) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if _, err = w.AddFileSized(ctx, p, info.Size(), f); err != nil {
		return err
	}
	m.Entries = append(m.Entries, backup.Entry{Path: p, Kind: "file", Scope: scope, Entity: entity})
	return nil
}
func verifyFileFirstHome(ctx context.Context, m *sandbox.BotHomeArchiveManifest, r io.Reader) error {
	if err := m.Validate(); err != nil {
		return err
	}
	for _, e := range m.Entries {
		if e.Directory {
			continue
		}
		h := sha256.New()
		n, err := io.CopyBuffer(h, io.LimitReader(botBackupContextReader{ctx, r}, e.Size), make([]byte, 32<<10))
		if err != nil {
			return err
		}
		if n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
			return store.ErrBotInvalid
		}
	}
	var extra [1]byte
	n, err := io.ReadFull(r, extra[:])
	if n != 0 || !errors.Is(err, io.EOF) {
		return store.ErrBotInvalid
	}
	return ctx.Err()
}

// Verification performs bounded disk spooling, never a whole-Home ReadAll. Its
// SQL gate is separate from restore, which continues to reject all Bot imports.
func verifyFileFirstPlatform(ctx context.Context, path string, opts backup.ReaderOptions, summary backup.Summary) error {
	byScope := map[backup.Scope]*fileFirstSQLInventory{}
	descriptors := map[backup.Scope]fileFirstScopeArchive{}
	entries := map[string]backup.Entry{}
	for _, e := range summary.Manifest.Entries {
		entries[e.Path] = e
		if e.Entity == fileFirstScopeEntity {
			if byScope[e.Scope] != nil {
				return store.ErrBotInvalid
			}
			byScope[e.Scope] = newFileFirstSQLInventory(e.Scope)
		}
	}
	if len(byScope) == 0 {
		return validateBotContentArchive(ctx, path, opts, summary)
	}
	if summary.Manifest.Backend != "sqlite" {
		return store.ErrBotUnsupported
	}
	reader, err := backup.OpenReader(path, opts)
	if err != nil {
		return err
	}
	defer reader.Close()
	err = reader.ForEachFile(func(p string, r io.Reader) error {
		e, ok := entries[p]
		if !ok {
			return nil
		}
		inv := byScope[e.Scope]
		if e.Entity == fileFirstScopeEntity {
			raw, err := io.ReadAll(io.LimitReader(botBackupContextReader{ctx, r}, backup.MaxMetadataBytes+1))
			if err != nil {
				return err
			}
			if len(raw) > backup.MaxMetadataBytes {
				return store.ErrBotInvalid
			}
			var d fileFirstScopeArchive
			if err = botBackupDecode(bytes.NewReader(raw), &d); err != nil {
				return err
			}
			if d.Format != 1 || d.Scope != e.Scope {
				return store.ErrBotInvalid
			}
			descriptors[e.Scope] = d
			return nil
		}
		if e.Kind != "jsonl" {
			if inv == nil || (e.Entity != fileFirstHomeEntity && e.Entity != fileFirstContentEntity) {
				return validateBotArchiveRow(e.Entity, map[string]any{})
			}
			return nil
		}
		scanner := backup.NewRecordScanner(r, e.Entity)
		live := ctx
		if inv != nil {
			live = context.WithValue(ctx, fileFirstInventoryKey{}, inv)
		}
		for scanner.Next() {
			record, err := scanner.Record()
			if err != nil {
				return err
			}
			row, err := backup.DecodeRecordData(record)
			if err != nil {
				return err
			}
			if err = validateBotExportRow(live, e.Entity, row); err != nil {
				return err
			}
		}
		return scanner.Err()
	})
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for scope, inv := range byScope {
		if err = inv.finish(); err != nil {
			return err
		}
		if inv.inventory == nil {
			return store.ErrBotInvalid
		}
		d, ok := descriptors[scope]
		if !ok || len(d.Homes) != len(inv.bots) {
			return store.ErrBotInvalid
		}
		e, ok := entries[d.ContentPath]
		if !ok || e.Scope != scope || e.Entity != fileFirstContentEntity || expected[d.ContentPath] {
			return store.ErrBotInvalid
		}
		expected[d.ContentPath] = true
		seen := map[store.BotID]bool{}
		for _, h := range d.Homes {
			b, ok := inv.bots[h.Bot.Bot.ID]
			if !ok || seen[b.Bot.ID] || !reflect.DeepEqual(b, h.Bot) || h.Manifest.Validate() != nil || h.Manifest.Binding != b.Selection.Binding.Scope.Key() {
				return store.ErrBotInvalid
			}
			seen[b.Bot.ID] = true
			e, ok := entries[h.Path]
			if !ok || e.Scope != scope || e.Entity != fileFirstHomeEntity || expected[h.Path] {
				return store.ErrBotInvalid
			}
			expected[h.Path] = true
		}
	}
	dir, err := os.MkdirTemp("", "astonish-backup-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	return reader.ForEachFile(func(p string, r io.Reader) error {
		e, ok := entries[p]
		if !ok {
			return nil
		}
		if e.Entity != fileFirstContentEntity && e.Entity != fileFirstHomeEntity {
			return nil
		}
		if !expected[p] {
			return store.ErrBotInvalid
		}
		inv, d := byScope[e.Scope], descriptors[e.Scope]
		if e.Entity == fileFirstHomeEntity {
			for _, h := range d.Homes {
				if h.Path == p {
					return verifyFileFirstHome(ctx, &h.Manifest, r)
				}
			}
			return store.ErrBotInvalid
		}
		f, err := os.CreateTemp(dir, "content-*")
		if err != nil {
			return err
		}
		_, copyErr := io.CopyBuffer(f, botBackupContextReader{ctx, r}, make([]byte, 32<<10))
		if err = errors.Join(copyErr, f.Close()); err != nil {
			return err
		}
		scopes := []store.BotContentScope{}
		for scope := range inv.inventory.allowed {
			scopes = append(scopes, scope)
		}
		if err = validateBotFileContentArchive(ctx, f.Name(), backup.ReaderOptions{}, scopes, inv.inventory.objects); err != nil {
			return fmt.Errorf("Bot content closure: %w", err)
		}
		return nil
	})
}
