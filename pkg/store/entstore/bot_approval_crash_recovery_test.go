package entstore

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// Consumption is conservative durable intent, not proof that the provider
// committed. An approved but unconsumed grant must not be labeled dispatched.
func TestBotApprovalCrashRecoveryEvidence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, consumed := range []bool{false, true} {
			name := "approved-not-dispatched"
			if consumed {
				name = "consumed-outcome-unknown"
			}
			t.Run(string(scope)+"/"+name, func(t *testing.T) {
				repo, owner, worker, run, lease, now := botRunFixture(t, scope)
				action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "alice", Arguments: json.RawMessage(`{"body":"exact"}`)}
				digest, err := action.Digest()
				if err != nil {
					t.Fatal(err)
				}
				approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				checkpoint, err := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: approval.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
				if err != nil {
					t.Fatal(err)
				}
				work, err := repo.LoadWork(worker, lease)
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Await exact approval", Work: store.BuildBotWorkProjection(work), NeedsApproval: []store.RunID{approval.ID}}}); err != nil {
					t.Fatal(err)
				}
				b, err := repo.Get(owner, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repo.DecideApproval(owner, store.BotApprovalDecision{BotID: run.BotID, ApprovalID: approval.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
					t.Fatal(err)
				}
				if err := repo.RequeueApprovedRun(owner, run.BotID, run.ID, approval.ID); err != nil {
					t.Fatal(err)
				}
				fresh, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "doomed", ExpiresAt: now.Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				if consumed {
					if err := repo.ConsumeApproval(worker, store.BotApprovalConsumption{Lease: fresh, ApprovalID: approval.ID, Version: run.Version, ActionDigest: digest}); err != nil {
						t.Fatal(err)
					}
				}
				*now = now.Add(2 * time.Minute)
				if err := repo.RecoverRun(owner, fresh); err != nil {
					t.Fatal(err)
				}
				saved, err := repo.GetRun(owner, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if saved.Status != store.RunInterrupted || saved.FinishedAt != nil || len(saved.Report.Unverified) == 0 {
					t.Fatalf("recovery: %+v", saved)
				}
				if (saved.Report.EffectState == store.BotReportEffectsUncertain) != consumed {
					t.Fatalf("consumed=%t effect=%q", consumed, saved.Report.EffectState)
				}
				if err := repo.RecoverRun(owner, fresh); err != nil {
					t.Fatal(err)
				}
				again, err := repo.GetRun(owner, run.BotID, run.ID)
				if err != nil || !reflect.DeepEqual(saved, again) {
					t.Fatalf("recovery not idempotent: %v", err)
				}
				if err := repo.RequeueApprovedRun(owner, run.BotID, run.ID, approval.ID); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("replayed: %v", err)
				}
				if err := repo.ConsumeApproval(worker, store.BotApprovalConsumption{Lease: fresh, ApprovalID: approval.ID, Version: run.Version, ActionDigest: digest}); err == nil {
					t.Fatal("dead worker consumed")
				}
			})
		}
	}
}
