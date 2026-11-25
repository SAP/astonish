package entstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// BotStagingGCReport describes an explicit, conservative collection. Only old,
// unreferenced document objects are eligible; other kinds remain unclassified.
// No paths or private reference material leave the scoped repository.
type BotStagingGCReport struct {
	Referenced      int
	Young           int
	InFlight        int
	OldUnclassified int
	Removed         int
}

// CollectBotStagingOrphans is an explicit internal maintenance entry point. It
// must never be invoked by GET/list/read, or treated as successful GC on error.
// SQL and publication fences protect the complete typed reference inventory.
// This is deliberately limited to documents: arbitrary old artifact/checkpoint
// objects may still contain references or be selected by another workflow.
// It deliberately does not reuse deleted-Bot purge authorization for a live Bot.
func CollectBotStagingOrphans(ctx context.Context, bots store.BotStore, id store.BotID, grace time.Duration) (*BotStagingGCReport, error) {
	switch s := bots.(type) {
	case *personalBotStore:
		if s != nil {
			return s.repository().collectStagingOrphans(ctx, id, grace)
		}
	case *teamBotStore:
		if s != nil {
			return s.repository().collectStagingOrphans(ctx, id, grace)
		}
	}
	return nil, store.ErrBotUnsupported
}

func (r *botRepository) collectStagingOrphans(ctx context.Context, id store.BotID, grace time.Duration) (*BotStagingGCReport, error) {
	if id == uuid.Nil || grace < 24*time.Hour {
		return nil, store.ErrBotInvalid
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		return nil, store.ErrBotForbidden
	}
	if _, err := r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return nil, err
	}
	var report *BotStagingGCReport
	err := r.db.Transaction(ctx, func(tx botDraftDB) (resultErr error) {
		db, ok := tx.(botHomeConfigurationDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		row, err := tx.GetBot(ctx, id)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, row.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != row.Bot.OwnerID || row.Bot.PermanentDelete || row.Bot.Status == store.BotDeleted {
			return store.ErrBotForbidden
		}
		if row.ActiveRunID != nil || row.LeaseExpiresAt != nil {
			return store.ErrBotBusy
		}
		// An actual root UPDATE obtains the database write fence. Keep it through
		// inventory, validation and filesystem inspection; do not sweep after commit.
		if err := db.selectHomeConfigurationRecords(ctx, *row, nil, nil); err != nil {
			return err
		}
		codec := db.contentCodec()
		if codec == nil || codec.Open == nil {
			return store.ErrBotUnsupported
		}
		content, err := codec.Open(ctx, id)
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, content.Close()) }()
		staging := content
		if home, ok := content.(*botHomeContent); ok {
			staging = home.staging
		}
		disk, ok := staging.(*botDiskContent)
		if !ok {
			return store.ErrBotUnsupported
		}
		if disk.scope.BotID != id || disk.scope.OwnerID != row.Bot.OwnerID.String() || disk.scope.IntakeID != uuid.Nil {
			return store.ErrBotForbidden
		}
		publication, err := disk.lockPublication(ctx)
		if err != nil {
			return err
		}
		defer publication.Close()
		scopes := []store.BotContentScope{disk.scope}
		if home, ok := content.(*botHomeContent); ok {
			scopes = append(scopes, home.homeScope)
		}
		if codec.RuntimeScope != nil {
			runtimeScope, err := codec.RuntimeScope(ctx, id)
			if err != nil {
				return err
			}
			if runtimeScope.BotID != id || runtimeScope.OwnerID != row.Bot.OwnerID.String() {
				return store.ErrBotForbidden
			}
			scopes = append(scopes, runtimeScope)
		}
		inventory, err := newBotBackupInventory(scopes)
		if err != nil {
			return err
		}
		records, err := db.homeConfigurationRecords(ctx, id)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := validateHomeRecord(ctx, codec, id, record); err != nil {
				return err
			}
		}
		if err := collectBotBackupRoots(ctx, tx, id, inventory); err != nil {
			return err
		}
		refs, err := stagingGCClosure(ctx, content, disk.scope, inventory)
		if err != nil {
			return err
		}
		report, err = disk.inspectStagingGC(ctx, refs, time.Now().Add(-grace))
		return err
	})
	return report, err
}

// stagingGCClosure shares backup's explicit DTO edge allowlist. Opaque model
// JSON is never interpreted as authority. Verify every byte before any deletion.
func stagingGCClosure(ctx context.Context, content store.BotContentStaging, scope store.BotContentScope, inventory *botBackupInventory) (map[string]struct{}, error) {
	refs := make(map[string]struct{})
	for n := 0; n < len(inventory.objects); n++ {
		object := inventory.objects[n]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		stream, err := content.Open(ctx, object.Ref, store.BotFileRange{})
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		// Limit even a faulty storage adapter to the advertised object size.
		limited := &io.LimitedReader{R: stream, N: object.Ref.SizeBytes + 1}
		reader := io.TeeReader(limited, hash)
		decodeErr := inventory.children(object, reader)
		_, readErr := io.Copy(io.Discard, reader)
		closeErr := stream.Close()
		if err := errors.Join(decodeErr, readErr, closeErr); err != nil {
			return nil, err
		}
		if limited.N != 1 || hex.EncodeToString(hash.Sum(nil)) != object.Ref.SHA256 {
			return nil, store.ErrBotConflict
		}
		if strings.HasPrefix(object.Ref.Key, "staged-v1/") {
			if object.Ref.Scope != scope {
				return nil, store.ErrBotForbidden
			}
			name, err := diskContentName(object.Ref)
			if err != nil {
				return nil, err
			}
			refs[name] = struct{}{}
		}
	}
	return refs, nil
}

// inspectStagingGC validates the entire bounded namespace before deleting any
// candidate. Caller holds both the root SQL write fence and publication flock.
// Temporary writes are retained regardless of age. Only document/version bytes
// are collected: their writers always select freshly generated refs under the
// root SQL fence, and no typed content edge points from another object to them.
// Other kinds remain retained even when absent from the live reference closure.
func (d *botDiskContent) inspectStagingGC(ctx context.Context, refs map[string]struct{}, cutoff time.Time) (*BotStagingGCReport, error) {
	if err := d.check(ctx); err != nil {
		return nil, err
	}
	if d.scope.BotID == uuid.Nil || d.scope.IntakeID != uuid.Nil {
		return nil, store.ErrBotForbidden
	}
	report := &BotStagingGCReport{}
	root, err := d.scopeRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	type candidate struct {
		name string
		info os.FileInfo
	}
	var candidates []candidate
	count := 0
	for {
		entries, readErr := dir.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		for _, entry := range entries {
			count++
			if count > botBackupMaxObjects {
				return nil, store.ErrBotInvalid
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			name := entry.Name()
			temporary := strings.HasPrefix(name, ".stage-")
			if temporary {
				id, err := uuid.Parse(strings.TrimPrefix(name, ".stage-"))
				if err != nil || ".stage-"+id.String() != name {
					return nil, store.ErrBotConflict
				}
			} else if !validStagingGCName(name, d.scope) {
				return nil, store.ErrBotConflict
			}
			info, err := root.Lstat(name)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, store.ErrBotConflict
			}
			file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
			if err != nil {
				return nil, err
			}
			opened, statErr := file.Stat()
			var stat unix.Stat_t
			fdErr := unix.Fstat(int(file.Fd()), &stat)
			closeErr := file.Close()
			if err := errors.Join(statErr, fdErr, closeErr); err != nil {
				return nil, err
			}
			if !os.SameFile(info, opened) || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
				return nil, store.ErrBotConflict
			}
			if temporary {
				report.InFlight++
				continue
			}
			if _, ok := refs[name]; ok {
				report.Referenced++
			} else if !opened.ModTime().Before(cutoff) {
				report.Young++
			} else if store.BotContentKind(strings.SplitN(name, "-", 2)[0]) == store.BotContentDocument {
				candidates = append(candidates, candidate{name: name, info: opened})
			} else {
				report.OldUnclassified++
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	// Unknown entries and broken closure above never cause partial collection.
	// A filesystem failure during unlink may be partial; Removed reports evidence
	// even when SQL subsequently rolls back. Only unreachable bytes were removed.
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		current, err := root.Lstat(candidate.name)
		if err != nil {
			return report, err
		}
		if !os.SameFile(candidate.info, current) || !current.Mode().IsRegular() || !current.ModTime().Before(cutoff) {
			return report, store.ErrBotConflict
		}
		if err := root.Remove(candidate.name); err != nil {
			return report, err
		}
		report.Removed++
	}
	return report, dir.Sync()
}

func validStagingGCName(name string, scope store.BotContentScope) bool {
	parts := strings.SplitN(name, "-", 3)
	if len(parts) != 3 {
		return false
	}
	revision, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	ref := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: scope, Kind: store.BotContentKind(parts[0]), Revision: revision, SHA256: strings.Repeat("0", 64), Key: "staged-v1/" + name}
	if ref.Validate() != nil {
		return false
	}
	_, err = diskContentName(ref)
	return err == nil
}
