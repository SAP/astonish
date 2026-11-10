package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func nextAuthorizedSchedule(t *testing.T, r *botRepository, s *botAuthorizedOperationRuntime, owner context.Context, bot uuid.UUID, op store.BotControlOperation, tr store.TriggerSpec) *store.BotOperation {
	t.Helper()
	b, err := r.Get(owner, bot)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: bot, Version: b.Version, Instructions: "Every day at nine, read the report. Keep other schedules unchanged.", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	worker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: bot, RunID: admitted.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: bot, RunID: admitted.Run.ID, WorkerID: "next", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	q := store.BotAuthorizedOperationRequest{Lease: lease, MandateRef: *admitted.Run.MandateRef, IdempotencyKey: "next", Payload: store.BotControlPayload{Operation: op, Reason: "owner requested", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: tr}}}
	pending, err := s.EnqueueAuthorizedOperation(worker, q)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, lease, "queued")}); err != nil {
		t.Fatal(err)
	}
	applied, err := s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	return applied
}
func TestBotAuthorizedSchedulePreservesDisabledAndPaused(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			o, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "queued")}); err != nil {
				t.Fatal(err)
			}
			if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID); err != nil {
				t.Fatal(err)
			}
			b, _ := r.Get(owner, q.MandateRef.BotID)
			v, _ := r.db.ReadVersion(owner, b.ID, b.Version)
			first := v.Triggers[0]
			jobs := r.db.(botTriggerDB).triggerScheduler()
			job := jobs.Get(owner, first.ScheduleID.String())
			job.Enabled = false
			past := time.Now().Add(-time.Hour).UTC()
			job.NextRun = &past
			if err = jobs.Update(owner, job); err != nil {
				t.Fatal(err)
			}
			added := nextAuthorizedSchedule(t, r, s, owner, b.ID, store.BotControlScheduleCreate, store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 10 * * *", Timezone: "UTC", Instructions: "second", Enabled: true})
			preserved := jobs.Get(owner, first.ScheduleID.String())
			if preserved.Enabled || preserved.NextRun == nil || !preserved.NextRun.Equal(past) || preserved.Payload.BotVersion != added.Receipt.Schedule.Version {
				t.Fatalf("unrelated changed %+v", preserved)
			}
			b, _ = r.Get(owner, b.ID)
			v, _ = r.db.ReadVersion(owner, b.ID, b.Version)
			second := v.Triggers[1]
			second.Cron = "0 11 * * *"
			nextAuthorizedSchedule(t, r, s, owner, b.ID, store.BotControlScheduleEdit, second)
			nextAuthorizedSchedule(t, r, s, owner, b.ID, store.BotControlSchedulePause, second)
			if jobs.Get(owner, second.ScheduleID.String()).Enabled {
				t.Fatal("not paused")
			}
			nextAuthorizedSchedule(t, r, s, owner, b.ID, store.BotControlScheduleRemove, second)
			if jobs.Get(owner, second.ScheduleID.String()) != nil {
				t.Fatal("not removed")
			}
			b, _ = r.Get(owner, b.ID)
			if _, err = r.Pause(owner, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "stay paused"}); err != nil {
				t.Fatal(err)
			}
			// Paused owner turns are control-only, not fresh task/schedule authority.
			pausedPolicy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy,error) {
				p,e := authorizedSchedulePolicy(ctx,ref)
				p.Constraints = append(p.Constraints,execution.BotActionResourceConstraint{Action:execution.CapabilityToolExecute,Resources:[]string{"propose_bot_control"}})
				return p,e
			}
			admitted,e := r.AdmitMessage(store.WithBotMandateAdmission(owner,pausedPolicy),store.BotMessageRequest{BotID:b.ID,Version:b.Version,Instructions:"Review my schedules",IdempotencyKey:uuid.NewString()})
			if e != nil { t.Fatal(e) }
			runtime := &botMandateRuntime{repo:r,policy:pausedPolicy}
			if _,e=runtime.VerifyBotMandate(owner,*admitted.Run.MandateRef,execution.BotMandatePurposeOperation); e != nil { t.Fatal(e) }
			if _,e=runtime.VerifyBotMandate(owner,*admitted.Run.MandateRef,execution.BotMandatePurposeTask); e == nil { t.Fatal("paused turn granted task purpose") }
			b, _ = r.Get(owner, b.ID)
			if b.Status != store.BotPaused { t.Fatal("implicit resume") }
		})
	}
}
