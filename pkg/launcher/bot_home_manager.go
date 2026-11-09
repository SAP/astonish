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
	// AuthorizeDelete is installed only by trusted supervisor/retention wiring.
	// It must authorize the exact binding, expired restoration window and stopped
	// workers BEFORE storage deletion, returning the server-derived backend scope.
	// It must never be a public request boolean or a post-delete receipt verifier.
	AuthorizeDelete func(context.Context, store.BotHomeBinding) (*sandbox.BotHomeBinding, error)
}

var _ bot.HomeManager = (*BotHomeManager)(nil)

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
	return deleter.DeleteBotHome(ctx, binding)
}
