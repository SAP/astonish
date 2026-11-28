package entstore

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotApprovalResumeFenced(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"body":"exact","token":"{{CREDENTIAL:mail:token}}"}`), Recipient: "alice"}
			digest, _ := action.Digest()
			a, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, _ := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: a.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
			priorCost := int64(7)
			priorUsage := store.BotUsage{InputTokens: 3, OutputTokens: 2, ToolCalls: 1, CostMicros: &priorCost, RuntimeNanos: int64(time.Second)}
			work, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Usage: priorUsage, Checkpoint: checkpoint, Report: store.BotReport{Work: store.BuildBotWorkProjection(work), Summary: "Waiting", NeedsApproval: []store.RunID{a.ID}}}); err != nil {
				t.Fatal(err)
			}
			if err := repo.RequeueApprovedRun(ctx, run.BotID, run.ID, a.ID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("pending requeued: %v", err)
			}
			b, _ := repo.Get(ctx, run.BotID)
			if _, err := repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: run.BotID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
				t.Fatal(err)
			}
			saved, _ := repo.GetRun(ctx, run.BotID, run.ID)
			if saved.Status != store.RunWaitingApproval {
				t.Fatal("decision autoqueued")
			}
			// Reconstruct the repository: no goroutine, pending channel or process-local
			// approval authority survives this boundary.
			repo = &botRepository{db: repo.db, auth: repo.auth, now: repo.now}
			for i := 0; i < 2; i++ {
				if err := repo.RequeueApprovedRun(ctx, run.BotID, run.ID, a.ID); err != nil {
					t.Fatal(err)
				}
			}
			input, err := repo.LoadRunInput(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			meter, err := bot.RestoreRunBudget(input.Version.Budget, time.Now(), input.Run.Usage)
			if err != nil {
				t.Fatal(err)
			}
			if err := meter.ReserveTool(worker, "send"); err != nil {
				t.Fatal(err)
			}
			totalUsage := meter.Usage()
			fresh, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "fresh-worker", ExpiresAt: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Fence <= lease.Fence {
				t.Fatal("fence reused")
			}
			if _, err := repo.LoadResumeApproval(worker, lease); err == nil {
				t.Fatal("stale worker loaded approval")
			}
			loaded, err := repo.LoadResumeApproval(worker, fresh)
			if err != nil || loaded.ActionDigest != digest {
				t.Fatalf("load %v %v", loaded, err)
			}
			q := store.BotApprovalConsumption{Lease: fresh, ApprovalID: a.ID, Version: run.Version, ActionDigest: digest}
			if err := repo.ConsumeApproval(worker, q); err != nil {
				t.Fatal(err)
			}
			if err := repo.ConsumeApproval(worker, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("reconsumed %v", err)
			}
			if _, err := repo.LoadResumeApproval(worker, fresh); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("consumed loaded %v", err)
			}
			if err := repo.RequeueApprovedRun(ctx, run.BotID, run.ID, a.ID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("uncertain replay %v", err)
			}
			work, err = repo.LoadWork(worker, fresh)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: fresh, Status: store.RunPartial, Usage: totalUsage, Report: store.BotReport{Work: store.BuildBotWorkProjection(work), Summary: "Action completed; independent outcome verification unavailable.", Unverified: []string{"external effect"}}}); err != nil {
				t.Fatal(err)
			}
			saved, err = repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertRunStateSequence(t, repo, ctx, *run, store.RunQueued, store.RunRunning, store.RunWaitingApproval, store.RunQueued, store.RunRunning, store.RunPartial)
			if saved.Usage.ToolCalls != 2 || saved.Usage.InputTokens != 3 || saved.Usage.OutputTokens != 2 || saved.Usage.CostMicros == nil || *saved.Usage.CostMicros != priorCost || saved.Usage.RuntimeNanos < priorUsage.RuntimeNanos {
				t.Fatalf("resume replaced or double-counted cumulative usage: %+v", saved.Usage)
			}
		})
	}
}
