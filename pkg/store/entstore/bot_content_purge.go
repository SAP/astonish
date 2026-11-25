package entstore

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// This private selector is used only after PurgeBot holds the root CAS lock and
// verifies physical cleanup. Opening a deleted Home would require storage that
// has already been removed; staging ownership still comes from the tx-bound row.
type botContentPurgeKey struct{}

func (c *botContentCodec) purgeStaging(ctx context.Context, rec botStoredRecord) error {
	if c == nil || c.Open == nil {
		return store.ErrBotUnsupported
	}
	if rec.Bot.Status != store.BotDeleted || rec.Bot.ID == uuid.Nil || rec.ActiveRunID != nil || rec.LeaseExpiresAt != nil {
		return store.ErrBotConflict
	}
	content, err := c.Open(context.WithValue(ctx, botContentPurgeKey{}, true), rec.Bot.ID)
	if err != nil {
		return err
	}
	disk, ok := content.(*botDiskContent)
	if !ok {
		return errors.Join(store.ErrBotUnsupported, content.Close())
	}
	if disk.scope.BotID != rec.Bot.ID || disk.scope.OwnerID != rec.Bot.OwnerID.String() || disk.scope.IntakeID != uuid.Nil {
		return errors.Join(store.ErrBotForbidden, disk.Close())
	}
	return errors.Join(disk.purgeNamespace(ctx), disk.Close())
}

// purgeNamespace unlinks only the exact Bot namespace. It deliberately does not
// follow links or recurse: staging's layout is flat. Any unexpected directory
// blocks SQL purge instead of adopting unknown content. Partial removal is safe
// to retry because the caller holds an irreversible deleted/expired root barrier.
func (d *botDiskContent) purgeNamespace(ctx context.Context) error {
	if err := d.check(ctx); err != nil {
		return err
	}
	if d.scope.BotID == uuid.Nil || d.scope.IntakeID != uuid.Nil {
		return store.ErrBotForbidden
	}
	parent, err := d.root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	root, err := d.scopeRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return parent.Sync()
	}
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		entries, readErr := dir.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Remove unlinks a symlink rather than following its target. Never recurse.
			if entry.IsDir() {
				return store.ErrBotConflict
			}
			if err := root.Remove(entry.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if err := dir.Sync(); err != nil {
		return err
	}
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	current, err := d.root.Lstat(d.namespace())
	if err != nil {
		return err
	}
	if !os.SameFile(opened, current) {
		return store.ErrBotConflict
	}
	if err := d.root.Remove(d.namespace()); err != nil {
		return err
	}
	// SQL must never commit before the namespace removal itself is durable.
	return parent.Sync()
}
