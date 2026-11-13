package entstore

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func waitingApprovalFixture(t *testing.T, scope store.BotScope) (*botRepository, context.Context, context.Context, *store.BotRun, *store.BotApproval, *time.Time) {
	t.Helper()
	repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
	action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"body":"exact"}`)}
	digest, _ := action.Digest()
	a, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: a.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
	work, err := repo.LoadWork(worker, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: raw, Report: store.BotReport{Summary: "Waiting", Work: store.BuildBotWorkProjection(work), NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
		t.Fatal(err)
	}
	return repo, ctx, worker, run, a, now
}

func TestBotApprovalRequeueCurrentAuthority(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			for _, scenario := range []string{"paused", "expired", "revoked-membership", "denied", "wrong-approval", "changed-version"} {
				t.Run(scenario, func(t *testing.T) {
					repo, ctx, _, run, a, now := waitingApprovalFixture(t, scope)
					b, err := repo.Get(ctx, run.BotID)
					if err != nil {
						t.Fatal(err)
					}
					decision := store.ApprovalApproved
					if scenario == "denied" {
						decision = store.ApprovalDenied
					}
					if _, err := repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: a.ActionDigest, Status: decision}); err != nil {
						t.Fatal(err)
					}
					approvalID := a.ID
					switch scenario {
					case "expired":
						*now = now.Add(2 * time.Hour)
					case "wrong-approval":
						approvalID = uuid.New()
					case "revoked-membership":
						repo.auth.check = func(context.Context, string) (string, error) { return "", store.ErrBotForbidden }
					case "paused", "changed-version":
						b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "review"})
						if err != nil {
							t.Fatal(err)
						}
						if scenario == "changed-version" {
							// Simulate a new active reviewed version independently of this old run.
							switch db := repo.db.(type) {
							case personalBotDB:
								err = db.client.Bot.UpdateOneID(b.ID).SetStatus("active").AddVersion(1).Exec(ctx)
							case teamBotDB:
								err = db.client.Bot.UpdateOneID(b.ID).SetStatus("active").AddVersion(1).Exec(ctx)
							}
							if err != nil {
								t.Fatal(err)
							}
						}
					}
					if err := repo.RequeueApprovedRun(ctx, run.BotID, run.ID, approvalID); err == nil {
						t.Fatal("unsafe requeue accepted")
					}
				})
			}
		})
	}
}

func TestBotApprovalConcurrentConsumption(t *testing.T) {
	repo, ctx, worker, run, a, now := waitingApprovalFixture(t, store.BotScopePersonal)
	b, _ := repo.Get(ctx, run.BotID)
	if _, err := repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: a.ActionDigest, Status: store.ApprovalApproved}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RequeueApprovedRun(ctx, run.BotID, run.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "fresh", ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := repo.ConsumeApproval(worker, store.BotApprovalConsumption{Lease: lease, ApprovalID: a.ID, Version: run.Version, ActionDigest: a.ActionDigest}); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("consumed %d times", successes.Load())
	}
}
