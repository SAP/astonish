package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func cleanupRuntimeContext(t *testing.T, verify store.BotCleanupVerifier) context.Context {
	t.Helper()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{
		Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodLocalOS,
		Surface: execution.SurfaceCode, ClientID: "cleanup-fixture", OrgSlug: "org", TeamSlug: "team",
		Scopes: []string{store.BotCleanupScope}, Authenticated: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if verify != nil {
		ctx = store.WithBotCleanupVerifier(ctx, verify)
	}
	return ctx
}

func cleanupDeleteFixture(t *testing.T, repo *botRepository, ctx context.Context, run *store.BotRun, lease store.BotLease) (*store.Bot, store.BotCleanupRequest) {
	t.Helper()
	// The baseline run fixture deliberately has no runtime home binding. Install
	// an opaque fixture binding, not a physical directory or deletion claim.
	home, err := botJSONMap(store.BotHomeBinding{ID: uuid.New(), BotID: run.BotID, Scope: repo.auth.scope, Backend: "fixture", StorageKey: "fixture-home", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	switch db := repo.db.(type) {
	case teamBotDB:
		err = db.client.Bot.UpdateOneID(run.BotID).SetHomeBinding(home).Exec(ctx)
	case personalBotDB:
		err = db.client.Bot.UpdateOneID(run.BotID).SetHomeBinding(home).Exec(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := repo.BeginDelete(ctx, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "test cleanup seam"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := repo.db.GetBot(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return deleted, store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: b.ID, ExpectedRevision: deleted.Revision, Fence: rec.Fence, Home: deleted.Home, RunID: run.ID, RunFence: lease.Fence}
}

func TestBotCleanupTrustedRetentionAndReplay(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, _, run, lease, now := botRunFixture(t, scope)
			deleted, teardown := cleanupDeleteFixture(t, repo, owner, run, lease)
			calls := 0
			runtime := cleanupRuntimeContext(t, func(_ context.Context, q store.BotCleanupRequest) error {
				calls++ // Fixture attestation only: there is no production sandbox.
				if q.BotID != deleted.ID || q.Home != deleted.Home {
					t.Fatal("callback was not bound to exact home")
				}
				return nil
			})
			home := teardown
			home.Kind, home.RunID, home.RunFence = store.BotCleanupHome, uuid.Nil, 0
			if err := repo.ConfirmCleanup(runtime, home); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("early home receipt: %v", err)
			}
			if err := repo.ConfirmCleanup(runtime, teardown); err != nil {
				t.Fatal(err)
			}
			rec, err := repo.db.GetBot(owner, deleted.ID)
			if err != nil || rec.ActiveRunID != nil || rec.LeaseExpiresAt != nil {
				t.Fatalf("teardown barrier: %+v %v", rec, err)
			}
			sequence := rec.LastSequence
			if err := repo.ConfirmCleanup(runtime, teardown); err != nil {
				t.Fatalf("teardown replay: %v", err)
			}
			rec, _ = repo.db.GetBot(owner, deleted.ID)
			if rec.LastSequence != sequence || calls != 1 {
				t.Fatal("replay duplicated callback/activity")
			}
			// After teardown the separate home receipt still cannot be accepted
			// before retention expires.
			if err := repo.ConfirmCleanup(runtime, home); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("home retired during retention: %v", err)
			}
			if n, err := repo.PurgeDue(owner, *now, 10); err != nil || n != 0 {
				t.Fatalf("early purge: %d %v", n, err)
			}
			*now = *deleted.RestoreUntil
			if n, err := repo.PurgeDue(owner, *now, 10); err != nil || n != 0 {
				t.Fatalf("run teardown authorized home purge: %d %v", n, err)
			}
			if err := repo.ConfirmCleanup(runtime, home); err != nil {
				t.Fatal(err)
			}
			if err := repo.ConfirmCleanup(runtime, home); err != nil || calls != 2 {
				t.Fatalf("home replay: %v calls=%d", err, calls)
			}
			if n, err := repo.PurgeDue(owner, *now, 10); err != nil || n != 1 {
				t.Fatalf("confirmed purge: %d %v", n, err)
			}
			if _, err := repo.Get(owner, deleted.ID); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("tombstone still exists: %v", err)
			}
			if n, err := repo.PurgeDue(owner, *now, 10); err != nil || n != 0 {
				t.Fatalf("purge replay: %d %v", n, err)
			}
		})
	}
}

func TestBotCleanupRejectsUntrustedAndStale(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, run, lease, _ := botRunFixture(t, scope)
			_, q := cleanupDeleteFixture(t, repo, owner, run, lease)
			calls := 0
			verify := func(context.Context, store.BotCleanupRequest) error { calls++; return nil }
			runtime := cleanupRuntimeContext(t, verify)
			for name, ctx := range map[string]context.Context{
				"absent": context.Background(), "owner": store.WithBotCleanupVerifier(owner, verify),
				"worker": store.WithBotCleanupVerifier(worker, verify), "missing_verifier": cleanupRuntimeContext(t, nil),
			} {
				t.Run(name, func(t *testing.T) {
					if err := repo.ConfirmCleanup(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("untrusted confirmation: %v", err)
					}
				})
			}
			for name, change := range map[string]func(*execution.Principal){
				"wrong_org":      func(p *execution.Principal) { p.OrgSlug = "other" },
				"wrong_team":     func(p *execution.Principal) { p.TeamSlug = "other" },
				"missing_scope":  func(p *execution.Principal) { p.Scopes = nil },
				"public_surface": func(p *execution.Principal) { p.Surface = execution.SurfaceStudio },
			} {
				t.Run(name, func(t *testing.T) {
					p, _ := execution.PrincipalFromContext(runtime)
					change(&p)
					ctx, err := execution.WithPrincipal(runtime, p)
					if err != nil {
						t.Fatal(err)
					}
					if err := repo.ConfirmCleanup(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("authority accepted: %v", err)
					}
				})
			}
			for name, change := range map[string]func(*store.BotCleanupRequest){
				"revision":      func(q *store.BotCleanupRequest) { q.ExpectedRevision++ },
				"fence":         func(q *store.BotCleanupRequest) { q.Fence++ },
				"home":          func(q *store.BotCleanupRequest) { q.Home.ID = uuid.New() },
				"home_revision": func(q *store.BotCleanupRequest) { q.Home.Revision++ },
				"storage":       func(q *store.BotCleanupRequest) { q.Home.StorageKey += "other" },
				"run":           func(q *store.BotCleanupRequest) { q.RunID = uuid.New() },
				"run_fence":     func(q *store.BotCleanupRequest) { q.RunFence++ },
			} {
				t.Run(name, func(t *testing.T) {
					stale := q
					change(&stale)
					if err := repo.ConfirmCleanup(runtime, stale); err == nil {
						t.Fatal("stale receipt accepted")
					}
				})
			}
			if calls != 0 {
				t.Fatal("invalid receipt reached verifier")
			}
			failure := errors.New("workers still alive")
			if err := repo.ConfirmCleanup(cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return failure }), q); !errors.Is(err, failure) {
				t.Fatalf("verification failure: %v", err)
			}
			rec, err := repo.db.GetBot(owner, q.BotID)
			if err != nil || rec.ActiveRunID == nil {
				t.Fatalf("untrusted request cleared barrier: %+v %v", rec, err)
			}
			// Exercise the exported optional capability on each concrete store too.
			var capability store.BotCleanupStore
			switch db := repo.db.(type) {
			case teamBotDB:
				capability = &teamBotStore{client: db.client, auth: repo.auth}
			case personalBotDB:
				capability = &personalBotStore{client: db.client, auth: repo.auth}
			}
			if err := capability.ConfirmCleanup(runtime, q); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Force the receipt/root mutation to roll back if its audit event cannot commit.
type cleanupFailAuditDB struct {
	botDraftDB
	botCleanupDB
}

func (d cleanupFailAuditDB) InsertActivity(context.Context, store.BotActivity, string) error {
	return errors.New("cleanup audit unavailable")
}

type cleanupFailAuditTransaction struct {
	botDraftDB
	botCleanupDB
}

func (d cleanupFailAuditTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error {
		return fn(cleanupFailAuditDB{botDraftDB: tx, botCleanupDB: tx.(botCleanupDB)})
	})
}

func TestBotCleanupAuditRollbackAndBusyRuns(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, _, run, lease, now := botRunFixture(t, scope)
			deleted, q := cleanupDeleteFixture(t, repo, owner, run, lease)
			original := repo.db
			runtime := cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return nil })
			repo.db = cleanupFailAuditTransaction{botDraftDB: original, botCleanupDB: original.(botCleanupDB)}
			if err := repo.ConfirmCleanup(runtime, q); err == nil {
				t.Fatal("audit failure not returned")
			}
			repo.db = original
			rec, err := repo.db.GetBot(owner, q.BotID)
			if err != nil || rec.ActiveRunID == nil {
				t.Fatalf("barrier lost on rollback: %+v %v", rec, err)
			}
			if _, exists := rec.Metadata[botCleanupReceiptKey(q.Kind)]; exists {
				t.Fatal("receipt survived rollback")
			}
			*now = *deleted.RestoreUntil
			home := q
			home.Kind, home.RunID, home.RunFence = store.BotCleanupHome, uuid.Nil, 0
			if err := repo.ConfirmCleanup(runtime, home); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("expiry bypassed run barrier: %v", err)
			}
			if err := repo.ConfirmCleanup(runtime, q); err != nil {
				t.Fatal(err)
			}
			// A pending worker record independently prevents final confirmation.
			switch db := repo.db.(type) {
			case teamBotDB:
				err = db.client.BotRun.UpdateOneID(run.ID).SetStatus("queued").Exec(owner)
			case personalBotDB:
				err = db.client.BotRun.UpdateOneID(run.ID).SetStatus("queued").Exec(owner)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.ConfirmCleanup(runtime, home); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("busy home confirmed: %v", err)
			}
			if n, err := repo.PurgeDue(owner, *now, 10); err != nil || n != 0 {
				t.Fatalf("busy bot purged: %d %v", n, err)
			}
		})
	}
}

func TestBotCleanupRestoreRaceInvalidatesReceipt(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, _, run, lease, now := botRunFixture(t, scope)
			deleted, q := cleanupDeleteFixture(t, repo, owner, run, lease)
			runtime := cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error {
				_, err := repo.Restore(owner, store.BotLifecycleRequest{BotID: deleted.ID, ExpectedRevision: deleted.Revision, ActorID: deleted.OwnerID, Reason: "racing restore"})
				return err
			})
			if err := repo.ConfirmCleanup(runtime, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("racing receipt accepted: %v", err)
			}
			rec, err := repo.db.GetBot(owner, q.BotID)
			if err != nil || rec.Bot.Status == store.BotDeleted || rec.ActiveRunID == nil {
				t.Fatalf("restore/barrier lost: %+v %v", rec, err)
			}
			if _, ok := rec.Metadata[botCleanupReceiptKey(q.Kind)]; ok {
				t.Fatal("stale receipt persisted")
			}
			// A later deletion is a new retention interval/fence, never the old receipt.
			_, err = repo.BeginDelete(owner, store.BotLifecycleRequest{BotID: deleted.ID, ExpectedRevision: rec.Bot.Revision, ActorID: deleted.OwnerID, Reason: "delete again"})
			if err != nil {
				t.Fatal(err)
			}
			*now = now.Add(store.BotRetention + time.Second)
			if err := repo.ConfirmCleanup(cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return nil }), q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("re-delete accepted prior receipt: %v", err)
			}
			if n, err := repo.PurgeDue(owner, *now, 10); err != nil || n != 0 {
				t.Fatalf("restored tombstone purged: %d %v", n, err)
			}
		})
	}
}
