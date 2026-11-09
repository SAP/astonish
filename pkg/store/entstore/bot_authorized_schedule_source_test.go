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

func TestAuthorizedScheduleDirectSourceAndTitleBoundary(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			m, err := s.ReadBotMandate(owner, q.MandateRef)
			if err != nil || m.Provenance.InitiationKind != store.BotInitiationKindDirectOwnerMessage || m.ScheduleCommand != nil {
				t.Fatalf("ordinary source: %+v %v", m, err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "done"}}); err != nil {
				t.Fatal(err)
			}
			// The marker is private to title admission, never supplied in JSON.
			titleCtx := context.WithValue(owner, titleMandateAdmissionKey{}, true)
			admitted, err := r.AdmitMessage(titleCtx, store.BotMessageRequest{BotID: q.MandateRef.BotID, Version: 1, Instructions: "Every day at nine read the report", IdempotencyKey: "title-boundary"})
			if err != nil {
				t.Fatal(err)
			}
			ref := *admitted.Run.MandateRef
			m, err = s.ReadBotMandate(owner, ref)
			if err != nil || m.Provenance.InitiationKind != store.BotInitiationKindTitle {
				t.Fatalf("title provenance: %+v %v", m, err)
			}
			if _, _, _, _, err = s.source(owner, r.db, ref, q.Payload); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("title became recurrence authority: %v", err)
			}
		})
	}
}

func TestAuthorizedScheduleFiniteQuotaAndScope(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			for _, mutate := range []func(*store.BotMandateRef){func(ref *store.BotMandateRef) { ref.OrgSlug = "other" }, func(ref *store.BotMandateRef) { ref.TeamSlug = "other" }, func(ref *store.BotMandateRef) { ref.OwnerID = uuid.NewString() }, func(ref *store.BotMandateRef) { ref.BotID = uuid.New() }} {
				ref := q.MandateRef
				mutate(&ref)
				if _, err := s.ListAuthorizedOperations(owner, ref); err == nil {
					t.Fatal("cross-scope list allowed")
				}
			}
			bad := q
			bad.MandateRef.BotID = uuid.New()
			if _, err := s.EnqueueAuthorizedOperation(worker, bad); err == nil {
				t.Fatal("cross-bot capture allowed")
			}
			p, _ := authorizedSchedulePolicy(owner, q.MandateRef)
			limits := p.Limits
			limits.MaxDailyRuns = 2000
			limits.MinScheduleIntervalSeconds = 120
			if err := boundedSchedule(store.TriggerSpec{Cron: "* * * * *", Timezone: "UTC"}, time.Now(), limits); err == nil {
				t.Fatal("minimum interval ignored")
			}
			limits.MaxSchedules = 1
			rec, err := r.db.GetBot(owner, q.MandateRef.BotID)
			if err != nil {
				t.Fatal(err)
			}
			op := store.BotOperation{ID: uuid.New(), Payload: q.Payload}
			candidate, _, err := s.scheduleCandidate(owner, r.db, rec, op, limits)
			if err != nil {
				t.Fatal(err)
			}
			// Persist a real first schedule, then inspect the next bounded candidate.
			queued, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "queued"}}); err != nil {
				t.Fatal(err)
			}
			if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, queued.ID); err != nil {
				t.Fatal(err)
			}
			rec, err = r.db.GetBot(owner, q.MandateRef.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if candidate.Version != rec.Bot.Version {
				t.Fatal("candidate version mismatch")
			}
			op.ID = uuid.New()
			if _, _, err = s.scheduleCandidate(owner, r.db, rec, op, limits); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("count quota ignored: %v", err)
			}
			// Other operation purposes cannot be substituted for a source task mandate.
			if _, err = s.VerifyBotMandate(owner, q.MandateRef, execution.BotMandatePurposeSchedule); err == nil {
				t.Fatal("purpose widened")
			}
		})
	}
}
