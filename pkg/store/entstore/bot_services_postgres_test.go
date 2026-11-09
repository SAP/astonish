//go:build integration

package entstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// These fixtures deliberately use the live router, not newSpendFixture or
// botRunFixture (which create SQLite clients). The existing PG harness owns all
// generated database names and their cleanup, including the restorepg org DB.
type postgresBotServicesFixture struct {
	platform      *Store
	scope         store.BotScope
	repo          *botRepository
	bots          store.BotStore
	memories      store.MemoryStore
	owner, worker context.Context
	run           *store.BotRun
	lease         store.BotLease
	q             store.BotSpendRequest
}

func newPostgresBotServicesFixture(t *testing.T, scope store.BotScope, runCap, monthCap int64) *postgresBotServicesFixture {
	t.Helper()
	s := setupPostgresBackupRestoreStore(t, "bot_services")
	ctx := context.Background()
	owner, orgID := uuid.New(), uuid.NewString()
	if err := s.Users().Create(ctx, &store.User{ID: owner.String(), Email: "bot-services@example.test", DisplayName: "Services owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Services", Slug: "restorepg", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Organizations().AddMember(ctx, owner.String(), orgID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.ProvisionOrg(ctx, orgID, "restorepg"); err != nil {
		t.Fatal(err)
	}
	org, err := s.ForOrg("restorepg")
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Name: "Ops", Slug: "ops", SchemaName: teamSchemaName("ops")}
	if err := org.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := org.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := org.ProvisionTeam(ctx, "ops"); err != nil {
		t.Fatal(err)
	}
	if err := org.ProvisionPersonalSchema(ctx, owner.String()); err != nil {
		t.Fatal(err)
	}
	f := &postgresBotServicesFixture{platform: s, scope: scope}
	f.owner, err = execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "restorepg", TeamSlug: "ops", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	if scope == store.BotScopeTeam {
		routed := org.ForTeam("ops")
		f.bots, f.memories = routed.Bots(), routed.Memories()
		f.repo = f.bots.(*teamBotStore).repository()
		if f.memories.(*teamMemoryStore).dialect != DialectPostgres {
			t.Fatal("team memory is not PostgreSQL")
		}
	} else {
		routed := org.ForUser(owner.String())
		f.bots, f.memories = routed.Bots(), routed.Memories()
		f.repo = f.bots.(*personalBotStore).repository()
		if f.memories.(*personalMemoryStore).dialect != DialectPostgres {
			t.Fatal("personal memory is not PostgreSQL")
		}
	}
	v := store.DefaultBotVersion()
	v.Charter = "Verify durable service accounting"
	b, err := f.bots.CreateDraft(f.owner, store.BotCreateRequest{Name: "Services", OwnerID: owner, Version: v, IdempotencyKey: "services"})
	if err != nil {
		t.Fatal(err)
	}
	// Runtime review is outside this persistence test. Draft creation strips caps,
	// so seed an immutable reviewed snapshot exactly as the spend unit fixture does.
	v.BotID, v.Version, v.CreatedBy, v.CreatedAt = b.ID, 2, owner, time.Now().UTC()
	v.Autonomy.SpendCurrency, v.Autonomy.MaxSpendPerRunMinor, v.Autonomy.MaxSpendPerMonthMinor = "USD", runCap, monthCap
	if err := f.repo.db.InsertVersion(f.owner, v, "reviewed-services"); err != nil {
		t.Fatal(err)
	}
	switch d := f.repo.db.(type) {
	case teamBotDB:
		err = d.client.Bot.UpdateOneID(b.ID).SetStatus("active").SetVersion(2).Exec(f.owner)
	case personalBotDB:
		err = d.client.Bot.UpdateOneID(b.ID).SetStatus("active").SetVersion(2).Exec(f.owner)
	}
	if err != nil {
		t.Fatal(err)
	}
	f.run, err = f.bots.RequestRun(f.owner, store.BotRunRequest{BotID: b.ID, Version: 2, Trigger: store.TriggerManual, IdempotencyKey: "run", Instructions: "Observe ledger"})
	if err != nil {
		t.Fatal(err)
	}
	f.worker = execution.WithBotRunIdentity(f.owner, execution.BotRunIdentity{BotID: b.ID, RunID: f.run.ID, Version: 2, ActorID: owner.String(), OrgSlug: "restorepg", TeamSlug: "ops"})
	f.lease, err = f.repo.ClaimRun(f.worker, store.BotClaimRequest{BotID: b.ID, RunID: f.run.ID, WorkerID: "pg-services", ExpiresAt: time.Now().Add(4 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	f.q = store.BotSpendRequest{BotID: b.ID, RunID: f.run.ID, Version: 2, Key: "purchase", Currency: "USD", Month: time.Now().UTC().Format(botSpendMonthLayout), AmountMinor: 60, ActionDigest: strings.Repeat("a", 64), Lease: &f.lease}
	return f
}

func TestIntegrationPostgresBotServicesSpendRace(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newPostgresBotServicesFixture(t, scope, 100, 100)
		api := f.bots.(store.BotSpendStore)
		const attempts = 24
		type result struct {
			key string
			err error
		}
		results := make(chan result, attempts)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range attempts {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				q := f.q
				q.Key, q.AmountMinor = fmt.Sprintf("purchase-%d", i), 10
				_, err := api.ReserveSpend(f.worker, q)
				results <- result{q.Key, err}
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		successes, existing := 0, ""
		for r := range results {
			if r.err == nil {
				successes++
				existing = r.key
			} else if !errors.Is(r.err, store.ErrBotForbidden) {
				t.Errorf("concurrent reserve %s: %v", r.key, r.err)
			}
		}
		if successes != 10 {
			t.Fatalf("successful reservations=%d want 10", successes)
		}
		retryErrors := make(chan error, attempts)
		start = make(chan struct{})
		for range attempts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				q := f.q
				q.Key, q.AmountMinor = existing, 10
				_, err := api.ReserveSpend(f.worker, q)
				retryErrors <- err
			}()
		}
		close(start)
		wg.Wait()
		close(retryErrors)
		for err := range retryErrors {
			if err != nil {
				t.Errorf("duplicate reservation: %v", err)
			}
		}
		rec, err := f.repo.db.GetBot(f.owner, f.q.BotID)
		if err != nil {
			t.Fatal(err)
		}
		ledger, err := readBotSpend(rec)
		if err != nil || len(ledger.Entries) != 10 {
			t.Fatalf("persisted ledger: %+v %v", ledger, err)
		}
		var total int64
		for _, e := range ledger.Entries {
			total += e.AmountMinor
		}
		if total != 100 {
			t.Fatalf("reserved=%d want 100", total)
		}
	})
}

func TestIntegrationPostgresBotServicesSpendSettlementAndCaps(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, caps := range [][2]int64{{100, 200}, {200, 100}} {
			t.Run(fmt.Sprintf("run%d_month%d", caps[0], caps[1]), func(t *testing.T) {
				f := newPostgresBotServicesFixture(t, scope, caps[0], caps[1])
				api := f.bots.(store.BotSpendStore)
				e, err := api.ReserveSpend(f.worker, f.q)
				if err != nil || e.State != store.BotSpendReserved {
					t.Fatalf("reserve: %+v %v", e, err)
				}
				q := store.BotSpendResolution{BotID: f.q.BotID, Key: f.q.Key, Outcome: store.BotSpendOutcomeUnknown, Lease: f.q.Lease, Evidence: "provider timed out"}
				if e, err = api.ResolveSpend(f.worker, q); err != nil || e.State != store.BotSpendUncertain {
					t.Fatalf("unknown: %+v %v", e, err)
				}
				more := f.q
				more.Key, more.AmountMinor = "more", 41
				if _, err := api.ReserveSpend(f.worker, more); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("uncertainty released cap: %v", err)
				}
				q.Outcome, q.ChargedMinor, q.Evidence = store.BotSpendOutcomeCharged, 40, "receipt:40; remainder:no-effect"
				if e, err = api.ResolveSpend(f.worker, q); err != nil || e.State != store.BotSpendSettled || e.ChargedMinor != 40 {
					t.Fatalf("settle: %+v %v", e, err)
				}
				if retry, err := api.ResolveSpend(f.worker, q); err != nil || *retry != *e {
					t.Fatalf("settlement replay: %+v %v", retry, err)
				}
				q.ChargedMinor = 39
				if _, err := api.ResolveSpend(f.worker, q); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("changed settlement: %v", err)
				}
				more.AmountMinor = 60
				if _, err := api.ReserveSpend(f.worker, more); err != nil {
					t.Fatal(err)
				}
				release := store.BotSpendResolution{BotID: more.BotID, Key: more.Key, Outcome: store.BotSpendOutcomeNoEffect, Evidence: "provider confirms absent"}
				for range 2 {
					if e, err := api.ResolveSpend(f.owner, release); err != nil || e.State != store.BotSpendReleased {
						t.Fatalf("release: %+v %v", e, err)
					}
				}
				more.AmountMinor = 59
				if _, err := api.ReserveSpend(f.worker, more); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("released key reused: %v", err)
				}
				// Read via a fresh repository; no in-memory ledger state is reused.
				fresh := &botRepository{auth: f.repo.auth, db: f.repo.db}
				if e, err := fresh.GetSpend(f.owner, f.q.BotID, f.q.Key); err != nil || e.State != store.BotSpendSettled || e.ChargedMinor != 40 {
					t.Fatalf("persisted settlement: %+v %v", e, err)
				}
			})
		}
	})
}

func TestIntegrationPostgresBotServicesCorrection(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newPostgresBotServicesFixture(t, scope, 100, 100)
		b, err := f.bots.Get(f.owner, f.run.BotID)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.memories.Add(f.owner, store.MemoryEntry{Content: "Thursday", Category: "delivery", SessionID: f.run.SessionID.String(), CreatedBy: b.OwnerID.String()}); err != nil {
			t.Fatal(err)
		}
		rows, err := f.memories.ListBySession(f.owner, f.run.SessionID.String())
		if err != nil || len(rows) != 1 {
			t.Fatalf("seed memory: %+v %v", rows, err)
		}
		q := store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: b.Revision, SupersededSource: "memory:" + rows[0].ID, Replacement: "Friday", Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Confirmed", Source: "owner-message:pg", ActorID: b.OwnerID, ObservedAt: time.Now().UTC()}}
		if err := f.bots.Correct(f.owner, q); err != nil {
			t.Fatal(err)
		}
		reconciler := f.bots.(store.BotCorrectionReconciler)
		failReceipt := true
		injected := errors.New("pg correction receipt unavailable")
		switch d := f.repo.db.(type) {
		case teamBotDB:
			d.client.BotOutbox.Use(func(next teament.Mutator) teament.Mutator {
				return teament.MutateFunc(func(ctx context.Context, m teament.Mutation) (teament.Value, error) {
					if failReceipt {
						return nil, injected
					}
					return next.Mutate(ctx, m)
				})
			})
		case personalBotDB:
			d.client.BotOutbox.Use(func(next personalent.Mutator) personalent.Mutator {
				return personalent.MutateFunc(func(ctx context.Context, m personalent.Mutation) (personalent.Value, error) {
					if failReceipt {
						return nil, injected
					}
					return next.Mutate(ctx, m)
				})
			})
		}
		if err := reconciler.ReconcileCorrection(f.owner, b.ID, q.ID, f.memories); !errors.Is(err, injected) {
			t.Fatalf("injected receipt failure: %v", err)
		}
		row, err := f.memories.Get(f.owner, rows[0].ID)
		if err != nil || row.Snippet != "Thursday" {
			t.Fatalf("partial memory commit: %+v %v", row, err)
		}
		if _, err := f.repo.db.FindActivity(f.owner, b.ID, "correction-memory:"+q.ID.String()); !errors.Is(err, store.ErrBotNotFound) {
			t.Fatalf("receipt survived rollback: %v", err)
		}
		failReceipt = false
		for range 2 {
			if err := reconciler.ReconcileCorrection(f.owner, b.ID, q.ID, f.memories); err != nil {
				t.Fatal(err)
			}
		}
		row, err = f.memories.Get(f.owner, rows[0].ID)
		if err != nil || row.Snippet != "Friday" || row.Category != "delivery" || row.SessionID != f.run.SessionID.String() {
			t.Fatalf("corrected memory provenance: %+v %v", row, err)
		}
		if _, err := f.repo.db.FindActivity(f.owner, b.ID, "correction-memory:"+q.ID.String()); err != nil {
			t.Fatalf("missing correction receipt: %v", err)
		}
		if err := f.memories.Update(f.owner, row.ID, "Saturday", row.Category); err != nil {
			t.Fatal(err)
		}
		if err := reconciler.ReconcileCorrection(f.owner, b.ID, q.ID, f.memories); err != nil {
			t.Fatal(err)
		}
		row, err = f.memories.Get(f.owner, row.ID)
		if err != nil || row.Snippet != "Saturday" {
			t.Fatalf("replay overwrote newer edit: %+v %v", row, err)
		}
		saved, err := f.bots.Get(f.owner, b.ID)
		if err != nil || saved.Version != b.Version || saved.Revision != b.Revision+1 {
			t.Fatalf("correction changed authority: %+v %v", saved, err)
		}
	})
}

func TestIntegrationPostgresBotServicesCleanup(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newPostgresBotServicesFixture(t, scope, 100, 100)
		deleted, teardown := cleanupDeleteFixture(t, f.repo, f.owner, f.run, f.lease)
		api := f.bots.(store.BotCleanupStore)
		calls := 0
		verify := func(_ context.Context, q store.BotCleanupRequest) error {
			calls++ // Fixture attestation only, not physical sandbox deletion.
			if q.BotID != deleted.ID || q.Home != deleted.Home {
				t.Fatal("verifier home binding mismatch")
			}
			return nil
		}
		runtime := cleanupRuntimeContext(t, verify)
		p, _ := execution.PrincipalFromContext(runtime)
		p.OrgSlug, p.TeamSlug = "restorepg", "ops"
		runtime, err := execution.WithPrincipal(runtime, p)
		if err != nil {
			t.Fatal(err)
		}
		if err := api.ConfirmCleanup(store.WithBotCleanupVerifier(f.owner, verify), teardown); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("owner authorized cleanup: %v", err)
		}
		stale := teardown
		stale.ExpectedRevision++
		if err := api.ConfirmCleanup(runtime, stale); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("stale receipt: %v", err)
		}
		home := teardown
		home.Kind, home.RunID, home.RunFence = store.BotCleanupHome, uuid.Nil, 0
		if err := api.ConfirmCleanup(runtime, home); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("early home receipt: %v", err)
		}
		if calls != 0 {
			t.Fatalf("invalid receipt reached verifier %d times", calls)
		}
		for range 2 {
			if err := api.ConfirmCleanup(runtime, teardown); err != nil {
				t.Fatal(err)
			}
		}
		rec, err := f.repo.db.GetBot(f.owner, deleted.ID)
		if err != nil || rec.ActiveRunID != nil || rec.LeaseExpiresAt != nil || calls != 1 {
			t.Fatalf("teardown/replay: %+v %v calls=%d", rec, err, calls)
		}
		if err := api.ConfirmCleanup(runtime, home); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("retention bypass: %v", err)
		}
		if n, err := f.repo.PurgeDue(f.owner, time.Now(), 10); err != nil || n != 0 {
			t.Fatalf("early purge: %d %v", n, err)
		}
		// Move only fixture retention timestamps into the past so the exported
		// concrete capability can use its real clock without waiting for retention.
		past := time.Now().UTC().Add(-time.Hour)
		switch d := f.repo.db.(type) {
		case teamBotDB:
			err = d.client.Bot.UpdateOneID(deleted.ID).SetDeletedAt(past.Add(-time.Hour)).SetPurgeAfter(past).Exec(f.owner)
		case personalBotDB:
			err = d.client.Bot.UpdateOneID(deleted.ID).SetDeletedAt(past.Add(-time.Hour)).SetPurgeAfter(past).Exec(f.owner)
		}
		if err != nil {
			t.Fatal(err)
		}
		if n, err := f.repo.PurgeDue(f.owner, time.Now(), 10); err != nil || n != 0 {
			t.Fatalf("purge without home receipt: %d %v", n, err)
		}
		for range 2 {
			if err := api.ConfirmCleanup(runtime, home); err != nil {
				t.Fatal(err)
			}
		}
		if calls != 2 {
			t.Fatalf("home replay invoked verifier again: %d", calls)
		}
		if n, err := f.repo.PurgeDue(f.owner, time.Now(), 10); err != nil || n != 1 {
			t.Fatalf("confirmed purge: %d %v", n, err)
		}
		if _, err := f.bots.Get(f.owner, deleted.ID); !errors.Is(err, store.ErrBotNotFound) {
			t.Fatalf("purged bot remains: %v", err)
		}
		if n, err := f.repo.PurgeDue(f.owner, time.Now(), 10); err != nil || n != 0 {
			t.Fatalf("purge replay: %d %v", n, err)
		}
	})
}
