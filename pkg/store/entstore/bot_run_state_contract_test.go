package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func assertRunStateSequence(t *testing.T, repo *botRepository, ctx context.Context, run store.BotRun, want ...store.RunStatus) {
	t.Helper()
	rows, err := messageReviewReader(repo).ReadBotTranscript(ctx, run.SessionID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	separateRunStates(t, rows, len(want))
	index := 0
	var last int64
	for _, row := range rows {
		raw, ok := row.Event.Actions.StateDelta[store.BotRunStateKey]
		if !ok {
			continue
		}
		data, _ := json.Marshal(raw)
		var state store.BotRunState
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		if state.RunID != run.ID || state.BotID != run.BotID || state.SessionID != run.SessionID || state.Status != want[index] || state.StateSequence <= last {
			t.Fatalf("state[%d]=%+v want=%s after=%d", index, state, want[index], last)
		}
		last = state.StateSequence
		index++
	}
}

func assertCommittedRunStateNotifications(t *testing.T, repo *botRepository, ctx context.Context) (context.Context, *[]store.BotRunState) {
	t.Helper()
	var states []store.BotRunState
	return store.WithBotRunStateNotifier(ctx, func(callbackCtx context.Context, state store.BotRunState) {
		t.Helper()
		saved, err := repo.GetRun(callbackCtx, state.BotID, state.RunID)
		if err != nil || saved.Status != state.Status {
			t.Fatalf("notification before durable run: %+v %v state=%+v", saved, err, state)
		}
		rows, err := messageReviewReader(repo).ReadBotTranscript(callbackCtx, state.SessionID, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			raw, ok := row.Event.Actions.StateDelta[store.BotRunStateKey]
			if !ok {
				continue
			}
			data, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			var persisted store.BotRunState
			if err := json.Unmarshal(data, &persisted); err != nil {
				t.Fatal(err)
			}
			if persisted.RunID == state.RunID && persisted.StateSequence == state.StateSequence && persisted.Status == state.Status {
				found = true
			}
		}
		if !found {
			t.Fatalf("notification before durable transcript: %+v", state)
		}
		states = append(states, state)
	}), &states
}

func TestBotRunStateAllStatusesAndFailedCAS(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, status := range []store.RunStatus{store.RunWaitingApproval, store.RunInterrupted, store.RunSucceeded, store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled} {
			t.Run(string(scope)+"/"+string(status), func(t *testing.T) {
				repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
				assertRunStateSequence(t, repo, ctx, *run, store.RunQueued, store.RunRunning)
				q := store.BotRunCommit{Lease: lease, Status: status, Report: store.BotReport{Summary: "observed outcome"}}
				if status == store.RunSucceeded {
					q.Report = acceptedWorkReport(t, repo, worker, lease, q.Report.Summary)
				}
				if status == store.RunWaitingApproval {
					action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{}`)}
					digest, _ := action.Digest()
					approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
					q.Checkpoint = json.RawMessage(`{}`)
					q.Report.NeedsApproval = []store.RunID{approval.ID}
				}
				calls := 0
				worker = store.WithBotRunStateNotifier(worker, func(callbackCtx context.Context, state store.BotRunState) {
					calls++
					saved, err := repo.GetRun(ctx, run.BotID, run.ID)
					if err != nil || saved.Status != status || state.Status != status {
						t.Fatalf("callback before commit: %+v %v %+v", saved, err, state)
					}
					assertRunStateSequence(t, repo, ctx, *run, store.RunQueued, store.RunRunning, status)
				})
				stale := q
				stale.Lease.Fence++
				if err := repo.CommitRun(worker, stale); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("stale CAS: %v", err)
				}
				if calls != 0 {
					t.Fatal("failed CAS notified")
				}
				if err := repo.CommitRun(worker, q); err != nil {
					t.Fatal(err)
				}
				if err := repo.CommitRun(worker, q); err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatalf("commit/replay callbacks=%d", calls)
				}
				assertRunStateSequence(t, repo, ctx, *run, store.RunQueued, store.RunRunning, status)
			})
		}
	}
}

func TestBotRunStateNotifyRollbackAndRecovery(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, lease, now := botRunFixture(t, scope)
			calls := 0
			ctx = store.WithBotRunStateNotifier(ctx, func(context.Context, store.BotRunState) { calls++ })
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("abort after metadata")
			err = repo.db.Transaction(ctx, func(tx botDraftDB) error {
				if err := appendBotRunStateTx(ctx, tx, *b, *run, 999, *now); err != nil {
					return err
				}
				if calls != 0 {
					t.Fatal("callback before outer commit")
				}
				return sentinel
			})
			if !errors.Is(err, sentinel) || calls != 0 {
				t.Fatalf("rollback=%v calls=%d", err, calls)
			}
			assertRunStateSequence(t, repo, ctx, *run, store.RunQueued, store.RunRunning)
			if err := repo.RecoverRun(ctx, lease); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal(err)
			}
			*now = now.Add(2 * time.Minute)
			if err := repo.RecoverRun(ctx, lease); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("recovery calls=%d", calls)
			}
			assertRunStateSequence(t, repo, ctx, *run, store.RunQueued, store.RunRunning, store.RunInterrupted)
		})
	}
}

func TestBotRunStateLifecycleQuiesce(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, notifications := assertCommittedRunStateNotifications(t, repo, ctx)
			if _, err := repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "pause writer"}); err != nil {
				t.Fatal(err)
			}
			if len(*notifications) != 1 || (*notifications)[0].Status != store.RunInterrupted {
				t.Fatalf("quiesce notifications: %+v", *notifications)
			}
			assertRunStateSequence(t, repo, ctx, *run, store.RunQueued, store.RunRunning, store.RunInterrupted)
		})
	}
}

func TestBotRunStateConfigurationQuiesce(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			v, err := repo.db.ReadVersion(ctx, run.BotID, run.Version)
			if err != nil {
				t.Fatal(err)
			}
			p, err := repo.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "config-quiesce", ExpiresAt: repo.clock().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlConfigurationOperation, Reason: "pause and review", Configuration: &store.BotControlConfiguration{Pause: true, Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, repo, worker, lease, "review ready")}); err != nil {
				t.Fatal(err)
			}
			queued, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: run.BotID, Version: run.Version, Trigger: store.TriggerManual, Instructions: "pending work", IdempotencyKey: "pending"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, notifications := assertCommittedRunStateNotifications(t, repo, ctx)
			applied, err := repo.ConfirmControlProposal(ctx, store.BotControlProposalDecision{BotID: p.BotID, ProposalID: p.ID, Digest: p.Digest})
			if err != nil || applied.Status != store.BotControlApplied {
				t.Fatalf("apply=%+v %v", applied, err)
			}
			if len(*notifications) != 1 || (*notifications)[0].RunID != queued.ID || (*notifications)[0].Status != store.RunCancelled {
				t.Fatalf("configuration notifications: %+v", *notifications)
			}
			assertRunStateSequence(t, repo, ctx, *queued, store.RunQueued, store.RunCancelled)
		})
	}
}

func TestBotRunStateNestedTitleNotifications(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, b := messageReviewDraft(t, scope)
			p, _ := execution.PrincipalFromContext(ctx)
			ctx, err := execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(1))
			calls := 0
			ctx = store.WithBotRunStateNotifier(ctx, func(_ context.Context, state store.BotRunState) {
				calls++
				run, err := repo.GetRun(ctx, state.BotID, state.RunID)
				if err != nil || run.Status != store.RunQueued {
					t.Fatalf("nested callback before commit: %+v %v", run, err)
				}
			})
			version := store.DefaultBotVersion()
			version.Charter = "Discuss the title"
			q := store.BotCreateRequest{Name: "Nested title", OwnerID: b.OwnerID, Version: version, IdempotencyKey: "nested-title"}
			receipt, err := repo.CreateDraftAndStart(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.CreateDraftAndStart(ctx, q); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("nested/replay callbacks=%d", calls)
			}
			assertRunStateSequence(t, repo, ctx, receipt.Run, store.RunQueued)
		})
	}
}

func TestBotRunStateReviewedTaskOnce(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, p := remainingProposal(t, scope, store.BotControlTaskOnceOperation, store.BotActive)
			ctx, notifications := assertCommittedRunStateNotifications(t, repo, ctx)
			decision := store.BotControlProposalDecision{BotID: p.BotID, ProposalID: p.ID, Digest: p.Digest}
			_, err := repo.ConfirmControlProposal(ctx, decision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.ConfirmControlProposal(ctx, decision); err != nil {
				t.Fatal(err)
			}
			runID := uuid.NewSHA1(p.ID, []byte("run"))
			if len(*notifications) != 1 || (*notifications)[0].RunID != runID || (*notifications)[0].Status != store.RunQueued {
				t.Fatalf("reviewed execution/replay notifications: %+v", *notifications)
			}
			run, err := repo.GetRun(ctx, p.BotID, runID)
			if err != nil {
				t.Fatal(err)
			}
			assertRunStateSequence(t, repo, ctx, *run, store.RunQueued)
		})
	}
}
