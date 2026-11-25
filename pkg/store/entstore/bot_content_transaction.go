package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// Each SQL transaction owns at most 8 MiB / 256 objects of verified immutable
// content. The cache never survives that transaction, caches failures, or caches
// mutable Home files. The normal opener still checks the current Bot/binding on
// every call.
const botTransactionContentBytes = 8 << 20
const botTransactionContentObjects = 256

type botTransactionContentKey struct {
	ref     store.BotContentRef
	backend sandbox.BackendKind
	home    string
}

type botTransactionContent struct {
	mu      sync.Mutex
	closed  bool
	bytes   int64
	objects map[botTransactionContentKey][]byte
}

type botContentSQLTransactionKey struct{}

func transactionBotContentCodec(c *botContentCodec, operation ...context.Context) *botContentCodec {
	if c == nil || c.Open == nil {
		return c
	}
	cache := &botTransactionContent{objects: make(map[botTransactionContentKey][]byte)}
	return &botContentCodec{RuntimeScope: c.RuntimeScope, OpenIntake: c.OpenIntake, Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
		if len(operation) == 1 {
			ctx = sandbox.WithBotHomeFileOperationFromContext(ctx, operation[0])
		}
		disk, err := c.Open(context.WithValue(ctx, botContentSQLTransactionKey{}, true), id)
		if home, ok := disk.(*botHomeContent); ok && err == nil {
			home.operation = ctx
			// A synchronous read operation may intentionally span several short SQL
			// transactions. Its verified immutable bytes remain authoritative until
			// that operation ends; this transaction cache covers callers without one.
			// The between-call window is the same contract for tool-return bookkeeping
			// that is not itself one WithBotContentReadOperation.
			operation, _ := ctx.Value(botContentReadOperationKey{}).(*botTransactionContent)
			active := false
			if operation != nil {
				operation.mu.Lock()
				active = !operation.closed
				operation.mu.Unlock()
			}
			if !active {
				if window := activeImmutableReadWindow(ctx); window != nil {
					home.readCache = window
				} else {
					home.readCache = cache
				}
			}
		}
		return disk, err
	}}
}

func (c *botTransactionContent) open(ctx context.Context, key botTransactionContentKey, open func() (*store.BotFileStream, error)) (*store.BotFileStream, error) {
	ref := key.ref
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.closed {
		return open()
	}
	if raw, ok := c.objects[key]; ok {
		return botTransactionContentStream(ctx, ref, raw), nil
	}
	stream, err := open()
	if err != nil {
		return nil, err
	}
	if stream == nil {
		return nil, store.ErrBotConflict
	}
	if ref.SizeBytes > botTransactionContentBytes-c.bytes || len(c.objects) >= botTransactionContentObjects {
		return stream, nil
	}
	raw, readErr := io.ReadAll(io.LimitReader(stream, ref.SizeBytes+1))
	if err := errors.Join(readErr, stream.Close()); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != ref.SizeBytes || hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, store.ErrBotConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.objects[key] = raw
	c.bytes += int64(len(raw))
	return botTransactionContentStream(ctx, ref, raw), nil
}

func (c *botTransactionContent) canStore(size int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && size >= 0 && size <= botTransactionContentBytes-c.bytes && len(c.objects) < botTransactionContentObjects
}

// storeReadback accepts only actual stored bytes after the Put caller verified
// its independently computed input digest. Copy prevents backend buffer aliasing.
func (c *botTransactionContent) storeReadback(ctx context.Context, key botTransactionContentKey, raw []byte) error {
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != key.ref.SizeBytes || hex.EncodeToString(sum[:]) != key.ref.SHA256 {
		return store.ErrBotConflict
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := c.objects[key]; exists {
		return nil
	}
	if c.closed || key.ref.SizeBytes > botTransactionContentBytes-c.bytes || len(c.objects) >= botTransactionContentObjects {
		return nil
	}
	c.objects[key] = bytes.Clone(raw)
	c.bytes += int64(len(raw))
	return nil
}

func botTransactionContentStream(ctx context.Context, ref store.BotContentRef, raw []byte) *store.BotFileStream {
	return &store.BotFileStream{ReadCloser: io.NopCloser(&botContentContextReader{ctx: ctx, reader: bytes.NewReader(raw)}), Entry: store.BotFileEntry{Path: ref.Key, Kind: store.BotFileRegular, SizeBytes: ref.SizeBytes, SHA256: ref.SHA256}}
}
