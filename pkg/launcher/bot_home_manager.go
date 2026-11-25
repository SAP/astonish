package launcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// BotHomeManager adapts independently retained backend storage to the runtime.
// It only recovers registered homes: provisioning/registration and deletion are
// separate administrative operations, never side effects of executing a run.
type BotHomeManager struct {
	Backend sandbox.BotHomeBackend
	Pool    sandbox.ToolNodePool
	Bots    store.BotStore
	// Scope is required for the optional file service; unset fails closed.
	Scope store.BotScope
	// AuthorizeFileCommit is installed by trusted composition bound to the live
	// run lease (or an explicit administrative edit reservation). It must check
	// cancellation and hold the durable lease/CAS barrier while invoking commit.
	// Caller-provided BeforeCommit callbacks are additional guards, not authority.
	AuthorizeFileCommit func(context.Context, store.BotHomeBinding, func() error) error
	// AuthorizeDelete is installed only by trusted supervisor/retention wiring.
	// It must authorize the exact binding, expired restoration window and stopped
	// workers BEFORE storage deletion, returning the server-derived backend scope.
	// It must never be a public request boolean or a post-delete receipt verifier.
	AuthorizeDelete func(context.Context, store.BotHomeBinding) (*sandbox.BotHomeBinding, error)
}

var _ bot.HomeManager = (*BotHomeManager)(nil)
var _ store.BotFiles = (*BotHomeManager)(nil)

// fileBinding resolves only registered storage. Unlike execution-time binding it
// deliberately does not Ensure a volume or require an active tool-node pool.
func (m *BotHomeManager) fileBinding(ctx context.Context, id store.BotID) (*store.Bot, *sandbox.BotHomeBinding, sandbox.BotHomeFilesBackend, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if m == nil || m.Bots == nil {
		return nil, nil, nil, store.ErrBotUnsupported
	}
	backend, ok := m.Backend.(sandbox.BotHomeFilesBackend)
	if !ok {
		return nil, nil, nil, store.ErrBotUnsupported
	}
	if m.Scope != store.BotScopePersonal && m.Scope != store.BotScopeTeam {
		return nil, nil, nil, store.ErrBotForbidden
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		return nil, nil, nil, store.ErrBotForbidden
	}
	b, err := m.Bots.Get(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	if b == nil || id == uuid.Nil || b.ID != id || b.OwnerID == uuid.Nil || b.Scope != m.Scope || (m.Scope == store.BotScopePersonal && b.OwnerID.String() != p.Subject) {
		return nil, nil, nil, store.ErrBotForbidden
	}
	h := b.Home
	if h.ID == uuid.Nil || h.BotID != id || h.Scope != m.Scope || h.Revision < 1 {
		return nil, nil, nil, store.ErrBotConflict
	}
	binding := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: b.OwnerID.String(), BotID: id.String()}, Backend: sandbox.BackendKind(h.Backend), Ref: h.StorageKey}
	if binding.Validate() != nil {
		return nil, nil, nil, store.ErrBotConflict
	}
	if binder, ok := backend.(sandbox.BotHomeSessionBinder); ok {
		if b.HomeSessionID == uuid.Nil {
			return nil, nil, nil, store.ErrBotConflict
		}
		if err := binder.BindBotHomeSession(binding, b.HomeSessionID.String()); err != nil {
			return nil, nil, nil, err
		}
	}
	return b, binding, backend, nil
}

func (m *BotHomeManager) List(ctx context.Context, id store.BotID, directory, cursor string, limit int) (*store.BotFilePage, error) {
	_, binding, backend, err := m.fileBinding(ctx, id)
	if err != nil {
		return nil, err
	}
	if directory != "." && sandbox.ValidateBotHomeArtifactPath(directory) != nil {
		return nil, store.ErrBotInvalid
	}
	if limit < 0 || limit > store.BotFilePageMax {
		return nil, store.ErrBotInvalid
	}
	if limit == 0 {
		limit = store.BotFilePageDefault
	}
	return backend.ListBotHomeFiles(ctx, binding, directory, cursor, limit)
}

func (m *BotHomeManager) Stat(ctx context.Context, id store.BotID, path string) (*store.BotFileEntry, error) {
	_, binding, backend, err := m.fileBinding(ctx, id)
	if err != nil {
		return nil, err
	}
	if sandbox.ValidateBotHomeArtifactPath(path) != nil {
		return nil, store.ErrBotInvalid
	}
	return backend.StatBotHomeFile(ctx, binding, path)
}

func (m *BotHomeManager) Open(ctx context.Context, id store.BotID, path string, r store.BotFileRange) (*store.BotFileStream, error) {
	_, binding, backend, err := m.fileBinding(ctx, id)
	if err != nil {
		return nil, err
	}
	if sandbox.ValidateBotHomeArtifactPath(path) != nil || r.Validate() != nil {
		return nil, store.ErrBotInvalid
	}
	// Preserve the backend-owned streaming closer and terminal errors verbatim.
	return backend.OpenBotHomeFile(ctx, binding, path, r)
}

// fileCommit never treats an absent fence or a caller callback as permission.
// Recheck the registered binding inside the trusted live commit barrier, after
// staging, so revocation/cancellation cannot silently replace previous bytes.
func (m *BotHomeManager) fileCommit(ctx context.Context, home store.BotHomeBinding, exact sandbox.BotHomeBinding, guard func(func() error) error) (func(func() error) error, error) {
	p, ok := execution.PrincipalFromContext(ctx)
	if m.AuthorizeFileCommit == nil || !ok || p.Subject != exact.Scope.UserID {
		return nil, store.ErrBotForbidden
	}
	authorize := m.AuthorizeFileCommit
	return func(commit func() error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if commit == nil {
			return store.ErrBotInvalid
		}
		invoke := func() error {
			// Recheck before entering the transaction. The installed authorizer
			// must verify home again on its locked view; re-entering the outer
			// repository from the callback deadlocks single-connection SQLite.
			b, binding, _, err := m.fileBinding(ctx, home.BotID)
			if err != nil {
				return err
			}
			if b.Home != home || *binding != exact {
				return store.ErrBotConflict
			}
			return authorize(ctx, home, func() error {
				if err := ctx.Err(); err != nil {
					return err
				}
				return commit()
			})
		}
		if guard != nil {
			return guard(invoke)
		}
		return invoke()
	}, nil
}

func (m *BotHomeManager) Write(ctx context.Context, id store.BotID, w store.BotFileWrite) (*store.BotFileEntry, error) {
	b, binding, backend, err := m.fileBinding(ctx, id)
	if err != nil {
		return nil, err
	}
	if sandbox.ValidateBotHomeArtifactPath(w.Path) != nil || w.Body == nil || w.SizeBytes < 0 || w.CreateOnly == (w.ExpectedSHA256 != "") {
		return nil, store.ErrBotInvalid
	}
	if w.ExpectedSHA256 != "" {
		digest, err := hex.DecodeString(w.ExpectedSHA256)
		if err != nil || len(digest) != sha256.Size {
			return nil, store.ErrBotInvalid
		}
	}
	w.BeforeCommit, err = m.fileCommit(ctx, b.Home, *binding, w.BeforeCommit)
	if err != nil {
		return nil, err
	}
	return backend.WriteBotHomeFile(ctx, binding, w)
}

func botHomeContentScope(binding *sandbox.BotHomeBinding, id store.BotID) store.BotContentScope {
	return store.BotContentScope{OrgID: binding.Scope.OrgID, TeamID: binding.Scope.TeamID, OwnerID: binding.Scope.UserID, BotID: id}
}

func checkedBotContentRef(ref *store.BotContentRef, binding *sandbox.BotHomeBinding, id store.BotID, kind store.BotContentKind) (*store.BotContentRef, error) {
	if ref == nil || ref.Validate() != nil || ref.Scope != botHomeContentScope(binding, id) || ref.Kind != kind {
		return nil, store.ErrBotConflict
	}
	return ref, nil
}

func (m *BotHomeManager) PutContent(ctx context.Context, id store.BotID, kind store.BotContentKind, size int64, body io.Reader, revision int64, guard func(func() error) error) (*store.BotContentRef, error) {
	b, binding, backend, err := m.fileBinding(ctx, id)
	if err != nil {
		return nil, err
	}
	if size < 0 || revision < 1 || body == nil {
		return nil, store.ErrBotInvalid
	}
	switch kind {
	case store.BotContentDocument, store.BotContentMemory, store.BotContentTask, store.BotContentSkill, store.BotContentEvidence, store.BotContentCheckpoint, store.BotContentArtifact, store.BotContentTemplate:
	default:
		return nil, store.ErrBotInvalid
	}
	fence, err := m.fileCommit(ctx, b.Home, *binding, guard)
	if err != nil {
		return nil, err
	}
	ref, err := backend.PutBotContent(ctx, binding, kind, size, body, revision, fence)
	if err != nil {
		return nil, err
	}
	return checkedBotContentRef(ref, binding, id, kind)
}

func (m *BotHomeManager) OpenContent(ctx context.Context, ref store.BotContentRef, r store.BotFileRange) (*store.BotFileStream, error) {
	if ref.Validate() != nil || r.Validate() != nil {
		return nil, store.ErrBotInvalid
	}
	// Team template objects have separate durable ownership, not a Bot Home.
	if ref.Scope.BotID == uuid.Nil {
		return nil, store.ErrBotUnsupported
	}
	_, binding, backend, err := m.fileBinding(ctx, ref.Scope.BotID)
	if err != nil {
		return nil, err
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || (p.Subject != binding.Scope.UserID && ref.Kind != store.BotContentArtifact) {
		return nil, store.ErrBotForbidden
	}
	if ref.Scope != botHomeContentScope(binding, ref.Scope.BotID) {
		return nil, store.ErrBotForbidden
	}
	return backend.OpenBotContent(ctx, binding, ref, r)
}

func (m *BotHomeManager) PublishSnapshot(ctx context.Context, id store.BotID, path, key string, guard func(func() error) error) (*store.BotContentRef, error) {
	b, binding, backend, err := m.fileBinding(ctx, id)
	if err != nil {
		return nil, err
	}
	if sandbox.ValidateBotHomeArtifactPath(path) != nil || sandbox.ValidateBotHomeArtifactPath(key) != nil {
		return nil, store.ErrBotInvalid
	}
	fence, err := m.fileCommit(ctx, b.Home, *binding, guard)
	if err != nil {
		return nil, err
	}
	ref, err := backend.PublishBotArtifactSnapshot(ctx, binding, path, key, fence)
	if err != nil {
		return nil, err
	}
	return checkedBotContentRef(ref, binding, id, store.BotContentArtifact)
}

func (m *BotHomeManager) binding(ctx context.Context, id store.BotID) (*store.Bot, *sandbox.BotHomeBinding, error) {
	if m == nil || m.Backend == nil || m.Pool == nil || m.Bots == nil {
		return nil, nil, store.ErrBotUnsupported
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		return nil, nil, store.ErrBotForbidden
	}
	b, err := m.Bots.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if b == nil || b.ID != id || b.OwnerID.String() != p.Subject {
		return nil, nil, store.ErrBotForbidden
	}
	if b.Home.ID == uuid.Nil || b.Home.BotID != id || b.Home.Scope != b.Scope || b.Home.Revision < 1 {
		return nil, nil, store.ErrBotUnsupported
	}
	scope := sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: id.String()}
	actual, err := m.Backend.EnsureBotHome(ctx, scope)
	if err != nil {
		return nil, nil, err
	}
	if actual == nil || actual.Validate() != nil || actual.Scope != scope || string(actual.Backend) != b.Home.Backend || actual.Ref != b.Home.StorageKey {
		return nil, nil, store.ErrBotConflict
	}
	return b, actual, nil
}
func (m *BotHomeManager) Ensure(ctx context.Context, r bot.HomeRequest) (store.BotHomeBinding, error) {
	b, _, err := m.binding(ctx, r.BotID)
	if err != nil {
		return store.BotHomeBinding{}, err
	}
	if b.Scope != r.Scope {
		return store.BotHomeBinding{}, store.ErrBotForbidden
	}
	return b.Home, nil
}
func (m *BotHomeManager) BindRun(ctx context.Context, r bot.HomeRunBinding) error {
	b, binding, err := m.binding(ctx, r.Home.BotID)
	if err != nil {
		return err
	}
	if b.Home != r.Home || r.PhysicalSessionID == uuid.Nil || r.Lease.BotID != b.ID || r.Lease.Fence <= 0 || !time.Now().Before(r.Lease.ExpiresAt) {
		return store.ErrBotConflict
	}
	run, err := m.Bots.GetRun(ctx, b.ID, r.Lease.RunID)
	if err != nil {
		return err
	}
	if run == nil || run.Status != store.RunRunning || run.SessionID != r.PhysicalSessionID || run.Lease.Fence != r.Lease.Fence || run.Lease.WorkerID != r.Lease.WorkerID || !time.Now().Before(run.Lease.ExpiresAt) {
		return store.ErrBotConflict
	}
	if binder, ok := m.Backend.(sandbox.BotHomeSessionBinder); ok {
		if b.HomeSessionID == uuid.Nil {
			return store.ErrBotConflict
		}
		if err := binder.BindBotHomeSession(binding, b.HomeSessionID.String()); err != nil {
			return err
		}
		if err := sandbox.BindBotHome(m.Pool, b.HomeSessionID.String(), binding); err != nil {
			return err
		}
		m.Pool.GetOrCreate(b.HomeSessionID.String()) // metadata-only lazy client
		m.Pool.Alias(r.PhysicalSessionID.String(), b.HomeSessionID.String())
		return nil
	}
	return sandbox.BindBotHome(m.Pool, r.PhysicalSessionID.String(), binding)
}

// ReadArtifact authorizes through the scoped store and never Ensures a missing
// volume or recreates the originating run. Metadata is not an authorization grant.
func (m *BotHomeManager) ReadArtifact(ctx context.Context, home store.BotHomeBinding, artifact store.BotArtifact) (io.ReadCloser, error) {
	if m == nil || m.Bots == nil {
		return nil, store.ErrBotUnsupported
	}
	reader, ok := m.Backend.(sandbox.BotHomeArtifactBackend)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		return nil, store.ErrBotForbidden
	}
	b, err := m.Bots.Get(ctx, home.BotID)
	if err != nil {
		return nil, err
	}
	if b == nil || b.ID != home.BotID || b.OwnerID.String() != p.Subject {
		return nil, store.ErrBotForbidden
	}
	if home.ID == uuid.Nil || home != b.Home || home.Scope != b.Scope || home.Revision < 1 || artifact.HomeID != home.ID {
		return nil, store.ErrBotConflict
	}
	if err := sandbox.ValidateBotHomeArtifactPath(artifact.RelativePath); err != nil {
		return nil, store.ErrBotInvalid
	}
	if artifact.SHA256 != "" {
		if digest, err := hex.DecodeString(artifact.SHA256); err != nil || len(digest) != sha256.Size {
			return nil, store.ErrBotInvalid
		}
	}
	binding := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: b.ID.String()}, Backend: sandbox.BackendKind(home.Backend), Ref: home.StorageKey}
	data, err := reader.ReadBotHomeArtifact(ctx, binding, artifact.RelativePath, sandbox.MaxBotHomeArtifactBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > sandbox.MaxBotHomeArtifactBytes {
		return nil, store.ErrBotInvalid
	}
	digest := sha256.Sum256(data)
	if artifact.SHA256 != "" && !strings.EqualFold(artifact.SHA256, hex.EncodeToString(digest[:])) {
		return nil, store.ErrBotConflict
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *BotHomeManager) Delete(ctx context.Context, home store.BotHomeBinding) error {
	if m == nil {
		return store.ErrBotUnsupported
	}
	deleter, ok := m.Backend.(sandbox.BotHomeDeleteBackend)
	if !ok {
		return store.ErrBotUnsupported
	}
	if m.AuthorizeDelete == nil {
		return store.ErrBotForbidden
	}
	binding, err := m.AuthorizeDelete(ctx, home)
	if err != nil {
		return err
	}
	if home.ID == uuid.Nil || home.BotID == uuid.Nil || home.Revision < 1 || binding == nil || binding.Validate() != nil || binding.Scope.BotID != home.BotID.String() || string(binding.Backend) != home.Backend || binding.Ref != home.StorageKey {
		return store.ErrBotConflict
	}
	// Capture a value, not the authorizer's mutable pointer. Every recheck must
	// return the same server-derived owner/tenant as the original reservation.
	exact := *binding
	recheck := func(c context.Context) error {
		if err := c.Err(); err != nil {
			return err
		}
		current, err := m.AuthorizeDelete(c, home)
		if err != nil {
			return err
		}
		if current == nil || *current != exact {
			return store.ErrBotConflict
		}
		return nil
	}
	if reconciler, ok := m.Backend.(sandbox.BotHomeSessionCleanupBackend); ok {
		if err := reconciler.ReconcileBotHomeSessions(ctx, &exact, recheck); err != nil {
			return err
		}
	}
	if err := recheck(ctx); err != nil {
		return err
	}
	return deleter.DeleteBotHome(ctx, &exact)
}
