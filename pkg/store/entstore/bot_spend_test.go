package entstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type spendFixture struct {
	repo          *botRepository
	api           store.BotSpendStore
	owner, worker context.Context
	q             store.BotSpendRequest
	now           time.Time
	reopen        func()
}

func newSpendFixture(t *testing.T, scope store.BotScope, runCap, monthCap int64) *spendFixture {
	t.Helper()
	f := &spendFixture{now: time.Now().UTC().Truncate(time.Millisecond)}
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "spend-org", TeamSlug: "spend-team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	f.owner = ctx
	auth := botRepositoryAuth{orgSlug: "spend-org", teamSlug: "spend-team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}
	f.repo = &botRepository{auth: auth, now: func() time.Time { return f.now }}
	path := filepath.Join(t.TempDir(), "spend.db")
	var closeDB func() error
	open := func() {
		db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(8)
		drv := entsql.OpenDB(dialect.SQLite, db)
		if scope == store.BotScopeTeam {
			c := teament.NewClient(teament.Driver(drv))
			closeDB = c.Close
			if err = c.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			f.repo.db = teamBotDB{client: c}
			f.api = &teamBotStore{client: c, auth: auth}
		} else {
			c := personalent.NewClient(personalent.Driver(drv))
			closeDB = c.Close
			if err = c.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			f.repo.db = personalBotDB{client: c}
			f.api = &personalBotStore{client: c, auth: auth}
		}
	}
	open()
	t.Cleanup(func() { _ = closeDB() })
	f.reopen = func() {
		if err := closeDB(); err != nil {
			t.Fatal(err)
		}
		open()
	}
	v := store.DefaultBotVersion()
	v.Charter = "Purchase ledger fixture"
	v.Autonomy.SpendCurrency = "USD"
	v.Autonomy.MaxSpendPerRunMinor = runCap
	v.Autonomy.MaxSpendPerMonthMinor = monthCap
	// Seed invalid-cap versions only in tests, bypassing unrelated charter validation.
	createVersion := v
	if runCap < 0 {
		createVersion.Autonomy.MaxSpendPerRunMinor = 0
	}
	if monthCap < 0 {
		createVersion.Autonomy.MaxSpendPerMonthMinor = 0
	}
	b, err := f.repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Spend", OwnerID: owner, Version: createVersion, IdempotencyKey: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	// Draft creation deliberately strips purchase caps. Seed a second immutable
	// reviewed snapshot directly: runtime review/activation is outside this test.
	v.BotID, v.Version, v.CreatedBy, v.CreatedAt = b.ID, 2, owner, f.now
	if err = f.repo.db.InsertVersion(ctx, v, "reviewed-spend-fixture"); err != nil {
		t.Fatal(err)
	}
	switch d := f.repo.db.(type) {
	case teamBotDB:
		err = d.client.Bot.UpdateOneID(b.ID).SetStatus("active").SetVersion(2).Exec(ctx)
	case personalBotDB:
		err = d.client.Bot.UpdateOneID(b.ID).SetStatus("active").SetVersion(2).Exec(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: 2, Trigger: store.TriggerManual, IdempotencyKey: "run", Instructions: "Test ledger"})
	if err != nil {
		t.Fatal(err)
	}
	f.worker = execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: 2, ActorID: owner.String(), OrgSlug: "spend-org", TeamSlug: "spend-team"})
	lease, err := f.repo.ClaimRun(f.worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "spend-worker", ExpiresAt: f.now.Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	f.q = store.BotSpendRequest{BotID: b.ID, RunID: run.ID, Version: 2, Key: "purchase", Currency: "USD", Month: f.now.Format(botSpendMonthLayout), AmountMinor: 60, ActionDigest: strings.Repeat("a", 64), Lease: &lease}
	return f
}

func eachSpendScope(t *testing.T, fn func(*testing.T, store.BotScope)) {
	t.Helper()
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) { fn(t, scope) })
	}
}

func TestBotSpendDurableSettlement(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newSpendFixture(t, scope, 100, 100)
		e, err := f.api.ReserveSpend(f.worker, f.q)
		if err != nil || e.State != store.BotSpendReserved {
			t.Fatalf("reserve: %+v %v", e, err)
		}
		again, err := f.api.ReserveSpend(f.worker, f.q)
		if err != nil || *again != *e {
			t.Fatalf("retry: %+v %v", again, err)
		}
		unknown := store.BotSpendResolution{BotID: f.q.BotID, Key: f.q.Key, Outcome: store.BotSpendOutcomeUnknown, Lease: f.q.Lease, Evidence: "timeout"}
		if e, err = f.api.ResolveSpend(f.worker, unknown); err != nil || e.State != store.BotSpendUncertain {
			t.Fatalf("unknown: %+v %v", e, err)
		}
		f.reopen()
		e, err = f.api.GetSpend(f.owner, f.q.BotID, f.q.Key)
		if err != nil || e.State != store.BotSpendUncertain || e.AmountMinor != 60 {
			t.Fatalf("reopen: %+v %v", e, err)
		}
		more := f.q
		more.Key = "more"
		more.AmountMinor = 41
		if _, err = f.api.ReserveSpend(f.worker, more); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("uncertain freed budget: %v", err)
		}
		settle := store.BotSpendResolution{BotID: f.q.BotID, Key: f.q.Key, Outcome: store.BotSpendOutcomeCharged, ChargedMinor: 40, Evidence: "receipt:40;remainder:no-effect", Lease: f.q.Lease}
		e, err = f.api.ResolveSpend(f.worker, settle)
		if err != nil || e.State != store.BotSpendSettled || e.ChargedMinor != 40 {
			t.Fatalf("settle: %+v %v", e, err)
		}
		if retry, err := f.api.ResolveSpend(f.worker, settle); err != nil || *retry != *e {
			t.Fatalf("settle retry: %+v %v", retry, err)
		}
		settle.ChargedMinor = 39
		if _, err = f.api.ResolveSpend(f.worker, settle); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("changed settlement: %v", err)
		}
		more.AmountMinor = 60
		if _, err = f.api.ReserveSpend(f.worker, more); err != nil {
			t.Fatal(err)
		}
		release := store.BotSpendResolution{BotID: more.BotID, Key: more.Key, Outcome: store.BotSpendOutcomeNoEffect, Evidence: "provider confirms not submitted"}
		if e, err = f.api.ResolveSpend(f.owner, release); err != nil || e.State != store.BotSpendReleased {
			t.Fatalf("release: %+v %v", e, err)
		}
		if _, err = f.api.ResolveSpend(f.owner, release); err != nil {
			t.Fatal(err)
		}
		more.AmountMinor = 59
		if _, err = f.api.ReserveSpend(f.worker, more); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("released key reused: %v", err)
		}
		f.reopen()
		e, err = f.api.GetSpend(f.owner, f.q.BotID, f.q.Key)
		if err != nil || e.ChargedMinor != 40 {
			t.Fatalf("settlement durability: %+v %v", e, err)
		}
	})
}

func TestBotSpendBindingAndAuthorization(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newSpendFixture(t, scope, 100, 100)
		if _, err := f.api.ReserveSpend(f.worker, f.q); err != nil {
			t.Fatal(err)
		}
		mutations := []func(*store.BotSpendRequest){func(q *store.BotSpendRequest) { q.AmountMinor++ }, func(q *store.BotSpendRequest) { q.Currency = "EUR" }, func(q *store.BotSpendRequest) { q.Month = "2000-01" }, func(q *store.BotSpendRequest) { q.RunID = uuid.New() }, func(q *store.BotSpendRequest) { q.Version++ }, func(q *store.BotSpendRequest) { q.ActionDigest = strings.Repeat("b", 64) }}
		for i, mutate := range mutations {
			q := f.q
			q.Lease = nil
			mutate(&q)
			if _, err := f.api.ReserveSpend(f.owner, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("binding %d: %v", i, err)
			}
		}
		q := f.q
		q.Key = "fresh"
		q.AmountMinor = 1
		if _, err := f.api.ReserveSpend(context.Background(), q); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("anonymous: %v", err)
		}
		if _, err := f.api.ReserveSpend(f.owner, q); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("lease without identity: %v", err)
		}
		q.Lease = nil
		if _, err := f.api.ReserveSpend(f.worker, q); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("worker owner fallback: %v", err)
		}
		if _, err := f.api.ReserveSpend(f.owner, q); err != nil {
			t.Fatalf("owner: %v", err)
		}
		q.Key = "stale-version"
		q.Version++
		if _, err := f.api.ReserveSpend(f.owner, q); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("reviewed version: %v", err)
		}
		q = f.q
		q.Key = "stale-lease"
		q.AmountMinor = 1
		lease := *q.Lease
		lease.Fence++
		q.Lease = &lease
		if _, err := f.api.ReserveSpend(f.worker, q); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("fence: %v", err)
		}
		f.now = f.now.Add(6 * time.Minute)
		if _, err := f.repo.ReserveSpend(f.worker, f.q); !errors.Is(err, store.ErrBotExpired) {
			t.Fatalf("expired retry: %v", err)
		}
		resolve := store.BotSpendResolution{BotID: f.q.BotID, Key: f.q.Key, Outcome: store.BotSpendOutcomeNoEffect, Evidence: "confirmed absent", Lease: f.q.Lease}
		if _, err := f.repo.ResolveSpend(f.worker, resolve); !errors.Is(err, store.ErrBotExpired) {
			t.Fatalf("expired resolve: %v", err)
		}
		resolve.Lease = nil
		if _, err := f.repo.ResolveSpend(f.owner, resolve); err != nil {
			t.Fatalf("owner reconciliation: %v", err)
		}
		f.repo.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
		if _, err := f.repo.ResolveSpend(f.owner, resolve); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("demoted owner: %v", err)
		}
	})
}

func TestBotSpendCapsAndValidation(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, caps := range [][2]int64{{0, 100}, {100, 0}, {-1, 100}, {100, -1}, {50, 100}, {100, 50}} {
			t.Run(fmt.Sprint(caps), func(t *testing.T) {
				f := newSpendFixture(t, scope, caps[0], caps[1])
				if _, err := f.api.ReserveSpend(f.worker, f.q); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("cap denied: %v", err)
				}
			})
		}
		f := newSpendFixture(t, scope, 100, 100)
		for i, mutate := range []func(*store.BotSpendRequest){func(q *store.BotSpendRequest) { q.AmountMinor = 0 }, func(q *store.BotSpendRequest) { q.AmountMinor = -1 }, func(q *store.BotSpendRequest) { q.Currency = "usd" }, func(q *store.BotSpendRequest) { q.Month = "2026-1" }, func(q *store.BotSpendRequest) { q.ActionDigest = "arbitrary" }, func(q *store.BotSpendRequest) { q.Key = " " }, func(q *store.BotSpendRequest) { q.BotID = uuid.Nil }} {
			q := f.q
			mutate(&q)
			if _, err := f.api.ReserveSpend(f.worker, q); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("invalid %d: %v", i, err)
			}
		}
		q := f.q
		q.Currency = "EUR"
		if _, err := f.api.ReserveSpend(f.worker, q); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("currency: %v", err)
		}
		q = f.q
		q.Month = "2000-01"
		if _, err := f.api.ReserveSpend(f.worker, q); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("backdate: %v", err)
		}
		if _, err := f.api.ReserveSpend(f.worker, f.q); err != nil {
			t.Fatal(err)
		}
		for _, resolution := range []store.BotSpendResolution{{Outcome: store.BotSpendOutcomeNoEffect}, {Outcome: store.BotSpendOutcomeNoEffect, ChargedMinor: 1, Evidence: "bad"}, {Outcome: store.BotSpendOutcomeCharged, ChargedMinor: 61, Evidence: "over"}, {Outcome: store.BotSpendOutcomeUnknown, ChargedMinor: 1}, {Outcome: store.BotSpendOutcomeCharged, Evidence: "zero"}, {Outcome: "invented"}} {
			resolution.BotID = f.q.BotID
			resolution.Key = f.q.Key
			if _, err := f.api.ResolveSpend(f.owner, resolution); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("invalid resolution %+v: %v", resolution, err)
			}
		}
		e, err := f.api.GetSpend(f.owner, f.q.BotID, f.q.Key)
		if err != nil || e.State != store.BotSpendReserved {
			t.Fatalf("invalid freed reservation: %+v %v", e, err)
		}
	})
}

func TestBotSpendConcurrentRetries(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newSpendFixture(t, scope, 100, 100)
		const count = 24
		results := make(chan error, count)
		var wg sync.WaitGroup
		for i := 0; i < count; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				q := f.q
				q.Key = fmt.Sprintf("purchase-%d", i)
				q.AmountMinor = 10
				_, err := f.api.ReserveSpend(f.worker, q)
				results <- err
			}(i)
		}
		wg.Wait()
		close(results)
		success := 0
		for err := range results {
			if err == nil {
				success++
			} else if !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("concurrent reserve: %v", err)
			}
		}
		if success != 10 {
			t.Fatalf("successful reservations=%d want 10", success)
		}
		rec, err := f.repo.db.GetBot(f.owner, f.q.BotID)
		if err != nil {
			t.Fatal(err)
		}
		ledger, err := readBotSpend(rec)
		if err != nil || len(ledger.Entries) != 10 {
			t.Fatalf("ledger: %+v %v", ledger, err)
		}
		var existing store.BotSpendReservation
		for _, e := range ledger.Entries {
			existing = e
			break
		}
		results = make(chan error, count)
		for i := 0; i < count; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				q := f.q
				q.Key = existing.Key
				q.AmountMinor = 10
				_, err := f.api.ReserveSpend(f.worker, q)
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatalf("duplicate: %v", err)
			}
		}
		f.reopen()
		rec, err = f.repo.db.GetBot(f.owner, f.q.BotID)
		if err != nil {
			t.Fatal(err)
		}
		ledger, err = readBotSpend(rec)
		if err != nil || len(ledger.Entries) != 10 {
			t.Fatalf("durable concurrent ledger: %+v %v", ledger, err)
		}
	})
}

func TestBotSpendExactAmountsAndMonth(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newSpendFixture(t, scope, math.MaxInt64, math.MaxInt64)
		q := f.q
		q.AmountMinor = math.MaxInt64 - 1
		if _, err := f.api.ReserveSpend(f.worker, q); err != nil {
			t.Fatal(err)
		}
		f.reopen()
		e, err := f.api.GetSpend(f.owner, q.BotID, q.Key)
		if err != nil || e.AmountMinor != q.AmountMinor {
			t.Fatalf("rounded: %+v %v", e, err)
		}
		extra := q
		extra.Key = "overflow"
		extra.AmountMinor = 2
		if _, err = f.api.ReserveSpend(f.worker, extra); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("overflow: %v", err)
		}
		extra.AmountMinor = 1
		if _, err = f.api.ReserveSpend(f.worker, extra); err != nil {
			t.Fatal(err)
		}
		// Month is store-clock UTC, not caller supplied; per-run totals span months.
		f.now = time.Date(f.now.Year(), f.now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		extra.Key = "next-month"
		extra.Month = f.now.Format(botSpendMonthLayout)
		extra.Lease = nil
		if _, err = f.repo.ReserveSpend(f.owner, extra); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("per-run reset at month: %v", err)
		}
		old := q
		old.Lease = nil
		if _, err = f.repo.ReserveSpend(f.owner, old); err != nil {
			t.Fatalf("old-month idempotency: %v", err)
		}
	})
}

func TestBotSpendMetadataCASAndRollback(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newSpendFixture(t, scope, 100, 100)
		rec, err := f.repo.db.GetBot(f.owner, f.q.BotID)
		if err != nil {
			t.Fatal(err)
		}
		stale := *rec
		if err = f.repo.MarkRead(f.owner, store.BotReadCursor{BotID: rec.Bot.ID, ViewerID: rec.Bot.OwnerID, Sequence: rec.LastSequence}); err != nil {
			t.Fatal(err)
		}
		if err = f.repo.db.(botSpendWriter).writeSpend(f.owner, stale, map[string]any{}); !errors.Is(err, errBotSpendCAS) {
			t.Fatalf("stale metadata CAS: %v", err)
		}
		if _, err = f.api.ReserveSpend(f.worker, f.q); err != nil {
			t.Fatal(err)
		}
		rec, err = f.repo.db.GetBot(f.owner, f.q.BotID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.CreateHash == "" || rec.Metadata["read_cursors"] == nil {
			t.Fatalf("metadata lost: %+v", rec.Metadata)
		}
		original := f.repo.db
		f.repo.db = spendRollbackDB{original}
		q := f.q
		q.Key = "rollback"
		q.AmountMinor = 10
		if _, err = f.repo.ReserveSpend(f.worker, q); err == nil {
			t.Fatal("missing injected error")
		}
		f.repo.db = original
		if _, err = f.repo.GetSpend(f.owner, q.BotID, q.Key); !errors.Is(err, store.ErrBotNotFound) {
			t.Fatalf("partial transaction: %v", err)
		}
		// A malformed persisted ledger fails closed instead of resetting the budget.
		switch d := f.repo.db.(type) {
		case teamBotDB:
			err = d.client.Bot.UpdateOneID(q.BotID).SetData(map[string]any{botSpendMetadataKey: map[string]any{}}).Exec(f.owner)
		case personalBotDB:
			err = d.client.Bot.UpdateOneID(q.BotID).SetData(map[string]any{botSpendMetadataKey: map[string]any{}}).Exec(f.owner)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.api.ReserveSpend(f.worker, q); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("corrupt ledger: %v", err)
		}
	})
}

func TestBotSpendMonthlyAcrossRunsAndIsolation(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newSpendFixture(t, scope, 100, 100)
		if _, err := f.api.ReserveSpend(f.worker, f.q); err != nil {
			t.Fatal(err)
		}
		rec, err := f.repo.db.GetBot(f.owner, f.q.BotID)
		if err != nil {
			t.Fatal(err)
		}
		// A second run consumes the same Bot/month budget, not a fresh allowance.
		otherRun := store.BotRun{ID: uuid.New(), BotID: f.q.BotID, Version: f.q.Version, SessionID: uuid.New(), Trigger: store.TriggerManual, IdempotencyKey: "second", Status: store.RunRunning, CreatedAt: f.now}
		if err = f.repo.db.InsertRun(f.owner, botStoredRun{Run: otherRun, RequestHash: "second"}, rec.Bot.OwnerID); err != nil {
			t.Fatal(err)
		}
		switch d := f.repo.db.(type) {
		case teamBotDB:
			err = d.client.BotRun.UpdateOneID(otherRun.ID).SetStatus("running").Exec(f.owner)
		case personalBotDB:
			err = d.client.BotRun.UpdateOneID(otherRun.ID).SetStatus("running").Exec(f.owner)
		}
		if err != nil {
			t.Fatal(err)
		}
		q := f.q
		q.RunID = otherRun.ID
		q.Key = "second-run"
		q.AmountMinor = 41
		q.Lease = nil
		if _, err = f.repo.ReserveSpend(f.owner, q); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("monthly allowance reset for run: %v", err)
		}
		// An independent Bot scope cannot read or resolve this Bot's ledger.
		isolated := newSpendFixture(t, scope, 100, 100)
		if _, err = isolated.api.GetSpend(isolated.owner, f.q.BotID, f.q.Key); !errors.Is(err, store.ErrBotNotFound) {
			t.Fatalf("cross-scope lookup: %v", err)
		}
		// A new UTC month has its own monthly allowance, while the previous
		// reservation and its idempotency key remain intact.
		f.now = time.Date(f.now.Year(), f.now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		q.Month = f.now.Format(botSpendMonthLayout)
		if _, err = f.repo.ReserveSpend(f.owner, q); err != nil {
			t.Fatalf("new month: %v", err)
		}
		old, err := f.repo.GetSpend(f.owner, f.q.BotID, f.q.Key)
		if err != nil || old.State != store.BotSpendReserved {
			t.Fatalf("month discarded old reservation: %+v %v", old, err)
		}
		// Non-owner team members cannot invoke owner reconciliation privileges.
		if scope == store.BotScopeTeam {
			principal, _ := execution.PrincipalFromContext(f.owner)
			principal.Subject = uuid.NewString()
			outsider, err := execution.WithPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			resolution := store.BotSpendResolution{BotID: f.q.BotID, Key: f.q.Key, Outcome: store.BotSpendOutcomeNoEffect, Evidence: "claimed"}
			if _, err = f.repo.ResolveSpend(outsider, resolution); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("nonowner resolution: %v", err)
			}
		}
		// Pausing must retain pending spend; only the owner can reconcile it.
		switch d := f.repo.db.(type) {
		case teamBotDB:
			err = d.client.Bot.UpdateOneID(f.q.BotID).SetStatus("paused").AddRevision(1).AddLastSeq(1).AddFence(1).Exec(f.owner)
		case personalBotDB:
			err = d.client.Bot.UpdateOneID(f.q.BotID).SetStatus("paused").AddRevision(1).AddLastSeq(1).AddFence(1).Exec(f.owner)
		}
		if err != nil {
			t.Fatal(err)
		}
		old, err = f.repo.GetSpend(f.owner, f.q.BotID, f.q.Key)
		if err != nil || old.State != store.BotSpendReserved {
			t.Fatalf("pause discarded reservation: %+v %v", old, err)
		}
		resolution := store.BotSpendResolution{BotID: f.q.BotID, Key: f.q.Key, Outcome: store.BotSpendOutcomeNoEffect, Evidence: "confirmed absent after pause"}
		if _, err = f.repo.ResolveSpend(f.owner, resolution); err != nil {
			t.Fatal(err)
		}
	})
}

type spendRollbackDB struct{ botDraftDB }

func (d spendRollbackDB) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errors.New("injected rollback after spend write")
	})
}
