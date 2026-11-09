package entstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotApproveContinueAtomic(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"queued", "paused", "expired", "wrong-digest", "revoked", "rollback", "requeue-failure", "stopped", "deleted", "changed-version", "stale-checkpoint", "wrong-actor", "changed-action", "denied"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				repo, ctx, worker, run, a, now := waitingApprovalFixture(t, scope)
				b, err := repo.Get(ctx, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				q := store.BotApproveContinueRequest{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: a.ActionDigest}
				original := repo.db
				before, err := original.GetBot(ctx, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "paused":
					_, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "review"})
				case "expired":
					*now = now.Add(2 * time.Hour)
				case "wrong-digest":
					q.ActionDigest = "wrong"
				case "revoked":
					repo.auth.check = func(context.Context, string) (string, error) { return "", store.ErrBotForbidden }
				case "denied":
					_, err = repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: a.ActionDigest, Status: store.ApprovalDenied})
				case "wrong-actor":
					q.ActorID = uuid.New()
				case "requeue-failure":
					// Fail the queue mutation after the approval decision is written.
					failQueue := func(next ent.Mutator) ent.Mutator {
						return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
							if status, ok := m.Field("status"); ok && fmt.Sprint(status) == string(store.RunQueued) {
								return nil, errors.New("injected queue failure")
							}
							return next.Mutate(ctx, m)
						})
					}
					switch db := repo.db.(type) {
					case personalBotDB:
						db.client.BotRun.Use(failQueue)
					case teamBotDB:
						db.client.BotRun.Use(failQueue)
					}
				case "stopped", "deleted", "changed-version", "stale-checkpoint", "changed-action":
					err = repo.db.Transaction(ctx, func(tx botDraftDB) error {
						if scenario == "changed-action" {
							a.Action.Arguments = []byte(`{"body":"replacement"}`)
							return tx.UpdateApproval(ctx, *a, store.ApprovalPending, *now)
						}
						switch db := tx.(type) {
						case personalBotDB:
							switch scenario {
							case "stopped":
								return db.client.Bot.UpdateOneID(b.ID).SetStatus("stopped").Exec(ctx)
							case "deleted":
								return db.client.Bot.UpdateOneID(b.ID).SetStatus("deleted").Exec(ctx)
							case "changed-version":
								return db.client.Bot.UpdateOneID(b.ID).AddVersion(1).Exec(ctx)
							case "stale-checkpoint":
								return db.client.BotRun.UpdateOneID(run.ID).AddFence(1).Exec(ctx)
							}
						case teamBotDB:
							switch scenario {
							case "stopped":
								return db.client.Bot.UpdateOneID(b.ID).SetStatus("stopped").Exec(ctx)
							case "deleted":
								return db.client.Bot.UpdateOneID(b.ID).SetStatus("deleted").Exec(ctx)
							case "changed-version":
								return db.client.Bot.UpdateOneID(b.ID).AddVersion(1).Exec(ctx)
							case "stale-checkpoint":
								return db.client.BotRun.UpdateOneID(run.ID).AddFence(1).Exec(ctx)
							}
						}
						return nil
					})
				case "rollback":
					repo.db = failApproveContinueTransaction{original}
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := repo.DecideAndRequeueApprovedRun(ctx, q)
				repo.db = original
				if scenario != "queued" && scenario != "paused" {
					if err == nil {
						t.Fatal("unsafe continuation accepted")
					}
					if scenario == "changed-action" {
						// Corrupt action data cannot be decoded. Restore only the fixture
						// payload to inspect that the rejected request made no decision.
						a.Action.Arguments = []byte(`{"body":"exact"}`)
						if e := original.UpdateApproval(ctx, *a, store.ApprovalPending, *now); e != nil {
							t.Fatal(e)
						}
					}
					saved, e := original.ReadApproval(ctx, b.ID, a.ID)
					wantStatus := store.ApprovalPending
					if scenario == "denied" {
						wantStatus = store.ApprovalDenied
					}
					if e != nil || saved.Status != wantStatus {
						t.Fatalf("decision escaped failed transaction: %+v %v", saved, e)
					}
					if scenario == "rollback" || scenario == "requeue-failure" {
						after, e := original.GetBot(ctx, b.ID)
						if e != nil || after.LastSequence != before.LastSequence {
							t.Fatalf("activity sequence escaped rollback: %+v %v", after, e)
						}
						savedRun, e := original.ReadRun(ctx, b.ID, run.ID)
						if e != nil || savedRun.Run.Status != store.RunWaitingApproval {
							t.Fatalf("queue escaped rollback: %+v %v", savedRun, e)
						}
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				want := store.BotContinuationQueued
				if scenario == "paused" {
					want = store.BotContinuationBlockedByPause
				}
				if got.Continuation != want || got.Approval.Status != store.ApprovalApproved || got.RunID != run.ID {
					t.Fatalf("result %+v", got)
				}
				again, err := repo.DecideAndRequeueApprovedRun(ctx, q)
				if err != nil || again.Continuation != want {
					t.Fatalf("repeat %+v %v", again, err)
				}
				current, _ := repo.Get(ctx, b.ID)
				if scenario == "paused" {
					saved, _ := repo.GetRun(ctx, b.ID, run.ID)
					if current.Status != store.BotPaused || saved.Status != store.RunWaitingApproval {
						t.Fatal("approval secretly resumed Bot")
					}
					return
				}
				lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "continue", ExpiresAt: now.Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				loaded, err := repo.LoadResumeApproval(worker, lease)
				if err != nil || loaded.ActionDigest != a.ActionDigest {
					t.Fatalf("load %v", err)
				}
				consumption := store.BotApprovalConsumption{Lease: lease, ApprovalID: a.ID, Version: run.Version, ActionDigest: a.ActionDigest}
				if err := repo.ConsumeApproval(worker, consumption); err != nil {
					t.Fatal(err)
				}
				if err := repo.ConsumeApproval(worker, consumption); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("reconsume %v", err)
				}
				if _, err := repo.DecideAndRequeueApprovedRun(ctx, q); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("consumed replay %v", err)
				}
			})
		}
	}
}

type failApproveContinueTransaction struct{ botDraftDB }

func (d failApproveContinueTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errors.New("injected failure before commit")
	})
}
