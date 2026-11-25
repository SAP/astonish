package entstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotApprovalResumePreDispatchDenial(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, run, lease, now := botRunFixture(t, scope)
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"body":"exact"}`), Recipient: "alice"}
			digest, _ := action.Digest()
			approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, _ := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: approval.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
			waiting := func(l store.BotLease) store.BotRunCommit {
				t.Helper()
				work, err := repo.LoadWork(worker, l)
				if err != nil {
					t.Fatal(err)
				}
				return store.BotRunCommit{Lease: l, Status: store.RunWaitingApproval, Checkpoint: bytes.Clone(checkpoint), Report: store.BotReport{Work: store.BuildBotWorkProjection(work), Summary: "Denied before dispatch", NeedsApproval: []uuid.UUID{approval.ID}}}
			}
			if err := repo.CommitRun(worker, waiting(lease)); err != nil {
				t.Fatal(err)
			}
			b, _ := repo.Get(owner, run.BotID)
			if _, err := repo.DecideApproval(owner, store.BotApprovalDecision{BotID: run.BotID, ApprovalID: approval.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
				t.Fatal(err)
			}
			// Repeat safe denials with fresh leases but byte-identical original authority.
			for attempt := 0; attempt < 2; attempt++ {
				if attempt == 0 {
					if err := repo.RequeueApprovedRun(owner, run.BotID, run.ID, approval.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := repo.DecideAndRequeueApprovedRun(owner, store.BotApproveContinueRequest{BotID: run.BotID, ApprovalID: approval.ID, ActorID: b.OwnerID, ActionDigest: digest}); err != nil {
						t.Fatalf("approved retry through approve-and-continue: %v", err)
					}
				}
				fresh, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "retry", ExpiresAt: now.Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repo.LoadResumeApproval(worker, lease); err == nil {
					t.Fatal("stale lease loaded approval")
				}
				q := waiting(fresh)
				stale := q
				stale.Lease = lease
				if err := repo.CommitRun(worker, stale); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("stale lease waiting commit accepted: %v", err)
				}
				claimedEffect := q
				claimedEffect.Report.EffectState = store.BotReportEffectsUncertain
				if err := repo.CommitRun(worker, claimedEffect); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("uncertain effect retained approved grant: %v", err)
				}
				altered := q
				c, _ := bot.DecodeApprovalCheckpoint(checkpoint, *run)
				c.Fence = fresh.Fence
				altered.Checkpoint, _ = json.Marshal(c)
				if err := repo.CommitRun(worker, altered); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("changed checkpoint accepted: %v", err)
				}
				if err := repo.CommitRun(worker, q); err != nil {
					t.Fatalf("unused approved waiting commit: %v", err)
				}
				saved, err := repo.GetRun(owner, run.BotID, run.ID)
				if err != nil || saved.Status != store.RunWaitingApproval || !bytes.Equal(saved.Checkpoint, checkpoint) {
					t.Fatalf("lost waiting checkpoint: %+v %v", saved, err)
				}
				all, err := repo.ListApprovals(owner, run.BotID, store.BotPage{})
				if err != nil || len(all) != 1 || all[0].Status != store.ApprovalApproved {
					t.Fatalf("approval changed: %+v %v", all, err)
				}
				lease = fresh
			}
			if err := repo.RequeueApprovedRun(owner, run.BotID, run.ID, approval.ID); err != nil {
				t.Fatal(err)
			}
			fresh, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "execute", ExpiresAt: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotApprovalConsumption{Lease: fresh, ApprovalID: approval.ID, Version: run.Version, ActionDigest: digest}
			if err := repo.ConsumeApproval(worker, q); err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, waiting(fresh)); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("consumed grant retained as waiting: %v", err)
			}
			if err := repo.ConsumeApproval(worker, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("consumed twice: %v", err)
			}
		})
	}
}
