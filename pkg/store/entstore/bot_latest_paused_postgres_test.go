//go:build integration

package entstore

import (
	"context"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestIntegrationPostgresBotLatestPausedScheduleAuthority(t *testing.T) {
	requireDraftActivationPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, allowed := range []bool{true, false} {
			name := "denied"
			if allowed {
				name = "allowed"
			}
			t.Run(name, func(t *testing.T) {
				f := newPostgresBotServicesFixture(t, scope, 100, 100)
				if err := f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: f.lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, f.worker, f.lease, "fixture complete")}); err != nil {
					t.Fatal(err)
				}
				currentAllows := allowed
				policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
					p, e := mandateTestPolicy(ctx, ref)
					resources := []string{"ask_user", "propose_bot_control", RoutineControlResource(store.BotControlResumeTasks), "schedule_job", "update_scheduled_job", "remove_scheduled_job", "shell_command", "http_request", "list_scheduled_jobs_future"}
					if currentAllows {
						resources = append(resources, "list_scheduled_jobs")
					}
					p.Constraints = []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: resources}}
					return p, e
				}
				f.owner = store.WithBotMandateAdmission(mandateOwnerContext(t, f.owner), policy)
				b, err := f.repo.Get(f.owner, f.run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				b, err = f.repo.Pause(f.owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "pause"})
				if err != nil {
					t.Fatal(err)
				}
				registry := execution.NewBotTurnAuthorizationRegistry(10)
				defer registry.Close()
				f.owner = store.WithBotMessageAdmission(f.owner, registry)
				a, err := f.repo.AdmitMessage(f.owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: "Which schedules are paused?", IdempotencyKey: "inspect"})
				if err != nil {
					t.Fatal(err)
				}
				if a.Run.MandateRef == nil || a.Run.Intent != store.BotIntentConversation {
					t.Fatalf("not paused conversation: %+v", a.Run)
				}
				ref := *a.Run.MandateRef
				f.reopenReviewed(t)
				runtime, err := NewBotMandateRuntime(f.bots, policy)
				if err != nil {
					t.Fatal(err)
				}
				m, err := runtime.VerifyBotMandate(f.owner, ref, execution.BotMandatePurposeOperation)
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"ask_user", "propose_bot_control", RoutineControlResource(store.BotControlResumeTasks)}
				if allowed {
					want = append(want, "list_scheduled_jobs")
				}
				if !reflect.DeepEqual(m.Constraints, []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: want}}) {
					t.Fatalf("paused permissions: %+v", m.Constraints)
				}
				p, _ := policy(f.owner, ref)
				if m.Limits != p.Limits {
					t.Fatal("finite limits changed")
				}
				if _, err = runtime.VerifyBotMandate(f.owner, ref, execution.BotMandatePurposeTask); err == nil {
					t.Fatal("task authority granted")
				}
				foreign := ref
				foreign.BotID = uuid.New()
				if _, err = runtime.VerifyBotMandate(f.owner, foreign, execution.BotMandatePurposeOperation); err == nil {
					t.Fatal("foreign ref accepted")
				}
				if _, err = f.repo.RequestRun(f.owner, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "background", IdempotencyKey: "background"}); err == nil {
					t.Fatal("autonomous work reopened")
				}
				if allowed {
					currentAllows = false
					if _, err = runtime.VerifyBotMandate(f.owner, ref, execution.BotMandatePurposeOperation); err == nil {
						t.Fatal("policy removal ignored")
					}
					currentAllows = true
				}
				if err = runtime.RevokeBotMandate(f.owner, ref); err != nil {
					t.Fatal(err)
				}
				if _, err = runtime.VerifyBotMandate(f.owner, ref, execution.BotMandatePurposeOperation); err == nil {
					t.Fatal("revocation ignored")
				}
			})
		}
	})
}
