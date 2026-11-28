package entstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

// botFileBackupRestoreOptions is deliberately internal. Targets are independently
// authorized repositories in a fresh control-plane root, NOT live Bot Homes.
// Exact scopes and staged-v1 identities are preserved. Retained content-v1 keys
// and cross-scope/SQL identity remapping require a separate typed rewrite protocol.
type botFileBackupRestoreOptions struct {
	Reader         backup.ReaderOptions
	SpoolDirectory string
	Scopes         []store.BotContentScope
	ExpectedRoots  []botBackupObject
	Targets        map[store.BotContentScope]*botDiskContent
}

// restoreBotFileContent validates a private snapshot before any destination write.
// Success returns the complete identity mapping only after durable installation
// and repository readback. It never changes SQL, removes sources, or overwrites
// objects. On installation failure, mapping is nil; an exclusively reserved target
// may contain verified orphan objects and must not be selected by SQL. The caller
// owns recovery/cleanup of these explicitly supplied disposable staging targets.
func restoreBotFileContent(ctx context.Context, path string, opts botFileBackupRestoreOptions) (map[store.BotContentRef]store.BotContentRef, error) {
	if !filepath.IsAbs(opts.SpoolDirectory) {
		return nil, store.ErrBotInvalid
	}
	// Reader reopens its path on every pass. Freeze the input in a private directory
	// so archive replacement between validation and extraction cannot change bytes.
	directory, err := os.MkdirTemp(opts.SpoolDirectory, ".bot-restore-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	snapshot := filepath.Join(directory, "archive")
	if err = snapshotBotFileArchive(ctx, path, snapshot); err != nil {
		return nil, err
	}
	if err = validateBotFileContentArchive(ctx, snapshot, opts.Reader, opts.Scopes, opts.ExpectedRoots); err != nil {
		return nil, err
	}
	reader, err := backup.OpenReader(snapshot, opts.Reader)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var index botBackupFileIndex
	if err = reader.ForEachFile(func(p string, r io.Reader) error {
		if p != botBackupIndexPath {
			return nil
		}
		return botBackupDecode(io.LimitReader(botBackupContextReader{ctx, r}, backup.MaxMetadataBytes+1), &index)
	}); err != nil {
		return nil, err
	}
	objects := make(map[string]botBackupObject, len(index.Objects))
	targets := make(map[store.BotContentScope]*botDiskContent)
	// Preflight the ENTIRE closure before reserving any destination namespace.
	for _, o := range index.Objects {
		if _, err = diskContentName(o.Ref); err != nil {
			return nil, store.ErrBotUnsupported
		}
		d := opts.Targets[o.Ref.Scope]
		if d == nil || d.root == nil || d.scope != o.Ref.Scope {
			return nil, store.ErrBotForbidden
		}
		if err = d.check(ctx); err != nil {
			return nil, err
		}
		if _, err = d.root.Lstat(d.namespace()); !errors.Is(err, os.ErrNotExist) {
			return nil, errors.Join(store.ErrBotConflict, err)
		}
		targets[o.Ref.Scope] = d
		objects[botBackupObjectPath(o)] = o
	}
	for _, d := range targets {
		if err = d.check(ctx); err != nil {
			return nil, err
		}
		// Unlike scopeRoot(true), exclusive reservation refuses even an empty prior
		// namespace. This also prevents a concurrent restore sharing a destination.
		if err = d.root.Mkdir(d.namespace(), 0700); err != nil {
			return nil, err
		}
		if err = syncBotRestoreRoot(d.root); err != nil {
			return nil, err
		}
	}
	mapping := make(map[store.BotContentRef]store.BotContentRef, len(objects))
	err = reader.ForEachFile(func(p string, r io.Reader) error {
		o, ok := objects[p]
		if !ok {
			return nil
		} // Only validated metadata entries remain.
		if err := installBotBackupObject(ctx, targets[o.Ref.Scope], o.Ref, r); err != nil {
			return err
		}
		mapping[o.Ref] = o.Ref
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(mapping) != len(objects) {
		return nil, store.ErrBotInvalid
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return mapping, nil
}

func snapshotBotFileArchive(ctx context.Context, source, target string) (result error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, in.Close()) }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return store.ErrBotInvalid
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, out.Close()) }()
	_, err = io.CopyBuffer(out, botBackupContextReader{ctx, in}, make([]byte, 32<<10))
	return err
}

func syncBotRestoreRoot(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func installBotBackupObject(ctx context.Context, d *botDiskContent, ref store.BotContentRef, body io.Reader) (result error) {
	if d == nil || d.root == nil || ref.Scope != d.scope {
		return store.ErrBotForbidden
	}
	if body == nil || ref.Validate() != nil {
		return store.ErrBotInvalid
	}
	if err := d.check(ctx); err != nil {
		return err
	}
	name, err := diskContentName(ref)
	if err != nil {
		return err
	}
	root, err := d.scopeRoot(false)
	if err != nil {
		return err
	}
	defer root.Close()
	temporary := ".restore-" + uuid.NewString()
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, file.Close()); _ = root.Remove(temporary) }()
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(file, h), botBackupContextReader{ctx, body}, make([]byte, 32<<10))
	if err != nil {
		return err
	}
	if n != ref.SizeBytes || hex.EncodeToString(h.Sum(nil)) != ref.SHA256 {
		return store.ErrBotInvalid
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h.Reset()
	n, err = io.CopyBuffer(h, botBackupContextReader{ctx, file}, make([]byte, 32<<10))
	if err != nil {
		return err
	}
	if n != ref.SizeBytes || hex.EncodeToString(h.Sum(nil)) != ref.SHA256 {
		return store.ErrBotConflict
	}
	if err = d.check(ctx); err != nil {
		return err
	}
	// Link is an atomic no-overwrite publication; remove our extra link before
	// opening through the production repository's regular-file/single-link checks.
	if err = root.Link(temporary, name); err != nil {
		return err
	}
	if err = root.Remove(temporary); err != nil {
		return err
	}
	if err = syncBotRestoreRoot(root); err != nil {
		return err
	}
	stream, err := d.Open(ctx, ref, store.BotFileRange{})
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, stream.Close()) }()
	h.Reset()
	n, err = io.CopyBuffer(h, botBackupContextReader{ctx, stream}, make([]byte, 32<<10))
	if err != nil {
		return err
	}
	if n != ref.SizeBytes || hex.EncodeToString(h.Sum(nil)) != ref.SHA256 {
		return store.ErrBotConflict
	}
	return nil
}
