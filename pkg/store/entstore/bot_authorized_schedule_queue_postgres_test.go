//go:build integration

package entstore

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestIntegrationPostgresBotQueuedScheduleReconciliation(t *testing.T) {
	requireDraftActivationPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, tc := range []struct {
			operation  store.BotControlOperation
			interleave bool
			disabled   bool
		}{
			{store.BotControlScheduleEdit, false, false},
			{store.BotControlSchedulePause, false, false},
			{store.BotControlScheduleRemove, false, false},
			{store.BotControlScheduleCreate, false, false},
			{store.BotControlScheduleCreate, false, true},
			{store.BotControlScheduleCreate, true, false},
		} {
			operation := tc.operation
			name := string(operation)
			if tc.disabled {
				name += "/disabled-unrelated"
			}
			if tc.interleave {
				name += "/claim-interleaving"
			}
			t.Run(name, func(t *testing.T) {
				f := newPostgresBotServicesFixture(t, scope, 100, 100)
				if err := f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: f.lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, f.worker, f.lease, "fixture complete")}); err != nil {
					t.Fatal(err)
				}
				owner := store.WithBotMandateAdmission(mandateOwnerContext(t, f.owner), authorizedSchedulePolicy)
				b, err := f.repo.Get(owner, f.run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				a, err := f.repo.AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: "Every day at nine, read the report. Keep other schedules unchanged.", IdempotencyKey: uuid.NewString()})
				if err != nil {
					t.Fatal(err)
				}
				ref := *a.Run.MandateRef
				worker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: a.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
				lease, err := f.repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: a.Run.ID, WorkerID: "pg-queue", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				runtime, err := NewBotAuthorizedOperationRuntime(f.bots, authorizedSchedulePolicy, func(context.Context, store.BotScheduleAuthorization) error { return nil }, func(context.Context, store.Bot, store.BotVersion) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				s := runtime.(*botAuthorizedOperationRuntime)
				op, err := s.EnqueueAuthorizedOperation(worker, store.BotAuthorizedOperationRequest{Lease: lease, MandateRef: ref, IdempotencyKey: "pg-queue", Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "daily report", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "read the report", Enabled: true}}}})
				if err != nil {
					t.Fatal(err)
				}
				if err = f.repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, worker, lease, "captured")}); err != nil {
					t.Fatal(err)
				}
				if _, err = s.ApplyAuthorizedOperation(context.Background(), ref, op.ID); err != nil {
					t.Fatal(err)
				}
				if tc.interleave {
					exerciseQueuedScheduleReconciliation(t, f.repo, s, owner, b.ID, operation, false, func(apply func() error) error {
						return postgresQueuedClaimInterleaving(t, f.repo, owner, b.ID, apply)
					})
				} else {
					exerciseQueuedScheduleReconciliation(t, f.repo, s, owner, b.ID, operation, tc.disabled)
				}
			})
		}
	})
}
