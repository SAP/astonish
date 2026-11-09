package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type retentionStoreFixture struct {
	store.BotStore
	store.BotPublicationStore
	bot       store.Bot
	claim     *store.BotHomeCleanupClaim
	confirmed bool
	purged    bool
}

func (s *retentionStoreFixture) ListActivity(context.Context, store.BotID, int64, int) ([]store.BotActivity, error) {
	return nil, nil
}
func (s *retentionStoreFixture) ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error) {
	return nil, nil
}

func (s *retentionStoreFixture) List(context.Context, store.BotPage) ([]store.Bot, error) {
	if s.purged {
		return nil, nil
	}
	return []store.Bot{s.bot}, nil
}
func (s *retentionStoreFixture) ReserveHomeCleanup(ctx context.Context, home store.BotHomeBinding, revision int64) (*store.BotHomeCleanupClaim, error) {
	p, _ := execution.PrincipalFromContext(ctx)
	if p.Kind != execution.PrincipalKindService {
		return nil, store.ErrBotForbidden
	}
	if s.bot.Status != store.BotDeleted || s.bot.RestoreUntil.After(time.Now()) || home != s.bot.Home || revision != s.bot.Revision {
		return nil, store.ErrBotConflict
	}
	if s.claim == nil {
		s.claim = &store.BotHomeCleanupClaim{Request: store.BotCleanupRequest{Kind: store.BotCleanupHome, BotID: s.bot.ID, ExpectedRevision: revision, Fence: 2, Home: home}, OwnerID: s.bot.OwnerID.String(), OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
	}
	copy := *s.claim
	return &copy, nil
}
func (s *retentionStoreFixture) ConfirmCleanup(ctx context.Context, q store.BotCleanupRequest) error {
	verify, ok := store.BotCleanupVerifierFromContext(ctx)
	if !ok || s.claim == nil || s.claim.Request != q {
		return store.ErrBotConflict
	}
	forged := q
	forged.Fence++
	if verify(ctx, forged) == nil {
		return errors.New("receipt accepted different fence")
	}
	if err := verify(ctx, q); err != nil {
		return err
	}
	s.confirmed = true
	return nil
}
func (s *retentionStoreFixture) PurgeDue(context.Context, time.Time, int) (int, error) {
	if !s.confirmed {
		return 0, errors.New("purge before confirmation")
	}
	s.purged = true
	return 1, nil
}

type retentionBackendFixture struct {
	sandbox.Backend
	sandbox.BotHomeBackend
	repo  *retentionStoreFixture
	fail  bool
	calls int
}

func (b *retentionBackendFixture) DeleteBotHome(_ context.Context, binding *sandbox.BotHomeBinding) error {
	b.calls++
	if b.repo.claim == nil {
		return errors.New("delete before durable reservation")
	}
	if binding.Ref != b.repo.claim.Request.Home.StorageKey || binding.Scope.UserID != b.repo.claim.OwnerID {
		return errors.New("incorrect deletion binding")
	}
	if b.fail {
		return errors.New("backend unavailable")
	}
	return nil
}

func retentionFixture(t *testing.T) (*BotWorker, *retentionStoreFixture, *retentionBackendFixture, context.Context) {
	t.Helper()
	id, owner := uuid.New(), uuid.New()
	expiry := time.Now().Add(-time.Hour)
	repo := &retentionStoreFixture{bot: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopePersonal, Status: store.BotDeleted, Revision: 2, RestoreUntil: &expiry, Home: store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: store.BotScopePersonal, Backend: "docker", StorageKey: "fixture-only-home", Revision: 1}}}
	backend := &retentionBackendFixture{repo: repo}
	w := &BotWorker{config: BotWorkerConfig{WorkerID: "retention-fixture", Sandbox: backend}}
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	return w, repo, backend, ctx
}

func TestBotRetentionPollingRetry(t *testing.T) {
	w, repo, backend, ctx := retentionFixture(t)
	backend.fail = true
	if err := w.pollStore(ctx, repo, nil); err == nil {
		t.Fatal("missing backend error")
	}
	if backend.calls != 1 || repo.claim == nil || repo.confirmed || repo.purged {
		t.Fatalf("failure lost retry or confirmed: %+v calls=%d", repo, backend.calls)
	}
	claim := *repo.claim
	backend.fail = false
	// A fresh worker reuses the persisted intent; no in-memory success flag is reused.
	restarted := &BotWorker{config: w.config}
	if err := restarted.pollStore(ctx, repo, nil); err != nil {
		t.Fatal(err)
	}
	if backend.calls != 2 || *repo.claim != claim || !repo.confirmed || !repo.purged {
		t.Fatal("retry did not confirm exact success and purge")
	}
	t.Log("production poll fixture: reserved before Delete; backend failure retained; retry confirmed immutable receipt before purge (no live Docker)")
}

func TestBotRetentionOwnerCannotInvokeAuthorizer(t *testing.T) {
	w, repo, backend, ctx := retentionFixture(t)
	var claim store.BotHomeCleanupClaim
	manager := w.botRetentionManager(repo, repo.bot, &claim)
	if _, err := manager.AuthorizeDelete(ctx, repo.bot.Home); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("owner invoked raw callback: %v", err)
	}
	if err := manager.Delete(ctx, repo.bot.Home); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("owner deleted: %v", err)
	}
	if backend.calls != 0 || repo.claim != nil {
		t.Fatal("owner reached reservation/deletion")
	}
}

func TestBotRetentionBeforeExpiry(t *testing.T) {
	w, repo, backend, ctx := retentionFixture(t)
	future := time.Now().Add(time.Hour)
	repo.bot.RestoreUntil = &future
	if err := w.pollStore(ctx, repo, nil); err != nil {
		t.Fatal(err)
	}
	if backend.calls != 0 || repo.claim != nil || repo.confirmed {
		t.Fatal("retention window bypassed")
	}
}

func TestBotRetentionUnsupportedBackend(t *testing.T) {
	w, repo, _, ctx := retentionFixture(t)
	w.config.Sandbox = nil
	if err := w.pollStore(ctx, repo, nil); err != nil {
		t.Fatal(err)
	}
	if repo.claim != nil || repo.confirmed || repo.purged {
		t.Fatal("unsupported backend authorized cleanup")
	}
}
