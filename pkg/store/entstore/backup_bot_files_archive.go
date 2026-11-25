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
	"sync"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

const botBackupIndexPath = "bot-files/index-v1.json"
const botBackupObjectPrefix = "bot-files/objects/"
const botBackupFilesMode = "bot-immutable-files-v1"

type botBackupFileIndex struct {
	Format  int               `json:"format"`
	Roots   []botBackupObject `json:"roots"`
	Objects []botBackupObject `json:"objects"`
}

// Internal only: this produces an immutable-content archive, NOT a recoverable
// platform backup. The public SQL/restore guard must remain closed until mutable
// Home snapshots, SQL row closure, attachment fences and restore mapping exist.
type botFileBackupExportOptions struct {
	Scopes      []store.BotContentScope
	Compression backup.Compression
	// Fence must exclude all SQL/staging/Home writers and attachments in scope.
	Fence func(context.Context, func() error) error
	// Collect reads SQL envelope roots while the same fence is held. It must also
	// supply memory, initial-state, independently owned template/intake roots.
	Collect func(context.Context, *botBackupInventory) error
	// Open resolves the independently authorized exact scope, never a host path
	// from the ref. Existing BotContentStaging.Open and BotFiles.OpenContent fit.
	Open func(context.Context, store.BotContentRef) (*store.BotFileStream, error)
}

func botBackupObjectPath(o botBackupObject) string {
	// Path includes identity AND digest, retaining alias references to equal bytes.
	raw, _ := json.Marshal(o.Ref)
	return fmt.Sprintf("%s%x", botBackupObjectPrefix, sha256.Sum256(raw))
}

func exportBotFileContent(ctx context.Context, path string, opts botFileBackupExportOptions) (resultErr error) {
	if opts.Fence == nil || opts.Collect == nil || opts.Open == nil {
		return store.ErrBotUnsupported
	}
	inv, err := newBotBackupInventory(opts.Scopes)
	if err != nil {
		return err
	}
	// Publish only a verified complete file archive. Failure leaves an existing
	// target untouched and removes only this operation's private temporary file.
	f, err := os.CreateTemp(filepath.Dir(path), ".bot-files-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	if err = f.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	defer func() { _ = os.Remove(temporary) }()
	calls := 0
	var fenceMu sync.Mutex
	active := true
	var workErr error
	err = opts.Fence(ctx, func() error {
		fenceMu.Lock()
		defer fenceMu.Unlock()
		if !active {
			return store.ErrBotConflict
		}
		calls++
		if calls != 1 {
			workErr = store.ErrBotConflict
			return workErr
		}
		workErr = writeBotFileContent(ctx, temporary, inv, opts)
		return workErr
	})
	fenceMu.Lock()
	active = false
	fenceMu.Unlock()
	if err = errors.Join(err, workErr); err != nil {
		return err
	}
	if calls != 1 {
		return store.ErrBotConflict
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func writeBotFileContent(ctx context.Context, path string, inv *botBackupInventory, opts botFileBackupExportOptions) (resultErr error) {
	if err := opts.Collect(ctx, inv); err != nil {
		return err
	}
	roots := append([]botBackupObject(nil), inv.objects...)
	w, err := backup.Create(path, backup.WriterOptions{Compression: opts.Compression})
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, w.Close()) }()
	m := backup.NewManifest("file-content", botBackupFilesMode, []backup.Scope{{Kind: "platform"}})
	for n := 0; n < len(inv.objects); n++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		o := inv.objects[n]
		if err = exportBotBackupObject(ctx, w, filepath.Dir(path), inv, o, opts.Open); err != nil {
			return fmt.Errorf("export %s: %w", o.label(), err)
		}
		m.Entries = append(m.Entries, backup.Entry{Path: botBackupObjectPath(o), Kind: "file", Entity: "bot_content_object", Scope: backup.Scope{Kind: "platform"}})
	}
	index := botBackupFileIndex{Format: 1, Roots: roots, Objects: inv.objects}
	raw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if len(raw) > backup.MaxMetadataBytes {
		return store.ErrBotInvalid
	}
	if _, err = w.AddFileSized(ctx, botBackupIndexPath, int64(len(raw)), bytes.NewReader(raw)); err != nil {
		return err
	}
	m.Entries = append(m.Entries, backup.Entry{Path: botBackupIndexPath, Kind: "file", Entity: "bot_content_index", Scope: backup.Scope{Kind: "platform"}})
	if err = w.CloseWithManifest(m); err != nil {
		return err
	}
	return validateBotFileContentArchive(ctx, path, backup.ReaderOptions{}, opts.Scopes, roots)
}

// Copy to a private spool before decoding or publishing: decoder allocations are
// bounded for typed containers, while raw artifacts have no 16MiB ceiling.
func exportBotBackupObject(ctx context.Context, w *backup.Writer, directory string, inv *botBackupInventory, o botBackupObject, open func(context.Context, store.BotContentRef) (*store.BotFileStream, error)) (resultErr error) {
	source, err := open(ctx, o.Ref)
	if err != nil {
		return err
	}
	if source == nil || source.ReadCloser == nil {
		return store.ErrBotInvalid
	}
	// Close on cancellation also unblocks owned transport reads. Join before return.
	var closeOnce sync.Once
	var closeErr error
	closeSource := func() error { closeOnce.Do(func() { closeErr = source.Close() }); return closeErr }
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = closeSource(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
		resultErr = errors.Join(resultErr, closeSource())
	}()
	f, err := os.CreateTemp(directory, ".bot-object-*")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, f.Close()); _ = os.Remove(f.Name()) }()
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(f, h), io.LimitReader(botBackupContextReader{ctx, source}, o.Ref.SizeBytes), make([]byte, 32<<10))
	if err != nil {
		return err
	}
	var extra [1]byte
	more, endErr := io.ReadFull(botBackupContextReader{ctx, source}, extra[:])
	if n != o.Ref.SizeBytes || more != 0 || !errors.Is(endErr, io.EOF) || hex.EncodeToString(h.Sum(nil)) != o.Ref.SHA256 {
		return fmt.Errorf("Bot object size/digest mismatch: got %d want %d, extra %d: %w", n, o.Ref.SizeBytes, more, errors.Join(store.ErrBotInvalid, endErr))
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = inv.children(o, f); err != nil {
		return fmt.Errorf("decode Bot object (%s): %w", o.Codec, err)
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = w.AddFileSized(ctx, botBackupObjectPath(o), o.Ref.SizeBytes, f)
	return err
}

type botBackupContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r botBackupContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// validateBotFileContentArchive verifies both bytes and reachable typed closure.
// expectedRoots must come from the caller's independently validated SQL snapshot,
// not the index itself. It performs no imports/provisioning/filesystem extraction.
func validateBotFileContentArchive(ctx context.Context, path string, opts backup.ReaderOptions, scopes []store.BotContentScope, expectedRoots []botBackupObject) error {
	inv, err := newBotBackupInventory(scopes)
	if err != nil {
		return err
	}
	for _, root := range expectedRoots {
		if err = inv.add(root); err != nil {
			return err
		}
	}
	reader, err := backup.OpenReader(path, opts)
	if err != nil {
		return err
	}
	defer reader.Close()
	summary, err := reader.Summary()
	if err != nil {
		return err
	}
	if summary.Manifest.Mode != botBackupFilesMode {
		return store.ErrBotUnsupported
	}
	checks := make(map[string]backup.Checksum)
	for _, c := range summary.Checksums {
		if c.Validate() != nil {
			return store.ErrBotInvalid
		}
		if _, ok := checks[c.Path]; ok {
			return store.ErrBotInvalid
		}
		checks[c.Path] = c
	}
	var index botBackupFileIndex
	found := false
	if err = reader.ForEachFile(func(p string, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p != botBackupIndexPath {
			return nil
		}
		if found {
			return store.ErrBotInvalid
		}
		found = true
		raw, err := io.ReadAll(io.LimitReader(botBackupContextReader{ctx, r}, backup.MaxMetadataBytes+1))
		if err != nil {
			return err
		}
		if len(raw) > backup.MaxMetadataBytes {
			return store.ErrBotInvalid
		}
		c, ok := checks[p]
		sum := sha256.Sum256(raw)
		if !ok || c.Size != int64(len(raw)) || c.SHA256 != hex.EncodeToString(sum[:]) {
			return store.ErrBotInvalid
		}
		return botBackupDecode(bytes.NewReader(raw), &index)
	}); err != nil {
		return err
	}
	if !found || index.Format != 1 || len(index.Objects) > botBackupMaxObjects || !reflect.DeepEqual(index.Roots, inv.objects) {
		return store.ErrBotInvalid
	}
	declared := make(map[string]botBackupObject)
	identity := make(map[string]bool)
	for _, o := range index.Objects {
		if err = validateBotBackupObject(o); err != nil {
			return err
		}
		if !inv.allowed[o.Ref.Scope] {
			return store.ErrBotForbidden
		}
		key := botBackupIdentity(o.Ref)
		if identity[key] {
			return store.ErrBotConflict
		}
		identity[key] = true
		p := botBackupObjectPath(o)
		declared[p] = o
		c, ok := checks[p]
		if !ok || c.Size != o.Ref.SizeBytes || c.SHA256 != o.Ref.SHA256 {
			return store.ErrBotInvalid
		}
	}
	if len(checks) != len(declared)+1 || len(summary.Manifest.Entries) != len(checks) {
		return store.ErrBotInvalid
	}
	for _, e := range summary.Manifest.Entries {
		if _, ok := checks[e.Path]; !ok {
			return store.ErrBotInvalid
		}
	}
	seen := make(map[string]bool)
	err = reader.ForEachFile(func(p string, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == backup.ManifestPath || p == backup.ChecksumsPath || p == botBackupIndexPath {
			return nil
		}
		o, ok := declared[p]
		if !ok || seen[p] {
			return store.ErrBotInvalid
		}
		// Export is breadth-first with roots first. Reject orphan/inverted entries.
		if reached, ok := inv.seen[botBackupIdentity(o.Ref)]; !ok || reached != o {
			return store.ErrBotInvalid
		}
		seen[p] = true
		h := sha256.New()
		var content io.Reader = botBackupContextReader{ctx, r}
		if o.Codec != botBackupLeaf {
			content = io.LimitReader(content, o.Ref.SizeBytes+1)
		}
		count := &botContentCountingReader{reader: io.TeeReader(content, h)}
		if err := inv.children(o, count); err != nil {
			return err
		}
		if _, err := io.CopyBuffer(io.Discard, count, make([]byte, 32<<10)); err != nil {
			return err
		}
		if count.n != o.Ref.SizeBytes || hex.EncodeToString(h.Sum(nil)) != o.Ref.SHA256 {
			return store.ErrBotInvalid
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(declared) || len(inv.objects) != len(declared) {
		return store.ErrBotInvalid
	}
	for _, o := range inv.objects {
		if declared[botBackupObjectPath(o)] != o {
			return store.ErrBotInvalid
		}
	}
	return nil
}
