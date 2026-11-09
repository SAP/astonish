package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotAuthorizedScheduleReopen(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			base, botID, _, _, reopen := ApprovalScopeReopenFixture(t, scope)
			owner := store.WithBotMandateAdmission(mandateOwnerContext(t, base), authorizedSchedulePolicy)
			bots, _ := reopen()
			admitted, err := bots.(store.BotMessageStore).AdmitMessage(owner, store.BotMessageRequest{BotID: botID, Version: 1, Instructions: "Schedule the report daily at nine UTC.", IdempotencyKey: "durable-source"})
			if err != nil {
				t.Fatal(err)
			}
			ref := *admitted.Run.MandateRef
			worker := execution.WithBotRunIdentity(base, execution.BotRunIdentity{BotID: botID, RunID: admitted.Run.ID, Version: 1, ActorID: ref.OwnerID, OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
			authorize := func(_ context.Context, a store.BotScheduleAuthorization) error {
				if a.Instructions != "Schedule the report daily at nine UTC." || a.Provenance.SourceRunID != admitted.Run.ID {
					return store.ErrBotForbidden
				}
				return nil
			}
			ready := func(context.Context, store.Bot, store.BotVersion) error { return nil }
			runtime, err := NewBotAuthorizedOperationRuntime(bots, authorizedSchedulePolicy, authorize, ready)
			if err != nil {
				t.Fatal(err)
			}
			r := runtime.(*botAuthorizedOperationRuntime).repo
			b, err := bots.Get(owner, botID)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: botID, RunID: admitted.Run.ID, WorkerID: "reopen", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotAuthorizedOperationRequest{Lease: lease, MandateRef: ref, IdempotencyKey: "durable-operation", Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "source instruction", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "read report", Enabled: true}}}}
			pending, err := runtime.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "queued"}}); err != nil {
				t.Fatal(err)
			}

			bots, _ = reopen()
			runtime, err = NewBotAuthorizedOperationRuntime(bots, authorizedSchedulePolicy, authorize, ready)
			if err != nil {
				t.Fatal(err)
			}
			queued, err := runtime.ListAuthorizedOperations(context.Background(), ref)
			if err != nil || len(queued) != 1 || queued[0].ID != pending.ID || queued[0].Status != store.BotOperationPending {
				t.Fatalf("queue after reopen: %+v %v", queued, err)
			}
			applied, err := runtime.ApplyAuthorizedOperation(context.Background(), ref, pending.ID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := applied.Receipt.Schedule
			if receipt.SourceRunID != admitted.Run.ID || receipt.UserEventID != admitted.Run.UserEventID || receipt.SourceDigest != pending.Provenance.SourceDigest {
				t.Fatal("receipt lost source linkage")
			}
			r = runtime.(*botAuthorizedOperationRuntime).repo
			rec, err := r.db.GetBot(context.Background(), botID)
			if err != nil {
				t.Fatal(err)
			}
			sequence := rec.LastSequence
			publication, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: botID, RunID: admitted.Run.ID, ReportSequence: sequence})
			if err != nil {
				t.Fatal(err)
			}

			bots, _ = reopen()
			runtime, err = NewBotAuthorizedOperationRuntime(bots, authorizedSchedulePolicy, authorize, ready)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := runtime.ApplyAuthorizedOperation(context.Background(), ref, pending.ID)
			if err != nil || !reflect.DeepEqual(replay, applied) {
				t.Fatalf("receipt replay after reopen: %+v %v", replay, err)
			}
			r = runtime.(*botAuthorizedOperationRuntime).repo
			publicationAgain, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: botID, RunID: admitted.Run.ID, ReportSequence: sequence})
			if err != nil || publicationAgain.EventID != publication.EventID {
				t.Fatalf("publication after reopen: %+v %v", publicationAgain, err)
			}
			job := r.db.(botTriggerDB).triggerScheduler().Get(context.Background(), receipt.ScheduleID.String())
			if job == nil || !job.Enabled || job.NextRun == nil || !job.NextRun.Equal(*receipt.NextRun) || job.Payload.BotVersion != receipt.Version {
				t.Fatalf("job after reopen: %+v", job)
			}
			// A terminated lease cannot capture even a replay after restart.
			if _, err = runtime.EnqueueAuthorizedOperation(worker, q); err == nil {
				t.Fatal("expired source lease captured")
			}
		})
	}
}

func TestBotAuthorizedScheduleReadinessPolicyFence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, _, worker, q := authorizedScheduleFixture(t, scope)
			pending, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "queued"}}); err != nil {
				t.Fatal(err)
			}
			s.readiness = func(context.Context, store.Bot, store.BotVersion) error {
				s.policy = func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
					return store.BotMandatePolicy{}, store.ErrBotForbidden
				}
				return nil
			}
			if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, pending.ID); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("policy changed during readiness: %v", err)
			}
			saved, err := s.ReadAuthorizedOperation(context.Background(), q.MandateRef, pending.ID)
			if err != nil || saved.Status != store.BotOperationPending {
				t.Fatalf("failed fence lost queue: %+v %v", saved, err)
			}
			rec, err := r.db.GetBot(context.Background(), q.MandateRef.BotID)
			if err != nil || rec.Bot.Version != pending.Provenance.Version {
				t.Fatalf("failed fence changed root: %+v %v", rec, err)
			}
		})
	}
}
