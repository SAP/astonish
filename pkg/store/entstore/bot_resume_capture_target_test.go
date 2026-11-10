package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestResumeCaptureRejectsTriggerIDBeforePending(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			created, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "scheduled")}); err != nil {
				t.Fatal(err)
			}
			created, err = s.ApplyAuthorizedOperation(owner, q.MandateRef, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			scheduleID := created.Receipt.Schedule.ScheduleID
			b, err := r.Get(owner, q.MandateRef.BotID)
			if err != nil {
				t.Fatal(err)
			}
			v, err := r.GetVersion(owner, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			var triggerID uuid.UUID
			for _, tr := range v.Triggers {
				if tr.ScheduleID != nil && *tr.ScheduleID == scheduleID {
					triggerID = tr.ID
				}
			}
			if triggerID == uuid.Nil || triggerID == scheduleID {
				t.Fatal("fixture must distinguish trigger and schedule")
			}
			b, err = r.Pause(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "pause"})
			if err != nil {
				t.Fatal(err)
			}
			policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, e := authorizedSchedulePolicy(ctx, ref)
				p.Constraints = append(p.Constraints, execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: []string{"propose_bot_control", RoutineControlResource(store.BotControlResumeTasks)}})
				return p, e
			}
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			defer registry.Close()
			owner = store.WithBotMessageAdmission(store.WithBotMandateAdmission(owner, policy), registry)
			admission, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "resume", Instructions: "Resume only the report"})
			if err != nil {
				t.Fatal(err)
			}
			ref := *admission.Run.MandateRef
			worker = execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: admission.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
			lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: admission.Run.ID, WorkerID: "resume", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			s.policy = policy
			s.authorize = func(context.Context, store.BotScheduleAuthorization) error { return nil }
			before, err := r.db.GetBot(owner, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			jobs := r.db.(botTriggerDB).triggerScheduler()
			jobBefore := jobs.Get(owner, scheduleID.String())
			// A real scheduler ID belonging to another Bot is not a target of this Bot.
			foreign := *jobBefore
			foreign.ID = uuid.NewString()
			foreign.Payload.BotID = uuid.NewString()
			if err := jobs.Add(owner, &foreign); err != nil {
				t.Fatal(err)
			}
			foreign.Enabled = false
			foreign.NextRun = nil
			if err := jobs.Update(owner, &foreign); err != nil {
				t.Fatal(err)
			}
			q = store.BotAuthorizedOperationRequest{Lease: lease, MandateRef: ref, IdempotencyKey: "resume-call", Payload: store.BotControlPayload{Operation: store.BotControlResumeTasks, Reason: "resume selected report", ResumeTargets: &store.BotControlResumeTargets{}}}
			for _, bad := range []uuid.UUID{triggerID, uuid.New(), uuid.MustParse(foreign.ID)} {
				q.Payload.ResumeTargets.ScheduleIDs = []uuid.UUID{scheduleID, bad}
				op, err := s.EnqueueAuthorizedOperation(worker, q)
				if !errors.Is(err, store.ErrBotInvalid) || op != nil {
					t.Fatalf("invalid target captured: op=%+v err=%v", op, err)
				}
				var targetErr *store.BotResumeTargetError
				if !errors.As(err, &targetErr) || targetErr.ScheduleID != bad {
					t.Fatalf("missing exact typed target: %v", err)
				}
				after, e := r.db.GetBot(owner, b.ID)
				if e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("invalid target changed bot, metadata or outbox")
				}
				if !reflect.DeepEqual(jobBefore, jobs.Get(owner, scheduleID.String())) {
					t.Fatal("invalid target changed schedule")
				}
				ops, e := s.ListAuthorizedOperations(owner, ref)
				if e != nil || len(ops) != 0 {
					t.Fatalf("invalid target persisted operation: %+v %v", ops, e)
				}
				id := uuid.NewSHA1(lease.RunID, []byte("authorized-routine:"+q.IdempotencyKey))
				if _, e := r.db.FindActivity(owner, b.ID, routineGrantKey(id)); !errors.Is(e, store.ErrBotNotFound) {
					t.Fatalf("invalid target persisted grant: %v", e)
				}
			}
			// Correction in the same source turn and even the same key is safe: no bad capture existed.
			q.Payload.ResumeTargets.ScheduleIDs = []uuid.UUID{scheduleID}
			op, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil || op.Status != store.BotOperationPending || op.Decision != store.BotOperationExecute {
				t.Fatalf("canonical target: %+v %v", op, err)
			}
			if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, lease, "captured")}); err != nil {
				t.Fatal(err)
			}
			op, err = s.ApplyAuthorizedOperation(owner, ref, op.ID)
			if err != nil || op.Status != store.BotOperationApplied {
				t.Fatalf("canonical resume: %+v %v", op, err)
			}
			after, err := r.db.GetBot(owner, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Metadata[scheduledTaskHead(triggerID)], after.Metadata[scheduledTaskHead(triggerID)]) {
				t.Fatal("resume replaced original finite task grant")
			}
			if job := jobs.Get(owner, scheduleID.String()); job == nil || !job.Enabled || job.NextRun == nil {
				t.Fatal("canonical schedule not resumed")
			}
			if jobs.Get(owner, foreign.ID).Enabled {
				t.Fatal("foreign schedule resumed")
			}
		})
	}
}
