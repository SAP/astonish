package entstore

import (
	"context"
	"database/sql"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"errors"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func progressFixture(t *testing.T, scope store.BotScope, reopen ...*func()) (*botRepository, context.Context, context.Context, *store.BotRun, store.BotLease, *time.Time) {
	t.Helper()
	owner := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}
	var db botDraftDB
	var activate func(uuid.UUID) error
	var closeDB func() error
	path := filepath.Join(t.TempDir(), "progress.db")
	open := func() {
		sqlDB, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		if _, err = sqlDB.Exec("PRAGMA foreign_keys=ON"); err != nil {
			t.Fatal(err)
		}
		driver := entsql.OpenDB(dialect.SQLite, sqlDB)
		if scope == store.BotScopeTeam {
			c := teament.NewClient(teament.Driver(driver))
			if err = c.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			db, closeDB = teamBotDB{client: c}, c.Close
			activate = func(id uuid.UUID) error { return c.Bot.UpdateOneID(id).SetStatus("active").Exec(ctx) }
		} else {
			c := personalent.NewClient(personalent.Driver(driver))
			if err = c.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			db, closeDB = personalBotDB{client: c}, c.Close
			activate = func(id uuid.UUID) error { return c.Bot.UpdateOneID(id).SetStatus("active").Exec(ctx) }
		}
	}
	open()
	t.Cleanup(func() { _ = closeDB() })
	repo := &botRepository{auth: auth, db: db, now: func() time.Time { return now }}
	if len(reopen) != 0 {
		*reopen[0] = func() {
			if err := closeDB(); err != nil {
				t.Fatal(err)
			}
			open()
			repo.db = db
		}
	}
	v := store.DefaultBotVersion()
	v.Charter = "Durable work"
	v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Progress", OwnerID: owner, Version: v, IdempotencyKey: "progress"})
	if err != nil {
		t.Fatal(err)
	}
	if err = activate(b.ID); err != nil {
		t.Fatal(err)
	}
	run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, IdempotencyKey: "initial", Instructions: "Observe"})
	if err != nil {
		t.Fatal(err)
	}
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: 1, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "worker", ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return repo, ctx, worker, run, lease, &now
}
func progressCheckpoint(t *testing.T, r *botRepository, ctx context.Context, l store.BotLease, now time.Time) store.BotProgressCheckpointRequest {
	t.Helper()
	s, err := r.db.ReadRun(ctx, l.BotID, l.RunID)
	if err != nil {
		t.Fatal(err)
	}
	binding := progressBinding(s)
	return store.BotProgressCheckpointRequest{Mutation: store.BotProgressMutation{Lease: l, Binding: binding, ExpectedRevision: progressRevision(s)}, State: store.BotProgressState{SchemaVersion: 1, Phase: store.BotProgressWorking, Checkpoint: &store.BotProgressCheckpoint{Kind: store.BotProgressCheckpointV1, SchemaVersion: 1, Binding: binding, Revision: progressRevision(s) + 1, TurnID: uuid.New(), ContextRef: "private-context:1", ContextDigest: strings.Repeat("a", 64), NextEligibleAt: now.Add(time.Minute), Usage: s.Run.Usage, DispatchLedgerCursor: s.ProgressCursor}}}
}
func TestBotProgressDurableDispatch(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			var reopen func()
			r, owner, ctx, run, l, now := progressFixture(t, scope, &reopen)
			q := progressCheckpoint(t, r, ctx, l, *now)
			state, err := r.CheckpointProgress(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.CheckpointProgress(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale checkpoint: %v", err)
			}
			m := q.Mutation
			m.ExpectedRevision = 1
			begin := store.BotProgressBeginDispatchRequest{Mutation: m, DispatchID: uuid.New(), Kind: store.BotProgressDispatchTool, OperationDigest: strings.Repeat("b", 64), ReservedUsage: store.BotUsage{ToolCalls: 1, InputTokens: 50}}
			receipt, err := r.BeginProgressDispatch(ctx, begin)
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err := r.BeginProgressDispatch(ctx, begin)
			if err != nil || *receipt != *duplicate {
				t.Fatalf("replay: %v", err)
			}
			changed := begin
			changed.OperationDigest = strings.Repeat("c", 64)
			if _, err = r.BeginProgressDispatch(ctx, changed); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("dispatch content changed", err)
			}
			q = progressCheckpoint(t, r, ctx, l, *now)
			q.State.Checkpoint.TurnID = state.Checkpoint.TurnID
			if _, err = r.CheckpointProgress(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("checkpoint passed inflight dispatch", err)
			}
			// Close/reopen the scoped SQLite connection before expired-lease recovery.
			// An older valid checkpoint MUST NOT make this write replay-safe.
			reopen()
			reopened := &botRepository{auth: r.auth, db: r.db, now: r.now}
			*now = now.Add(2 * time.Minute)
			if err = reopened.RecoverRun(owner, l); err != nil {
				t.Fatal(err)
			}
			saved, err := r.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Run.Status != store.RunInterrupted || saved.Progress.Phase != store.BotProgressReconciling || saved.Run.Usage.ToolCalls != 1 || saved.Run.Usage.InputTokens != 50 {
				t.Fatalf("lost uncertainty/charges: %+v", saved)
			}
		})
	}
}
func TestBotProgressSettledSuspendContinue(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			var reopen func()
			r, owner, ctx, run, l, now := progressFixture(t, scope, &reopen)
			q := progressCheckpoint(t, r, ctx, l, *now)
			state, err := r.CheckpointProgress(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			m := q.Mutation
			m.ExpectedRevision = 1
			begin := store.BotProgressBeginDispatchRequest{Mutation: m, DispatchID: uuid.New(), Kind: store.BotProgressDispatchModel, OperationDigest: strings.Repeat("a", 64), ReservedUsage: store.BotUsage{InputTokens: 100}}
			if _, err = r.BeginProgressDispatch(ctx, begin); err != nil {
				t.Fatal(err)
			}
			settle := store.BotProgressSettleDispatchRequest{Mutation: m, DispatchID: begin.DispatchID, Outcome: store.BotProgressDispatchSettled, Usage: store.BotUsage{InputTokens: 120}, EvidenceRef: "provider-receipt:exact-call"}
			a, err := r.SettleProgressDispatch(ctx, settle)
			if err != nil {
				t.Fatal(err)
			}
			b, err := r.SettleProgressDispatch(ctx, settle)
			if err != nil || *a != *b {
				t.Fatal("settlement replay", err)
			}
			q = progressCheckpoint(t, r, ctx, l, *now)
			q.State.Checkpoint.TurnID = state.Checkpoint.TurnID
			q.State.Phase = store.BotProgressSuspended
			state, err = r.CheckpointProgress(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if state.Checkpoint.Usage.InputTokens != 120 {
				t.Fatal("lost reservation settlement")
			}
			reopen()
			input, err := r.LoadRunInput(owner, run.BotID, run.ID)
			if err != nil || input.Progress == nil {
				t.Fatal("restore", err)
			}
			*now = now.Add(6 * time.Hour)
			l, err = r.ClaimRun(ctx, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "reopened", ExpiresAt: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			m.Lease = l
			m.ExpectedRevision = 2
			if _, err = r.ContinueProgress(ctx, store.BotProgressContinueRequest{Mutation: m, CheckpointRevision: 2, Reason: store.BotProgressReasonRecovery}); err != nil {
				t.Fatal(err)
			}
			saved, err := r.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Run.Usage.InputTokens != 120 || saved.Run.Usage.RuntimeNanos >= int64(time.Hour) {
				t.Fatalf("reset tokens or charged queue time: %+v", saved.Run.Usage)
			}
		})
	}
}
