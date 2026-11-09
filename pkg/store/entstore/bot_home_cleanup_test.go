package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotHomeCleanupReservation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, _, run, lease, now := botRunFixture(t, scope)
			deleted, teardown := cleanupDeleteFixture(t, repo, owner, run, lease)
			runtime := cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return nil })
			if _, err := repo.ReserveHomeCleanup(owner, deleted.Home, deleted.Revision); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("owner claim: %v", err)
			}
			if _, err := repo.ReserveHomeCleanup(runtime, deleted.Home, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("early claim: %v", err)
			}
			*now = *deleted.RestoreUntil
			if _, err := repo.ReserveHomeCleanup(runtime, deleted.Home, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("active teardown barrier: %v", err)
			}
			if err := repo.ConfirmCleanup(runtime, teardown); err != nil {
				t.Fatal(err)
			}
			stale := deleted.Home
			stale.StorageKey += "-wrong"
			if _, err := repo.ReserveHomeCleanup(runtime, stale, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("wrong binding: %v", err)
			}
			if _, err := repo.ReserveHomeCleanup(runtime, deleted.Home, deleted.Revision+1); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale revision: %v", err)
			}
			before, err := repo.db.GetBot(owner, deleted.ID)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := repo.ReserveHomeCleanup(runtime, deleted.Home, deleted.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if claim.Request.Home != deleted.Home || claim.OwnerID != deleted.OwnerID.String() {
				t.Fatal("wrong claim identity")
			}
			after, _ := repo.db.GetBot(owner, deleted.ID)
			if _, ok := after.Metadata[botHomeCleanupClaimKey]; !ok {
				t.Fatal("claim not durable")
			}
			if after.LastSequence != before.LastSequence+1 {
				t.Fatal("missing reservation CAS")
			}
			// Simulate a restore that read before the claim. CAS must reject it even
			// though the Bot revision and fence have not changed.
			next := before.Bot
			next.Status = store.BotPaused
			next.Revision++
			next.RestoreUntil = nil
			next.DeletedAt = nil
			if err := repo.db.Transaction(owner, func(tx botDraftDB) error { return tx.SetLifecycle(owner, *before, next) }); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale restore CAS: %v", err)
			}
			// Clock rollback cannot reopen a claimed home.
			*now = deleted.RestoreUntil.Add(-time.Hour)
			if _, err := repo.Restore(owner, store.BotLifecycleRequest{BotID: deleted.ID, ExpectedRevision: deleted.Revision, ActorID: deleted.OwnerID, Reason: "race"}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("restore after reservation: %v", err)
			}
			*now = *deleted.RestoreUntil
			// A failed physical deletion produces no receipt and no purge eligibility.
			failure := errors.New("backend deletion failed")
			if err := repo.ConfirmCleanup(cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return failure }), claim.Request); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if n, err := repo.PurgeDue(owner, *now, 10); err != nil || n != 0 {
				t.Fatalf("failed deletion purged: %d %v", n, err)
			}
			// Reconstruct the repository as on a worker restart; durable claim is identical.
			restarted := *repo
			retry, err := restarted.ReserveHomeCleanup(runtime, deleted.Home, deleted.Revision)
			if err != nil || *retry != *claim {
				t.Fatalf("retry: %+v %v", retry, err)
			}
			if err := restarted.ConfirmCleanup(runtime, retry.Request); err != nil {
				t.Fatal(err)
			}
			if n, err := restarted.PurgeDue(owner, *now, 10); err != nil || n != 1 {
				t.Fatalf("verified purge: %d %v", n, err)
			}
		})
	}
}

func TestBotHomeCleanupRestoreWins(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, _, run, lease, now := botRunFixture(t, scope)
			deleted, _ := cleanupDeleteFixture(t, repo, owner, run, lease)
			if _, err := repo.Restore(owner, store.BotLifecycleRequest{BotID: deleted.ID, ExpectedRevision: deleted.Revision, ActorID: deleted.OwnerID, Reason: "restore first"}); err != nil {
				t.Fatal(err)
			}
			*now = *deleted.RestoreUntil
			if _, err := repo.ReserveHomeCleanup(cleanupRuntimeContext(t, nil), deleted.Home, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("claim after restore: %v", err)
			}
		})
	}
}
