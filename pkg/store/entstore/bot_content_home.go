package entstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// botHomeContent is an internal codec storage seam, not an authorization service.
// Trusted composition must provide the transaction-derived Bot, an independently
// verified retained binding and a transaction-local commit guard. In particular,
// neither the backend nor guard may re-enter the outer Bot repository.
//
// This adapter does not select SQL refs. Its caller must replace the complete
// reference closure atomically before enabling execution; copied staging bytes
// remain available when that transaction rolls back. Intake and template storage
// must continue to use their independent staging adapter.
type botHomeContent struct {
	staging     store.BotContentStaging
	stagedScope store.BotContentScope
	homeScope   store.BotContentScope
	binding     sandbox.BotHomeBinding
	backend     sandbox.BotHomeFilesBackend
	guard       func(func() error) error
	operation   context.Context
	readCache   *botTransactionContent
}

func newBotHomeContent(staging store.BotContentStaging, stagedScope store.BotContentScope, bot store.Bot, verified sandbox.BotHomeBinding, backend sandbox.BotHomeFilesBackend, guard func(func() error) error) (*botHomeContent, error) {
	if staging == nil || backend == nil || guard == nil {
		return nil, store.ErrBotUnsupported
	}
	if stagedScope.Validate() != nil || verified.Validate() != nil || bot.ID == uuid.Nil || bot.OwnerID == uuid.Nil || stagedScope.IntakeID != uuid.Nil || stagedScope.BotID != bot.ID || stagedScope.OwnerID != bot.OwnerID.String() || verified.Scope.BotID != bot.ID.String() || verified.Scope.UserID != bot.OwnerID.String() || verified.Scope.OrgID != stagedScope.OrgID {
		return nil, store.ErrBotForbidden
	}
	if bot.Home.ID == uuid.Nil || bot.Home.BotID != bot.ID || bot.Home.Scope != bot.Scope || bot.Home.Revision < 1 || bot.Home.Backend != string(verified.Backend) || bot.Home.StorageKey != verified.Ref {
		return nil, store.ErrBotConflict
	}
	switch bot.Scope {
	case store.BotScopePersonal:
		// A personal staging namespace is deliberately NOT a retained Home team.
		// The latter comes only from the verified binding, never the active viewer.
		if stagedScope.OrgID != "" && stagedScope.TeamID != personalSchemaName(bot.OwnerID.String()) {
			return nil, store.ErrBotForbidden
		}
	case store.BotScopeTeam:
		if stagedScope.TeamID != verified.Scope.TeamID {
			return nil, store.ErrBotForbidden
		}
	default:
		return nil, store.ErrBotForbidden
	}
	if binder, ok := backend.(sandbox.BotHomeSessionBinder); ok {
		if bot.HomeSessionID == uuid.Nil {
			return nil, store.ErrBotConflict
		}
		if err := binder.BindBotHomeSession(&verified, bot.HomeSessionID.String()); err != nil {
			return nil, err
		}
	}
	return &botHomeContent{staging: staging, stagedScope: stagedScope, homeScope: store.BotContentScope{OrgID: verified.Scope.OrgID, TeamID: verified.Scope.TeamID, OwnerID: bot.OwnerID.String(), BotID: bot.ID}, binding: verified, backend: backend, guard: guard}, nil
}

// botContentCommitGuard bounds each trusted operation to one synchronous commit.
// A guard that skips a commit, invokes twice, or suppresses its error fails closed.
func botContentCommitGuard(ctx context.Context, guard func(func() error) error, commit func() error) error {
	if guard == nil || commit == nil {
		return store.ErrBotForbidden
	}
	var mu sync.Mutex
	active, calls := true, 0
	var commitErr error
	err := guard(func() error {
		mu.Lock()
		defer mu.Unlock()
		if !active {
			return store.ErrBotForbidden
		}
		calls++
		if calls != 1 {
			return store.ErrBotConflict
		}
		if commitErr = ctx.Err(); commitErr == nil {
			commitErr = commit()
		}
		return commitErr
	})
	mu.Lock()
	defer mu.Unlock()
	active = false
	if calls != 1 {
		return errors.Join(err, store.ErrBotConflict)
	}
	return errors.Join(err, commitErr)
}

func (h *botHomeContent) Put(ctx context.Context, kind store.BotContentKind, revision, size int64, body io.Reader, barrier func(func() error) error) (refResult *store.BotContentRef, errResult error) {
	timing := ObserveBotExecutionStage(ctx, BotStageContentWrite)
	defer func() { timing(errResult) }()
	ctx = sandbox.WithBotHomeFileOperationFromContext(ctx, h.operation)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if barrier == nil || body == nil || size < 0 || revision < 1 {
		return nil, store.ErrBotInvalid
	}
	hash := sha256.New()
	counted := &botContentCountingReader{reader: io.TeeReader(body, hash)}
	binding := h.binding
	var guardMu sync.Mutex
	guardActive, guardCalls := true, 0
	var guardErr error
	put := h.backend.PutBotContent
	var readback []byte
	withReadback := false
	if backend, ok := h.backend.(sandbox.BotHomeContentReadbackBackend); ok && h.readCache != nil && size <= sandbox.BotHomeContentReadbackLimit && h.readCache.canStore(size) {
		withReadback = true
		put = func(ctx context.Context, b *sandbox.BotHomeBinding, k store.BotContentKind, n int64, body io.Reader, revision int64, guard func(func() error) error) (*store.BotContentRef, error) {
			ref, raw, err := backend.PutBotContentReadback(ctx, b, k, n, body, revision, guard)
			readback = raw
			return ref, err
		}
	}
	ref, err := put(ctx, &binding, kind, size, counted, revision, func(commit func() error) error {
		guardMu.Lock()
		defer guardMu.Unlock()
		if !guardActive {
			return store.ErrBotForbidden
		}
		guardCalls++
		if guardCalls != 1 {
			return store.ErrBotConflict
		}
		guardErr = botContentCommitGuard(ctx, barrier, func() error { return botContentCommitGuard(ctx, h.guard, commit) })
		return guardErr
	})
	guardMu.Lock()
	guardActive = false
	guardMu.Unlock()
	if err = errors.Join(err, guardErr); err != nil {
		return nil, err
	}
	if guardCalls != 1 {
		return nil, store.ErrBotConflict
	}
	if ref == nil || ref.Validate() != nil || ref.Scope != h.homeScope || !strings.HasPrefix(ref.Key, "content-v1/") || ref.Kind != kind || ref.Revision != revision || ref.SizeBytes != size || counted.n != size || ref.SHA256 != hex.EncodeToString(hash.Sum(nil)) {
		return nil, store.ErrBotConflict
	}
	if withReadback {
		// Validate stored bytes independently of both backend metadata and input.
		// Put's input hash above must match the full reference before caching.
		if err := h.readCache.storeReadback(ctx, botTransactionContentKey{ref: *ref, backend: h.binding.Backend, home: h.binding.Ref}, readback); err != nil {
			return nil, err
		}
	}
	return ref, nil
}

type botContentCountingReader struct {
	reader io.Reader
	n      int64
}

func (r *botContentCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.n += int64(n)
	return n, err
}

func (h *botHomeContent) Open(ctx context.Context, ref store.BotContentRef, span store.BotFileRange) (streamResult *store.BotFileStream, errResult error) {
	timing := ObserveBotExecutionStage(ctx, BotStageContentRead)
	defer func() { timing(errResult) }()
	ctx = sandbox.WithBotHomeFileOperationFromContext(ctx, h.operation)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.Validate() != nil || span.Validate() != nil {
		return nil, store.ErrBotInvalid
	}
	switch {
	case strings.HasPrefix(ref.Key, "staged-v1/"):
		if ref.Scope != h.stagedScope {
			return nil, store.ErrBotForbidden
		}
		return h.staging.Open(ctx, ref, span)
	case strings.HasPrefix(ref.Key, "content-v1/"):
		if ref.Scope != h.homeScope {
			return nil, store.ErrBotForbidden
		}
		binding := h.binding
		cache := h.readCache
		if cache == nil {
			cache = activeImmutableReadWindow(ctx)
		}
		if cache != nil && span == (store.BotFileRange{}) {
			return cache.open(ctx, botTransactionContentKey{ref: ref, backend: binding.Backend, home: binding.Ref}, func() (*store.BotFileStream, error) {
				return h.backend.OpenBotContent(ctx, &binding, ref, span)
			})
		}
		return h.backend.OpenBotContent(ctx, &binding, ref, span)
	default:
		return nil, store.ErrBotInvalid
	}
}

// bindStaged changes only physical reference metadata. No logical content is
// decoded/reencoded: immutable task/skill/action hashes remain unchanged.
func (h *botHomeContent) bindStaged(ctx context.Context, ref store.BotContentRef, barrier func(func() error) error) (*store.BotContentRef, error) {
	if ref.Scope != h.stagedScope || !strings.HasPrefix(ref.Key, "staged-v1/") {
		return nil, store.ErrBotForbidden
	}
	stream, err := h.staging.Open(ctx, ref, store.BotFileRange{})
	if err != nil {
		return nil, err
	}
	next, putErr := h.Put(ctx, ref.Kind, ref.Revision, ref.SizeBytes, stream, barrier)
	if err = errors.Join(putErr, stream.Close()); err != nil {
		return nil, err
	}
	if next.SHA256 != ref.SHA256 {
		return nil, store.ErrBotConflict
	}
	copied, err := h.Open(ctx, *next, store.BotFileRange{})
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, copied)
	if err = errors.Join(readErr, copied.Close()); err != nil {
		return nil, err
	}
	if n != ref.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
		return nil, store.ErrBotConflict
	}
	return next, nil
}

// The selected adapter cannot be rebound to an arbitrary destination or used as
// a cleanup capability. Reference-aware cleanup uses the original staging store.
func (h *botHomeContent) Bind(context.Context, store.BotContentRef, store.BotFiles, func(func() error) error) (*store.BotContentRef, error) {
	return nil, store.ErrBotUnsupported
}
func (h *botHomeContent) Remove(context.Context, store.BotContentRef, func(func() error) error) error {
	return store.ErrBotUnsupported
}
func (h *botHomeContent) Close() error { return h.staging.Close() }

var _ store.BotContentStaging = (*botHomeContent)(nil)
