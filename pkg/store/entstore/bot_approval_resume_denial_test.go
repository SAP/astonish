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
	"google.golang.org/adk/session"
)

func TestEmployeeWorkerDurableApprovalBinding(t *testing.T) {
	a := store.BotApproval{ID: uuid.New(), ActionDigest: "exact"}
	event := func(author, worker, dispatch, phase string, approval uuid.UUID, digest string) *session.Event {
		return &session.Event{Author: author, Actions: session.EventActions{StateDelta: map[string]any{"bot_private_worker_v1": map[string]any{"worker_id": worker, "dispatch_id": dispatch, "phase": phase, "approval_id": approval, "action_digest": digest}}}}
	}
	start := event("bot_runtime", "worker", "dispatch", "started", uuid.Nil, "")
	approval := event("bot_runtime", "worker", "dispatch", "approval", a.ID, a.ActionDigest)
	for _, test := range []struct {
		name    string
		events  []*session.Event
		allowed bool
	}{
		{"exact", []*session.Event{start, approval}, true},
		{"no-history", nil, false},
		{"owner-spoof", []*session.Event{start, event("user", "worker", "dispatch", "approval", a.ID, a.ActionDigest)}, false},
		{"model-spoof", []*session.Event{start, event("model", "worker", "dispatch", "approval", a.ID, a.ActionDigest)}, false},
		{"foreign-dispatch", []*session.Event{start, event("bot_runtime", "worker", "other", "approval", a.ID, a.ActionDigest)}, false},
		{"foreign-approval", []*session.Event{start, event("bot_runtime", "worker", "dispatch", "approval", uuid.New(), a.ActionDigest)}, false},
		{"foreign-action", []*session.Event{start, event("bot_runtime", "worker", "dispatch", "approval", a.ID, "other")}, false},
		{"uncertain-resuming", []*session.Event{start, approval, event("bot_runtime", "worker", "dispatch", "resuming", a.ID, a.ActionDigest)}, false},
		{"settled", []*session.Event{start, approval, event("bot_runtime", "worker", "dispatch", "settled", uuid.Nil, "")}, false},
		{"duplicate-binding", []*session.Event{start, approval, event("bot_runtime", "other", "dispatch2", "started", uuid.Nil, ""), event("bot_runtime", "other", "dispatch2", "approval", a.ID, a.ActionDigest)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := matchPrivateWorkerApproval(test.events, a)
			if (err == nil) != test.allowed {
				t.Fatalf("allowed=%v err=%v", test.allowed, err)
			}
		})
	}
}

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
