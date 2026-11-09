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

func TestOwnerMessagePriority(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"autonomous", "interactive", "denied", "version", "cross_scope", "rollback"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				repo, scoped, ctx, initial, _ := runtimeFixture(t, scope)
				_ = scoped
				p0, _ := execution.PrincipalFromContext(ctx)
				old := initial
				worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: old.BotID, RunID: old.ID, Version: old.Version, ActorID: p0.Subject, OrgSlug: p0.OrgSlug, TeamSlug: p0.TeamSlug})
				lease, claimErr := repo.ClaimRun(worker, store.BotClaimRequest{BotID: old.BotID, RunID: old.ID, WorkerID: "old", ExpiresAt: time.Now().Add(time.Minute)})
				if claimErr != nil {
					t.Fatal(claimErr)
				}
				if scenario == "interactive" {
					// Finish the fixture's autonomous writer, then claim a real owner turn.
					if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "done"}}); err != nil {
						t.Fatal(err)
					}
					msg, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: old.BotID, Version: old.Version, IdempotencyKey: "first-owner", Instructions: "hello"})
					if err != nil {
						t.Fatal(err)
					}
					old = &msg.Run
					p, _ := execution.PrincipalFromContext(ctx)
					worker = execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: old.BotID, RunID: old.ID, Version: old.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
					lease, err = repo.ClaimRun(worker, store.BotClaimRequest{BotID: old.BotID, RunID: old.ID, WorkerID: "owner", ExpiresAt: time.Now().Add(time.Minute)})
					if err != nil {
						t.Fatal(err)
					}
				}
				before, _ := repo.db.GetBot(ctx, old.BotID)
				q := store.BotMessageRequest{BotID: old.BotID, Version: old.Version, IdempotencyKey: "priority", Instructions: "Pause and focus on my request"}
				admissionCtx := ctx
				switch scenario {
				case "denied":
					admissionCtx = context.Background()
				case "version":
					q.Version++
				case "cross_scope":
					p, _ := execution.PrincipalFromContext(ctx)
					p.OrgSlug = "other"
					admissionCtx, _ = execution.WithPrincipal(context.Background(), p)
				case "rollback":
					original := repo.db
					repo.db = priorityFailDB{botDraftDB: original}
					defer func() { repo.db = original }()
				}
				receipt, err := repo.AdmitMessage(admissionCtx, q)
				after, _ := repo.db.GetBot(ctx, old.BotID)
				saved, _ := repo.GetRun(ctx, old.BotID, old.ID)
				if scenario != "autonomous" && scenario != "interactive" {
					if err == nil {
						t.Fatal("invalid admission accepted")
					}
					if after.Fence != before.Fence || after.LastSequence != before.LastSequence || saved.Status != store.RunRunning {
						t.Fatal("failed admission quiesced writer")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "interactive" {
					if after.Fence != before.Fence || saved.Status != store.RunRunning {
						t.Fatal("owner interrupted owner")
					}
					return
				}
				if saved.Status != store.RunInterrupted || after.Fence != lease.Fence+1 || after.ActiveRunID == nil || *after.ActiveRunID != old.ID || after.Bot.Status != store.BotActive {
					t.Fatalf("lost barrier: %+v %+v", after, saved)
				}
				if _, err = repo.RenewLease(worker, lease, time.Now().Add(time.Minute)); err == nil {
					t.Fatal("stale writer renewed")
				}
				again, err := repo.AdmitMessage(ctx, q)
				if err != nil || again.Run.ID != receipt.Run.ID {
					t.Fatal("replay failed", err)
				}
				stable, _ := repo.db.GetBot(ctx, old.BotID)
				if stable.Fence != after.Fence || stable.LastSequence != after.LastSequence {
					t.Fatal("replay quiesced again")
				}
				p, _ := execution.PrincipalFromContext(ctx)
				claimCtx := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: old.BotID, RunID: receipt.Run.ID, Version: old.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
				claim := store.BotClaimRequest{BotID: old.BotID, RunID: receipt.Run.ID, WorkerID: "next", ExpiresAt: time.Now().Add(time.Minute)}
				if _, err = repo.ClaimRun(claimCtx, claim); !errors.Is(err, store.ErrBotConflict) {
					t.Fatal("second writer", err)
				}
				cleanup := store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: old.BotID, RunID: old.ID, RunFence: lease.Fence, Fence: after.Fence, ExpectedRevision: after.Bot.Revision, Home: after.Bot.Home}
				verified := cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return nil })
				if err = repo.ConfirmCleanup(verified, cleanup); err != nil {
					t.Fatal(err)
				}
				bg, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: old.BotID, Version: old.Version, Trigger: store.TriggerManual, Instructions: "background", IdempotencyKey: "background"})
				if err != nil {
					t.Fatal(err)
				}
				bgctx := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: old.BotID, RunID: bg.ID, Version: bg.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
				if _, err = repo.ClaimRun(bgctx, store.BotClaimRequest{BotID: old.BotID, RunID: bg.ID, WorkerID: "scheduler", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, store.ErrBotConflict) {
					t.Fatal("background bypassed queued owner", err)
				}
				if _, err = repo.ClaimRun(claimCtx, claim); err != nil {
					t.Fatal("verified owner blocked", err)
				}
			})
		}
	}
}

type priorityFailDB struct{ botDraftDB }

func (d priorityFailDB) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(priorityFailTx{botDraftDB: tx}) })
}

type priorityFailTx struct{ botDraftDB }

func (d priorityFailTx) SaveRunCancellation(ctx context.Context, r botStoredRecord, s botStoredRun, p store.RunStatus, n time.Time) error {
	return d.botDraftDB.(botRunCancellationDB).SaveRunCancellation(ctx, r, s, p, n)
}

// Failure after quiescing, before message insertion, must roll the fence back.
func (d priorityFailTx) AdvanceSequence(context.Context, uuid.UUID, int64) error {
	return errors.New("injected admission failure")
}
