package entstore

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func pendingControlSafety(t *testing.T, r *botRepository, ctx context.Context, b *store.Bot, payload store.BotControlPayload) *store.BotControlProposal {
	t.Helper()
	intent := store.BotIntentRegular
	if b.Status == store.BotPaused {
		intent = store.BotIntentConversation
	}
	run, err := r.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: intent, Trigger: store.TriggerManual, Instructions: "review", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "review", ExpiresAt: r.clock().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "review", ExpiresAt: r.clock().Add(time.Hour), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, lease, "ready")}); err != nil {
		t.Fatal(err)
	}
	return p
}

func scheduledControlSafety(t *testing.T, scope store.BotScope) (*botRepository, context.Context, *store.Bot, string) {
	t.Helper()
	r, ctx, worker, run, lease, _ := botRunFixture(t, scope)
	if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, lease, "done")}); err != nil {
		t.Fatal(err)
	}
	b, err := r.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	reviewedControl(t, r, ctx, store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "daily", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 8 * * *", Timezone: "UTC", Instructions: "check", Enabled: true}}})
	b, err = r.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.db.ReadVersion(ctx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	return r, ctx, b, v.Triggers[0].ScheduleID.String()
}

func TestBotControlProposalResumePreservesSchedules(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, schedule := scheduledControlSafety(t, scope)
			b, err := r.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "pause", Now: r.clock()})
			if err != nil {
				t.Fatal(err)
			}
			jobs := r.db.(botTriggerDB).triggerScheduler()
			before := jobs.Get(ctx, schedule)
			if before == nil || before.Enabled || before.NextRun != nil {
				t.Fatalf("not paused: %+v", before)
			}
			r.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
			p := pendingControlSafety(t, r, ctx, b, store.BotControlPayload{Operation: store.BotControlResume, Reason: "resume without enabling schedules"})
			got, err := r.ConfirmControlProposal(ctx, store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest})
			if err != nil || got.Status != store.BotControlApplied {
				t.Fatalf("resume: %+v %v", got, err)
			}
			assertRemainingOutcome(t, r, ctx, p)
			after := jobs.Get(ctx, schedule)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("resume changed schedule: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestBotControlProposalConfigurationPauseRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, schedule := scheduledControlSafety(t, scope)
			v, err := r.db.ReadVersion(ctx, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			p := pendingControlSafety(t, r, ctx, b, store.BotControlPayload{Operation: store.BotControlConfigurationOperation, Reason: "pause for reviewed config", Configuration: &store.BotControlConfiguration{Pause: true, Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery}})
			queued, err := r.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "queued", IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			jobs := r.db.(botTriggerDB).triggerScheduler()
			before := jobs.Get(ctx, schedule)
			if before == nil || !before.Enabled {
				t.Fatal("expected enabled schedule")
			}
			original := r.db
			r.db = controlReceiptFailTransaction{original}
			d := store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}
			_, err = r.ConfirmControlProposal(ctx, d)
			r.db = original
			if err == nil || err.Error() != "injected receipt failure" {
				t.Fatalf("receipt fault: %v", err)
			}
			after, err := r.Get(ctx, b.ID)
			if err != nil || after.Status != store.BotActive || after.Version != b.Version || after.Revision != b.Revision {
				t.Fatalf("root escaped rollback: %+v %v", after, err)
			}
			if !reflect.DeepEqual(before, jobs.Get(ctx, schedule)) {
				t.Fatal("schedule escaped rollback")
			}
			run, err := r.db.ReadRun(ctx, b.ID, queued.ID)
			if err != nil || run.Run.Status != store.RunQueued {
				t.Fatalf("run escaped rollback: %+v %v", run, err)
			}
			saved, err := r.ReadControlProposal(ctx, b.ID, p.ID)
			if err != nil || saved.Status != store.BotControlProposed {
				t.Fatalf("receipt escaped rollback: %+v %v", saved, err)
			}
			applied, err := r.ConfirmControlProposal(ctx, d)
			if err != nil || applied.Status != store.BotControlApplied {
				t.Fatalf("retry: %+v %v", applied, err)
			}
			after, err = r.Get(ctx, b.ID)
			if err != nil || after.Status != store.BotPaused || after.Version != b.Version+1 {
				t.Fatalf("configuration not paused: %+v %v", after, err)
			}
			job := jobs.Get(ctx, schedule)
			if job == nil || job.Enabled || job.NextRun != nil {
				t.Fatalf("schedule not disabled: %+v", job)
			}
			run, err = r.db.ReadRun(ctx, b.ID, queued.ID)
			if err != nil || run.Run.Status != store.RunCancelled {
				t.Fatalf("queued run not quiesced: %+v %v", run, err)
			}
		})
	}
}
