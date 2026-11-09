package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func authorizedSchedulePolicy(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
	p, e := mandateTestPolicy(ctx, ref)
	p.Limits.MaxSchedules = 4
	return p, e
}

func authorizedScheduleFixture(t *testing.T, scope store.BotScope) (*botRepository, *botAuthorizedOperationRuntime, context.Context, context.Context, store.BotAuthorizedOperationRequest) {
	t.Helper()
	r, bots, base, initial, _ := runtimeFixture(t, scope)
	owner := store.WithBotMandateAdmission(mandateOwnerContext(t, base), authorizedSchedulePolicy)
	receipt, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: initial.BotID, Version: 1, Instructions: "Every day at nine, read the report. Keep other schedules unchanged.", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	ref := *receipt.Run.MandateRef
	b, err := r.Get(owner, ref.BotID)
	if err != nil {
		t.Fatal(err)
	}
	worker := execution.WithBotRunIdentity(base, execution.BotRunIdentity{BotID: b.ID, RunID: receipt.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
	lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: receipt.Run.ID, WorkerID: "schedule-test", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewBotAuthorizedOperationRuntime(bots, authorizedSchedulePolicy, func(_ context.Context, a store.BotScheduleAuthorization) error {
		if a.Instructions != "Every day at nine, read the report. Keep other schedules unchanged." || a.Provenance.UserEventID == "" {
			return store.ErrBotForbidden
		}
		return nil
	}, func(context.Context, store.Bot, store.BotVersion) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	q := store.BotAuthorizedOperationRequest{Lease: lease, MandateRef: ref, IdempotencyKey: "schedule", Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "Requested daily report", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "read the report", Enabled: true}}}}
	return r, rt.(*botAuthorizedOperationRuntime), owner, worker, q
}
func TestBotAuthorizedScheduleBusyRetryReadiness(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			o, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			again, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil || again.ID != o.ID {
				t.Fatalf("retry %v", err)
			}
			changed := q
			payload := *q.Payload.Schedule
			changed.Payload.Schedule = &payload
			payload.Trigger.Instructions = "different"
			if _, err = s.EnqueueAuthorizedOperation(worker, changed); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("digest %v", err)
			}
			if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID); !errors.Is(err, store.ErrBotBusy) {
				t.Fatalf("busy %v", err)
			}
			if _, err = s.TransitionBotOperation(context.Background(), q.MandateRef, o.ID, 1, store.BotOperationApplying, store.BotOperationReceipt{Status: store.BotOperationApplying}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("generic transition %v", err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "queued"}}); err != nil {
				t.Fatal(err)
			}
			s.readiness = func(context.Context, store.Bot, store.BotVersion) error { return store.ErrBotUnsupported }
			if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("readiness %v", err)
			}
			current, _ := s.ReadAuthorizedOperation(context.Background(), q.MandateRef, o.ID)
			if current.Status != store.BotOperationPending {
				t.Fatal("readiness changed queue")
			}
			s.readiness = func(context.Context, store.Bot, store.BotVersion) error { return nil }
			applied, err := s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID)
			if err != nil {
				t.Fatal(err)
			}
			if applied.Status != store.BotOperationApplied || applied.Receipt.Schedule == nil || !applied.Receipt.Schedule.Enabled || applied.Receipt.Schedule.NextRun == nil {
				t.Fatalf("receipt %+v", applied)
			}
			rec, _ := r.db.GetBot(context.Background(), q.MandateRef.BotID)
			seq := rec.LastSequence
			replay, err := s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID)
			if err != nil || replay.Revision != applied.Revision {
				t.Fatalf("apply retry %v", err)
			}
			rec, _ = r.db.GetBot(context.Background(), q.MandateRef.BotID)
			if rec.LastSequence != seq {
				t.Fatal("duplicate receipt")
			}
			pub, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.MandateRef.BotID, RunID: q.Lease.RunID, ReportSequence: seq})
			if err != nil {
				t.Fatal(err)
			}
			pubAgain, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.MandateRef.BotID, RunID: q.Lease.RunID, ReportSequence: seq})
			if err != nil || pub.EventID != pubAgain.EventID {
				t.Fatalf("publication retry %v", err)
			}
			list, err := s.ListAuthorizedOperations(context.Background(), q.MandateRef)
			if err != nil || len(list) != 1 {
				t.Fatalf("list %v %+v", err, list)
			}
		})
	}
}
func TestBotAuthorizedScheduleRevokedAndDenied(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			s.authorize = func(context.Context, store.BotScheduleAuthorization) error { return store.ErrBotForbidden }
			if _, err := s.EnqueueAuthorizedOperation(worker, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal(err)
			}
			s.authorize = func(context.Context, store.BotScheduleAuthorization) error { return nil }
			o, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "queued"}}); err != nil {
				t.Fatal(err)
			}
			if err = s.RevokeBotMandate(owner, q.MandateRef); err != nil {
				t.Fatal(err)
			}
			if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID); err == nil {
				t.Fatal("revoked applied")
			}
			rec, _ := r.db.GetBot(owner, q.MandateRef.BotID)
			if rec.Bot.Version != 1 {
				t.Fatal("revocation changed version")
			}
		})
	}
}
func TestBotAuthorizedScheduleBounds(t *testing.T) {
	p, _ := mandateTestPolicy(context.Background(), store.BotMandateRef{})
	tr := store.TriggerSpec{Cron: "* * * * *", Timezone: "UTC"}
	if err := boundedSchedule(tr, time.Now(), p.Limits); err == nil {
		t.Fatal("daily cap")
	}
	tr.Cron = "0 9 * * *"
	if err := boundedSchedule(tr, time.Now(), p.Limits); err != nil {
		t.Fatal(err)
	}
}
