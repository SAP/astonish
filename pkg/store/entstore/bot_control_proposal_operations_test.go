package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func reviewedControl(t *testing.T, r *botRepository, ctx context.Context, payload store.BotControlPayload) *store.BotControlProposal {
	t.Helper()
	bots, err := r.List(ctx, store.BotPage{})
	if err != nil || len(bots) != 1 {
		t.Fatalf("list: %v %v", bots, err)
	}
	b := bots[0]
	run, err := r.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, IdempotencyKey: uuid.NewString(), Instructions: "review"})
	if err != nil {
		t.Fatal(err)
	}
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "worker", ExpiresAt: r.clock().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "review", ExpiresAt: r.clock().Add(time.Hour), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "review ready"}}); err != nil {
		t.Fatal(err)
	}
	applied, err := r.ConfirmControlProposal(ctx, store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest})
	if err != nil || applied.Status != store.BotControlApplied {
		t.Fatalf("apply %s: %+v %v", payload.Operation, applied, err)
	}
	return applied
}

func TestBotControlProposalScheduleGoalConfiguration(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "done"}}); err != nil {
				t.Fatal(err)
			}
			b, _ := r.Get(ctx, run.BotID)
			reviewedControl(t, r, ctx, store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "daily check", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "America/New_York", Instructions: "check", Enabled: true}}})
			b, _ = r.Get(ctx, b.ID)
			v, _ := r.db.ReadVersion(ctx, b.ID, b.Version)
			tr := v.Triggers[0]
			jobs := r.db.(botTriggerDB).triggerScheduler()
			job := jobs.Get(ctx, tr.ScheduleID.String())
			expected, _ := triggerNext(tr, r.clock())
			if job == nil || !job.Enabled || job.NextRun == nil || !job.NextRun.Equal(expected) || job.Payload.BotVersion != b.Version {
				t.Fatalf("schedule: %+v", job)
			}
			preserved := r.clock().Add(-time.Minute)
			job.NextRun = &preserved
			if err := jobs.Update(ctx, job); err != nil {
				t.Fatal(err)
			}
			text := "Changed responsibility with schedules"
			reviewedControl(t, r, ctx, store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "clarify", Responsibility: &text})
			b, _ = r.Get(ctx, b.ID)
			job = jobs.Get(ctx, tr.ScheduleID.String())
			if job.Payload.BotVersion != b.Version || job.NextRun == nil || !job.NextRun.Equal(preserved) {
				t.Fatal("stale schedule version")
			}
			tr.Cron = "30 10 * * *"
			reviewedControl(t, r, ctx, store.BotControlPayload{Operation: store.BotControlScheduleEdit, Reason: "later", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: tr}})
			b, _ = r.Get(ctx, b.ID)
			reviewedControl(t, r, ctx, store.BotControlPayload{Operation: store.BotControlSchedulePause, Reason: "pause", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: tr}})
			job = jobs.Get(ctx, tr.ScheduleID.String())
			if job.Enabled || job.NextRun != nil {
				t.Fatal("schedule not paused")
			}
			b, _ = r.Get(ctx, b.ID)
			reviewedControl(t, r, ctx, store.BotControlPayload{Operation: store.BotControlScheduleRemove, Reason: "remove", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: tr}})
			if jobs.Get(ctx, tr.ScheduleID.String()) != nil {
				t.Fatal("schedule retained")
			}
			p := reviewedControl(t, r, ctx, store.BotControlPayload{Operation: store.BotControlGoalCreate, Reason: "goal", Goal: &store.BotControlGoal{Goal: store.BotGoal{BotID: b.ID, Title: "Observe", CompletionCriteria: "Report exists"}}})
			g, err := r.db.ReadGoal(ctx, b.ID, uuid.NewSHA1(p.ID, []byte("goal")))
			if err != nil {
				t.Fatal(err)
			}
			g.Title = "Observe carefully"
			reviewedControl(t, r, ctx, store.BotControlPayload{Operation: store.BotControlGoalEdit, Reason: "clarify", Goal: &store.BotControlGoal{Goal: *g, ExpectedRevision: g.Revision}})
			updated, _ := r.db.ReadGoal(ctx, b.ID, g.ID)
			if updated.Revision != 2 || updated.Title != g.Title {
				t.Fatal("goal edit missing")
			}
			b, _ = r.Get(ctx, b.ID)
			v, _ = r.db.ReadVersion(ctx, b.ID, b.Version)
			reviewedControl(t, r, ctx, store.BotControlPayload{Operation: store.BotControlConfigurationOperation, Reason: "review and pause", Configuration: &store.BotControlConfiguration{Pause: true, Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery}})
			b, _ = r.Get(ctx, b.ID)
			if b.Status != store.BotPaused {
				t.Fatal("configuration resumed active bot")
			}
		})
	}
}

func TestBotControlProposalConcurrentDecision(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			text := "Once"
			p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "race", ExpiresAt: r.clock().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "race", Responsibility: &text}})
			if err != nil {
				t.Fatal(err)
			}
			d := store.BotControlProposalDecision{BotID: run.BotID, ProposalID: p.ID, Digest: p.Digest}
			if _, err = r.ConfirmControlProposal(worker, d); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("worker: %v", err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "ready"}}); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); _, err := r.ConfirmControlProposal(ctx, d); errs <- err }()
			}
			wg.Wait()
			close(errs)
			successes := 0
			for err := range errs {
				if err == nil {
					successes++
				}
			}
			if successes == 0 {
				t.Fatal("neither decision succeeded")
			}
			b, _ := r.Get(ctx, run.BotID)
			if b.Version != p.Version+1 {
				t.Fatalf("double apply: %+v", b)
			}
			saved, err := r.ReadControlProposal(ctx, run.BotID, p.ID)
			if err != nil || saved.Status != store.BotControlApplied {
				t.Fatalf("receipt: %+v %v", saved, err)
			}
		})
	}
}
