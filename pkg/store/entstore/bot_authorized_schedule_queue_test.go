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

func TestBotAuthorizedScheduleQueuedReconciliation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, tc := range []struct {
			operation store.BotControlOperation
			disabled  bool
		}{
			{store.BotControlScheduleEdit, false},
			{store.BotControlSchedulePause, false},
			{store.BotControlScheduleRemove, false},
			{store.BotControlScheduleCreate, false},
			{store.BotControlScheduleCreate, true},
		} {
			operation := tc.operation
			name := string(scope) + "/" + string(operation)
			if tc.disabled {
				name += "/disabled-unrelated"
			}
			t.Run(name, func(t *testing.T) {
				r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
				pending, err := s.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "captured")}); err != nil {
					t.Fatal(err)
				}
				if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, pending.ID); err != nil {
					t.Fatal(err)
				}
				exerciseQueuedScheduleReconciliation(t, r, s, owner, q.MandateRef.BotID, operation, tc.disabled)
			})
		}
	}
}

// Shared by SQLite and the guarded routed PostgreSQL fixture.
func exerciseQueuedScheduleReconciliation(t *testing.T, r *botRepository, s *botAuthorizedOperationRuntime, owner context.Context, id uuid.UUID, operation store.BotControlOperation, disabled bool, applyHooks ...func(func() error) error) {
	t.Helper()
	b, err := r.Get(owner, id)
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.GetVersion(owner, id, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	tr := v.Triggers[0]
	jobs := r.db.(botTriggerDB).triggerScheduler()
	job := jobs.Get(owner, tr.ScheduleID.String())
	due := time.Now().UTC().Add(-time.Second)
	job.NextRun = &due
	if err = jobs.Update(owner, job); err != nil {
		t.Fatal(err)
	}
	// Read back the driver's timestamp before constructing the exact occurrence.
	due = *jobs.Get(owner, job.ID).NextRun
	occurrence := draftActivationOccurrence(b, tr, due)
	admitted, err := r.AdmitScheduledRun(owner, occurrence)
	if err != nil {
		t.Fatal(err)
	}
	original, err := r.db.ReadRun(owner, id, admitted.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if original.Run.MandateRef == nil || original.Run.Status != store.RunQueued {
		t.Fatal("missing pinned queued task")
	}
	if disabled {
		job.Enabled = false
		if err = jobs.Update(owner, job); err != nil {
			t.Fatal(err)
		}
	}
	jobBefore := jobs.Get(owner, job.ID)
	if operation == store.BotControlScheduleCreate {
		tr = store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 10 * * *", Timezone: "UTC", Instructions: "second task", Enabled: true}
	} else if operation == store.BotControlScheduleEdit {
		tr.Cron = "0 11 * * *"
	}
	applyQueuedScheduleWithBarriers(t, r, s, owner, id, operation, tr, applyHooks...)
	saved, err := r.db.ReadRun(owner, id, admitted.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := r.Get(owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Run.ID != original.Run.ID || saved.RequestHash != original.RequestHash || saved.Instructions != original.Instructions || saved.Run.SessionID != original.Run.SessionID || saved.Run.IdempotencyKey != original.Run.IdempotencyKey || !saved.OccurrenceAt.Equal(original.OccurrenceAt) || !reflect.DeepEqual(saved.Run.MandateRef, original.Run.MandateRef) || !reflect.DeepEqual(saved.Run.CallerCeiling, original.Run.CallerCeiling) {
		t.Fatal("reconciliation changed occurrence identity or authority")
	}
	if operation == store.BotControlScheduleCreate {
		jobAfter := jobs.Get(owner, job.ID)
		jobBefore.Payload.BotVersion = current.Version
		if !reflect.DeepEqual(jobBefore, jobAfter) {
			t.Fatal("unrelated schedule runtime state changed")
		}
	}
	if operation == store.BotControlScheduleCreate && !disabled {
		if saved.Run.Status != store.RunQueued || saved.Run.Version != current.Version {
			t.Fatalf("unchanged occurrence not rebound: %+v", saved.Run)
		}
		if _, err = r.LoadRunInput(owner, id, saved.Run.ID); err != nil {
			t.Fatal(err)
		}
		// Rebinding is not a new grant: revocation/current policy still gates execution.
		pin := *saved.Run.MandateRef
		if _, err = s.VerifyBotMandate(owner, pin, execution.BotMandatePurposeTask); err != nil {
			t.Fatal(err)
		}
		policy := s.policy
		s.policy = func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
			return store.BotMandatePolicy{}, store.ErrBotForbidden
		}
		_, policyErr := s.VerifyBotMandate(owner, pin, execution.BotMandatePurposeTask)
		s.policy = policy
		if policyErr == nil {
			t.Fatal("rebound authority bypassed current policy")
		}
		principal, ok := execution.PrincipalFromContext(owner)
		if !ok {
			t.Fatal("missing authenticated owner principal")
		}
		identity := execution.BotRunIdentity{BotID: id, RunID: saved.Run.ID, Version: current.Version, ActorID: principal.Subject, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug}
		worker := execution.WithBotRunIdentity(owner, identity)
		if err = r.authorizeWorker(worker, id, saved.Run.ID, current.Version, b.OwnerID); err != nil {
			t.Fatalf("current-version worker routing: %v", err)
		}
		staleIdentity := identity
		staleIdentity.Version = original.Run.Version // Only the version differs.
		oldWorker := execution.WithBotRunIdentity(owner, staleIdentity)
		claim := store.BotClaimRequest{BotID: id, RunID: saved.Run.ID, WorkerID: "rebound", ExpiresAt: r.clock().Add(time.Minute)}
		if _, err = r.ClaimRun(oldWorker, claim); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("stale worker identity rejection: %v", err)
		}
		afterStale, err := r.db.ReadRun(owner, id, saved.Run.ID)
		if err != nil || !reflect.DeepEqual(saved, afterStale) {
			t.Fatalf("stale worker mutated rebound run: %v", err)
		}
		lease, e := r.ClaimRun(worker, claim)
		if e != nil {
			t.Fatal(e)
		}
		if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, lease, "same occurrence executed once")}); err != nil {
			t.Fatal(err)
		}
		if err = s.RevokeBotMandate(owner, pin); err != nil {
			t.Fatal(err)
		}
		if _, err = s.VerifyBotMandate(owner, pin, execution.BotMandatePurposeTask); err == nil {
			t.Fatal("revoked rebound authority accepted")
		}
	} else {
		if saved.Run.Status != store.RunCancelled || saved.Run.FinishedAt == nil || saved.Run.Version != original.Run.Version {
			t.Fatalf("obsolete occurrence stranded: %+v", saved.Run)
		}
		if _, err = r.LoadRunInput(owner, id, saved.Run.ID); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("cancelled input executable: %v", err)
		}
	}
	// Neither the old scheduler replay nor a current-version duplicate can create
	// another run for the admitted occurrence; reconciliation never redispatches.
	_, _ = r.AdmitScheduledRun(owner, occurrence)
	rebound := draftActivationOccurrence(current, v.Triggers[0], due)
	_, _ = r.AdmitScheduledRun(owner, rebound)
	runs, err := r.ListRuns(owner, id, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, run := range runs {
		if run.TriggerID == original.Run.TriggerID && run.Trigger == store.TriggerCron {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate occurrence created %d runs", count)
	}
}
