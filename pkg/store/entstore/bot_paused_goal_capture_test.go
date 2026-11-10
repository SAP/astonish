package entstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestPausedGoalCaptureRequiresCompletionCriteria(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			schedule, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "schedule captured")}); err != nil {
				t.Fatal(err)
			}
			schedule, err = s.ApplyAuthorizedOperation(owner, q.MandateRef, schedule.ID)
			if err != nil {
				t.Fatal(err)
			}
			b, err := r.Get(owner, q.MandateRef.BotID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = r.Pause(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Pause independent work"})
			if err != nil {
				t.Fatal(err)
			}
			policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, e := authorizedSchedulePolicy(ctx, ref)
				p.Constraints[0].Resources = append(p.Constraints[0].Resources, "propose_bot_control", RoutineControlResource(store.BotControlGoalCreate))
				return p, e
			}
			owner = store.WithBotMessageAdmission(store.WithBotMandateAdmission(owner, policy), execution.NewBotTurnAuthorizationRegistry(10))
			s.policy = policy
			const instruction = "Keep a goal named Orlando weather clarity: make each weather update easy to understand. Stay paused."
			admission, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: instruction, IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			q.MandateRef = *admission.Run.MandateRef
			worker = execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: admission.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: q.MandateRef.OrgSlug, TeamSlug: q.MandateRef.TeamSlug})
			q.Lease, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: admission.Run.ID, WorkerID: "paused-goal", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			s.authorize = func(_ context.Context, a store.BotScheduleAuthorization) error {
				if a.Instructions != instruction || a.Provenance.SourceRunID != admission.Run.ID || a.Mandate.Purpose != execution.BotMandatePurposeOperation {
					return store.ErrBotForbidden
				}
				return nil
			}
			q.Payload = store.BotControlPayload{Operation: store.BotControlGoalCreate, Reason: "Owner requested a durable goal to keep weather updates easy to understand.", Goal: &store.BotControlGoal{Goal: store.BotGoal{BotID: b.ID, Title: "Orlando weather clarity", Description: "Make each Orlando weather update easy to understand.", Status: store.BotGoalActive}}}
			q.IdempotencyKey = "missing-criteria"
			before, _ := r.db.GetBot(owner, b.ID)
			op, err := s.EnqueueAuthorizedOperation(worker, q)
			if !errors.Is(err, store.ErrBotInvalid) || !strings.Contains(err.Error(), "completion_criteria") {
				// Diagnose the original failure beyond the reconciler's generic receipt.
				if err == nil {
					if e := r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "captured"}}); e != nil {
						t.Fatal(e)
					}
					_, applyErr := s.ApplyAuthorizedOperation(owner, q.MandateRef, op.ID)
					t.Logf("original missing-criteria capture accepted; direct apply error = %v", applyErr)
				}
				t.Fatalf("incomplete goal should fail in source turn, got op=%+v err=%v", op, err)
			}
			after, _ := r.db.GetBot(owner, b.ID)
			if after.LastSequence != before.LastSequence {
				t.Fatal("invalid goal persisted a grant or operation")
			}
			if _, err = r.checkLease(worker, r.db, q.Lease, r.clock()); err != nil {
				t.Fatal("invalid input ended source", err)
			}
			q.Payload.Goal.Goal.CompletionCriteria = "Each weather update is easy to understand."
			q.IdempotencyKey = "corrected-criteria"
			op, err = s.EnqueueAuthorizedOperation(worker, q)
			if err != nil || op.Status != store.BotOperationPending || op.Decision != store.BotOperationExecute {
				t.Fatalf("corrected capture: %+v %v", op, err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "captured")}); err != nil {
				t.Fatal(err)
			}
			if err = s.ReconcileAuthorizedOperations(owner, b.ID, 32); err != nil {
				t.Fatal(err)
			}
			op, err = s.ReadAuthorizedOperation(owner, q.MandateRef, op.ID)
			if err != nil || op.Status != store.BotOperationApplied {
				t.Fatalf("corrected apply: %+v %v", op, err)
			}
			goal, err := r.db.ReadGoal(owner, b.ID, uuid.NewSHA1(op.ID, []byte("goal")))
			if err != nil || goal.CompletionCriteria != q.Payload.Goal.Goal.CompletionCriteria {
				t.Fatalf("goal: %+v %v", goal, err)
			}
			after, _ = r.db.GetBot(owner, b.ID)
			job := r.db.(botTriggerDB).triggerScheduler().Get(owner, schedule.Receipt.Schedule.ScheduleID.String())
			if after.Bot.Status != store.BotPaused || after.ActiveRunID != nil || job == nil || job.Enabled || job.NextRun != nil {
				t.Fatal("goal changed paused independent work")
			}
			if _, err = s.VerifyBotMandate(owner, q.MandateRef, execution.BotMandatePurposeTask); err == nil {
				t.Fatal("goal granted task authority")
			}
		})
	}
}
