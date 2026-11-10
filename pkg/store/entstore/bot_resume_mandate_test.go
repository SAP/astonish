package entstore

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestRoutineResumeAfterPausedResponsibilityUpdate(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			finish := func() {
				t.Helper()
				if err := r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "captured")}); err != nil {
					t.Fatal(err)
				}
			}
			apply := func() *store.BotOperation {
				t.Helper()
				op, err := s.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				if op.Decision != store.BotOperationExecute {
					t.Fatalf("redundant authorization: %+v", op)
				}
				finish()
				op, err = s.ApplyAuthorizedOperation(owner, q.MandateRef, op.ID)
				if err != nil {
					t.Fatalf("apply %s: %v", q.Payload.Operation, err)
				}
				if op.Status != store.BotOperationApplied {
					t.Fatal(op)
				}
				return op
			}
			first := apply()
			target := first.Receipt.Schedule.ScheduleID
			policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, e := authorizedSchedulePolicy(ctx, ref)
				p.Limits.MaxDailyRuns = 100
				p.Constraints[0].Resources = append(p.Constraints[0].Resources, "propose_bot_control", RoutineControlResource(store.BotControlResponsibility), RoutineControlResource(store.BotControlResumeTasks))
				return p, e
			}
			owner = store.WithBotMessageAdmission(store.WithBotMandateAdmission(owner, policy), execution.NewBotTurnAuthorizationRegistry(10))
			s.policy = policy
			var instruction string
			s.authorize = func(_ context.Context, a store.BotScheduleAuthorization) error {
				if a.Instructions != instruction || a.Provenance.SourceRunID != q.Lease.RunID || a.Provenance.UserEventID == "" {
					return store.ErrBotForbidden
				}
				return nil
			}
			admit := func(text string) {
				t.Helper()
				instruction = text
				b, err := r.Get(owner, q.Lease.BotID)
				if err != nil {
					t.Fatal(err)
				}
				admission, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: text, IdempotencyKey: uuid.NewString()})
				if err != nil {
					t.Fatal(err)
				}
				q.MandateRef = *admission.Run.MandateRef
				worker = execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: admission.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: q.MandateRef.OrgSlug, TeamSlug: q.MandateRef.TeamSlug})
				q.Lease, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: admission.Run.ID, WorkerID: "resume-test", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				q.IdempotencyKey = uuid.NewString()
			}
			// A second current-version schedule deliberately disabled before global pause.
			admit("Keep the other report disabled")
			b, _ := r.Get(owner, q.Lease.BotID)
			q.Payload = store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: instruction, Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 10 * * *", Timezone: "UTC", Instructions: "other report", Enabled: false}}}
			other := apply().Receipt.Schedule.ScheduleID
			b, _ = r.Get(owner, q.Lease.BotID)
			if _, err := r.Pause(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Pause until explicitly resumed"}); err != nil {
				t.Fatal(err)
			}
			checkDisabled := func() {
				t.Helper()
				for _, id := range []uuid.UUID{target, other} {
					job := r.db.(botTriggerDB).triggerScheduler().Get(owner, id.String())
					if job == nil || job.Enabled || job.NextRun != nil {
						t.Fatalf("paused job enabled: %+v", job)
					}
				}
			}
			checkDisabled()
			admit("Make weather reports easy to understand. Stay paused.")
			text := "Make weather reports easy to understand"
			q.Payload = store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: instruction, Responsibility: &text}
			apply()
			checkDisabled()
			b, _ = r.Get(owner, q.Lease.BotID)
			if b.Status != store.BotPaused {
				t.Fatal("responsibility resumed bot")
			}
			admit("Resume only the weather updates now")
			saved, err := s.ReadBotMandate(owner, q.MandateRef)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Mandate.Purpose != execution.BotMandatePurposeOperation || !routineAllowed(saved.Mandate, store.BotControlResumeTasks) {
				t.Fatal("paused mandate lost targeted resume")
			}
			q.Payload = store.BotControlPayload{Operation: store.BotControlResumeTasks, Reason: instruction, ResumeTargets: &store.BotControlResumeTargets{ScheduleIDs: []uuid.UUID{target}}}
			apply()
			b, _ = r.Get(owner, q.Lease.BotID)
			if b.Status != store.BotActive {
				t.Fatal("not resumed")
			}
			jobs := r.db.(botTriggerDB).triggerScheduler()
			if job := jobs.Get(owner, target.String()); job == nil || !job.Enabled || job.NextRun == nil {
				t.Fatalf("target not resumed: %+v", job)
			}
			if job := jobs.Get(owner, other.String()); job == nil || job.Enabled || job.NextRun != nil {
				t.Fatalf("unrelated job resumed: %+v", job)
			}
			proposals, err := r.ListControlProposals(owner, b.ID)
			if err != nil || len(proposals) != 0 {
				t.Fatalf("redundant reviews: %+v %v", proposals, err)
			}
		})
	}
}
