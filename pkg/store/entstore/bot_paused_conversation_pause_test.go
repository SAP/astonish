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

// A second pause is not an explicit request to stop the current owner turn.
func TestRoutinePauseAlreadyPausedConversation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, scoped, ctx, initial, _ := runtimeFixture(t, scope)
			policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, err := authorizedSchedulePolicy(ctx, ref)
				p.Constraints = append(p.Constraints, execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: []string{"propose_bot_control", RoutineControlResource(store.BotControlPriorityPause)}})
				return p, err
			}
			owner := store.WithBotMandateAdmission(mandateOwnerContext(t, ctx), policy)
			b, err := r.Get(owner, initial.BotID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = r.Pause(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Pause independent work"})
			if err != nil {
				t.Fatal(err)
			}
			owner = store.WithBotMessageAdmission(owner, execution.NewBotTurnAuthorizationRegistry(10))
			admission, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "units", Instructions: "Which temperature units do I prefer?"})
			if err != nil {
				t.Fatal(err)
			}
			run := admission.Run
			if run.Intent != store.BotIntentConversation || !run.IsInteractiveChat() {
				t.Fatalf("not paused interactive: %+v", run)
			}
			principal, _ := execution.PrincipalFromContext(owner)
			worker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug})
			lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "conversation", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			rt, err := NewBotAuthorizedOperationRuntime(scoped, policy, func(_ context.Context, a store.BotScheduleAuthorization) error {
				if a.Instructions != "Which temperature units do I prefer?" || a.Provenance.SourceRunID != run.ID || a.Provenance.UserEventID != run.UserEventID {
					return store.ErrBotForbidden
				}
				return nil
			}, func(context.Context, store.Bot, store.BotVersion) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			before, err := r.db.GetBot(owner, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotAuthorizedOperationRequest{Lease: lease, MandateRef: *run.MandateRef, IdempotencyKey: "new-model-call", Payload: store.BotControlPayload{Operation: store.BotControlPriorityPause, Reason: "Earlier pause request remains in force"}}
			for _, key := range []string{"new-model-call", "new-model-call", "another-model-call"} {
				q.IdempotencyKey = key
				op, err := rt.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				if op.Status != store.BotOperationApplied || op.Receipt == nil {
					t.Fatalf("no durable receipt: %+v", op)
				}
				after, err := r.db.GetBot(owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				if after.Fence != before.Fence || after.Bot.Revision != before.Bot.Revision || after.Bot.Status != store.BotPaused || after.ActiveRunID == nil || *after.ActiveRunID != run.ID {
					t.Fatalf("redundant pause fenced conversation: fence %d -> %d, revision %d -> %d", before.Fence, after.Fence, before.Bot.Revision, after.Bot.Revision)
				}
				if _, err := r.checkLease(worker, r.db, lease, r.clock()); err != nil {
					t.Fatal("conversation lease revoked", err)
				}
			}
			if _, err := r.RequestRun(owner, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "background", IdempotencyKey: "background"}); err == nil {
				t.Fatal("autonomous admission reopened")
			}
			if _, err := r.GetVersion(worker, b.ID, b.Version); err != nil {
				t.Fatal("current version unreadable", err)
			}
			if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, lease, "Celsius")}); err != nil {
				t.Fatal("conversation cannot finish", err)
			}
			// Another explicit conversation can still be stopped using the exact-run path.
			next, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "next", Instructions: "Another question"})
			if err != nil {
				t.Fatal(err)
			}
			worker = execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: next.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug})
			lease, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: next.Run.ID, WorkerID: "conversation", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.CancelRun(owner, b.ID, next.Run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = r.checkLease(worker, r.db, lease, r.clock()); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("explicit stop failed: %v", err)
			}
		})
	}
}

func TestRoutinePriorityPauseLifecycleMatrix(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, status := range []store.BotStatus{store.BotActive, store.BotDraft, store.BotPaused, store.BotStopped, store.BotDeleted} {
			t.Run(string(scope)+"/"+string(status), func(t *testing.T) {
				r, s, owner, _, q := authorizedScheduleFixture(t, scope)
				// Exercise the transition primitive against each lifecycle; run admission is
				// tested separately and is never inferred from model intent or reason prose.
				err := r.db.Transaction(owner, func(tx botDraftDB) error {
					rec, err := tx.GetBot(owner, q.Lease.BotID)
					if err != nil {
						return err
					}
					next := rec.Bot
					next.Status = status
					next.Revision++
					if err := tx.SetLifecycle(owner, *rec, next); err != nil {
						return err
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				before, _ := r.db.GetBot(owner, q.Lease.BotID)
				op := store.BotOperation{ID: uuid.New(), IdempotencyKey: "matrix", MandateRef: q.MandateRef, Payload: store.BotControlPayload{Operation: store.BotControlPriorityPause, Reason: "pause"}, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Revision: 1}
				err = r.db.Transaction(owner, func(tx botDraftDB) error {
					rec, err := tx.GetBot(owner, q.Lease.BotID)
					if err != nil {
						return err
					}
					return s.pauseRoutineTx(owner, tx, rec, &op)
				})
				if status == store.BotStopped || status == store.BotDeleted {
					if !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("inactive accepted: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				after, _ := r.db.GetBot(owner, q.Lease.BotID)
				saved, err := r.GetRun(owner, q.Lease.BotID, q.Lease.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if status == store.BotPaused {
					if before.Fence != after.Fence {
						t.Fatal("repeat pause advanced fence")
					}
				} else if after.Fence != before.Fence+1 || saved.Status != store.RunInterrupted {
					t.Fatal("first pause did not interrupt active work")
				}
			})
		}
	}
}
