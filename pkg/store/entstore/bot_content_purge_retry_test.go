package entstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Force a transaction rollback after the concrete SQL and disk purge both ran.
// The durable delete intent must remain sufficient for a metadata-only retry.
type rollbackPurgeDB struct{ botDraftDB }

func (d rollbackPurgeDB) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errors.New("injected commit failure")
	})
}

func TestBotStagingPurgeFailureRetry(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, _, _, _ := botRunFixture(t, scope)
			owner := uuid.MustParse(repo.auth.userID)
			draft, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Draft", OwnerID: owner, Version: permanentTestVersion(), IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			disk := purgeTestDisk(t, repo, ctx, draft.ID)
			root, err := disk.scopeRoot(false)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			// A foreign directory is never recursively adopted, and makes disk deletion fail.
			if err := root.Mkdir("unexpected", 0700); err != nil {
				t.Fatal(err)
			}
			deleted, err := repo.BeginPermanentDelete(ctx, store.BotPermanentDeleteRequest{BotID: draft.ID, ActorID: owner, ExpectedRevision: draft.Revision, Confirm: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.PurgeDeleted(ctx, draft.ID, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("unsafe disk purge: %v", err)
			}
			rec, err := repo.db.GetBot(ctx, draft.ID)
			if err != nil || rec.Bot.Revision != deleted.Revision || !rec.Bot.PermanentDelete {
				t.Fatalf("disk failure lost retry metadata: %+v %v", rec, err)
			}
			if err := root.Remove("unexpected"); err != nil {
				t.Fatal(err)
			}
			original := repo.db
			repo.db = rollbackPurgeDB{original}
			if err := repo.PurgeDeleted(ctx, draft.ID, deleted.Revision); err == nil {
				t.Fatal("missing commit failure")
			}
			repo.db = original
			if _, err := disk.root.Lstat(disk.namespace()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("disk purge did not run: %v", err)
			}
			rec, err = repo.db.GetBot(ctx, draft.ID)
			if err != nil || rec.Bot.Revision != deleted.Revision || !rec.Bot.PermanentDelete {
				t.Fatalf("commit failure lost retry metadata: %+v %v", rec, err)
			}
			if _, err := repo.Restore(ctx, store.BotLifecycleRequest{BotID: draft.ID, ActorID: owner, ExpectedRevision: deleted.Revision, Reason: "attempt restore after purge"}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("restored after irreversible purge: %v", err)
			}
			restarted := *repo
			if err := restarted.PurgeDeleted(ctx, draft.ID, deleted.Revision); err != nil {
				t.Fatalf("retry after missing namespace: %v", err)
			}
		})
	}
}

func TestBotRetentionPurgesStagingAfterCleanup(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, lease, now := botRunFixture(t, scope)
			disk := purgeTestDisk(t, repo, ctx, run.BotID)
			deleted, teardown := cleanupDeleteFixture(t, repo, ctx, run, lease)
			if n, err := repo.PurgeDue(ctx, *now, 10); err != nil || n != 0 {
				t.Fatalf("recoverable purge: %d %v", n, err)
			}
			if _, err := disk.root.Lstat(disk.namespace()); err != nil {
				t.Fatal("recoverable staging removed", err)
			}
			*now = deleted.RestoreUntil.Add(time.Second)
			runtime := cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return nil })
			if err := repo.ConfirmCleanup(runtime, teardown); err != nil {
				t.Fatal(err)
			}
			claim, err := repo.ReserveHomeCleanup(runtime, deleted.Home, deleted.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.ConfirmCleanup(runtime, claim.Request); err != nil {
				t.Fatal(err)
			}
			if n, err := repo.PurgeDue(ctx, *now, 10); err != nil || n != 1 {
				t.Fatalf("retention purge: %d %v", n, err)
			}
			if _, err := disk.root.Lstat(disk.namespace()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("retention staging survived: %v", err)
			}
		})
	}
}
