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

type cancellationReceiptBackend struct {
	sandbox.Backend
	err     error
	session string
}

func (b *cancellationReceiptBackend) VerifySessionDestroyed(_ context.Context, id string) error {
	b.session = id
	return b.err
}

type cancellationReceiptStore struct {
	store.BotStore
	run       store.BotRun
	bot       store.Bot
	confirmed bool
}

func (s *cancellationReceiptStore) GetRun(context.Context, store.BotID, store.RunID) (*store.BotRun, error) {
	return &s.run, nil
}
func (s *cancellationReceiptStore) Get(context.Context, store.BotID) (*store.Bot, error) {
	return &s.bot, nil
}
func (s *cancellationReceiptStore) ConfirmCleanup(ctx context.Context, q store.BotCleanupRequest) error {
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Kind != execution.PrincipalKindService || !p.HasScope(store.BotCleanupScope) {
		return store.ErrBotForbidden
	}
	if q.RunID != s.run.ID || q.RunFence != s.run.Lease.Fence || q.Fence != q.RunFence+1 || q.Home != s.bot.Home {
		return store.ErrBotConflict
	}
	verify, ok := store.BotCleanupVerifierFromContext(ctx)
	if !ok {
		return store.ErrBotForbidden
	}
	if err := verify(ctx, q); err != nil {
		return err
	}
	s.confirmed = true
	return nil
}
func TestBotWorkerCancellationRequiresPhysicalReceipt(t *testing.T) {
	id, rid := uuid.New(), uuid.New()
	repo := &cancellationReceiptStore{bot: store.Bot{ID: id, Status: store.BotActive, Revision: 2}, run: store.BotRun{BotID: id, ID: rid, Status: store.RunInterrupted, SessionID: uuid.New(), Lease: store.BotLease{Fence: 3, WorkerID: "fixture"}}}
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := execution.PrincipalFromContext(ctx)
	repo.bot.OwnerID = uuid.MustParse(owner.Subject)
	physical := &cancellationReceiptBackend{err: errors.New("still terminating")}
	worker := &BotWorker{config: BotWorkerConfig{WorkerID: "fixture", Sandbox: physical}}
	if err = worker.confirmCancelledRun(ctx, repo, id, rid); err == nil || repo.confirmed {
		t.Fatal("early receipt accepted")
	}
	physical.err = nil
	if err = worker.confirmCancelledRun(ctx, repo, id, rid); err != nil || !repo.confirmed {
		t.Fatalf("receipt not confirmed %v", err)
	}
	if physical.session != repo.run.SessionID.String() {
		t.Fatal("wrong physical run")
	}
	repo.confirmed = false
	physical.session = ""
	worker.config.WorkerID = "another-worker"
	repo.run.Lease.ExpiresAt = time.Now().Add(time.Minute)
	if err = worker.confirmCancelledRun(ctx, repo, id, rid); err != nil || repo.confirmed || physical.session != "" {
		t.Fatal("foreign worker verified teardown", err)
	}
	worker.config.WorkerID = "fixture"
	// Revoked (zero) leases and expired old leases can be verified after restart,
	// but a running run or a foreign owner must never reach physical verification.
	for _, expiry := range []time.Time{{}, time.Now().Add(-time.Minute)} {
		worker.config.WorkerID = "replacement"
		repo.run.Lease.ExpiresAt = expiry
		repo.bot.Status = store.BotPaused
		if err = worker.confirmCancelledRun(ctx, repo, id, rid); err != nil || !repo.confirmed {
			t.Fatal("restart receipt refused", err)
		}
		repo.confirmed = false
		physical.session = ""
	}
	repo.run.Status = store.RunRunning
	if err = worker.confirmCancelledRun(ctx, repo, id, rid); err != nil || repo.confirmed || physical.session != "" {
		t.Fatal("running worker inspected", err)
	}
	repo.run.Status = store.RunInterrupted
	repo.bot.OwnerID = uuid.New()
	if err = worker.confirmCancelledRun(ctx, repo, id, rid); !errors.Is(err, store.ErrBotForbidden) || physical.session != "" {
		t.Fatal("foreign owner inspected", err)
	}
	repo.bot.OwnerID = uuid.MustParse(owner.Subject)
	worker.config.WorkerID = "fixture"
	sessionID := repo.run.SessionID
	repo.run.SessionID = uuid.Nil
	if err = worker.confirmCancelledRun(ctx, repo, id, rid); !errors.Is(err, store.ErrBotConflict) || repo.confirmed || physical.session != "" {
		t.Fatal("missing session accepted as physical evidence", err)
	}
	repo.run.SessionID = sessionID
	worker.config.Sandbox = nil
	if err = worker.confirmCancelledRun(ctx, repo, id, rid); !errors.Is(err, store.ErrBotUnsupported) || repo.confirmed {
		t.Fatal("missing verifier accepted")
	}
}
