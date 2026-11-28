package entstore

import (
	"context"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestPausedScheduleReadMandate(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, allowed := range []bool{true, false} {
			name := string(scope) + "/denied"
			if allowed {
				name = string(scope) + "/allowed"
			}
			t.Run(name, func(t *testing.T) {
				r, scoped, ctx, initial, _ := runtimeFixture(t, scope)
				currentAllows := allowed
				policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
					p, err := mandateTestPolicy(ctx, ref)
					resources := []string{"ask_user", "propose_bot_control", RoutineControlResource(store.BotControlResumeTasks), "schedule_job", "update_scheduled_job", "remove_scheduled_job", "shell_command", "http_request", "list_scheduled_jobs_future"}
					if currentAllows {
						resources = append(resources, "list_scheduled_jobs")
					}
					p.Constraints = []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: resources}}
					return p, err
				}
				owner := store.WithBotMandateAdmission(mandateOwnerContext(t, ctx), policy)
				b, err := r.Get(owner, initial.BotID)
				if err != nil {
					t.Fatal(err)
				}
				b, err = r.Pause(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Pause independent work"})
				if err != nil {
					t.Fatal(err)
				}
				registry := execution.NewBotTurnAuthorizationRegistry(10)
				defer registry.Close()
				owner = store.WithBotMessageAdmission(owner, registry)
				receipt, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "inspect", Instructions: "Which schedules are paused?"})
				if err != nil {
					t.Fatal(err)
				}
				if receipt.Run.MandateRef == nil || receipt.Run.Intent != store.BotIntentConversation {
					t.Fatalf("not a paused conversation: %+v", receipt.Run)
				}
				runtime, err := NewBotMandateRuntime(scoped, policy)
				if err != nil {
					t.Fatal(err)
				}
				ref := *receipt.Run.MandateRef
				m, err := runtime.VerifyBotMandate(owner, ref, execution.BotMandatePurposeOperation)
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"ask_user", "propose_bot_control", RoutineControlResource(store.BotControlResumeTasks)}
				if allowed {
					want = append(want, "list_scheduled_jobs")
				}
				if !reflect.DeepEqual(m.Constraints, []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: want}}) {
					t.Fatalf("paused mandate resources: %+v want %v", m.Constraints, want)
				}
				p, _ := policy(owner, ref)
				if m.Limits != p.Limits {
					t.Fatal("pause changed finite limits")
				}
				if _, err := runtime.VerifyBotMandate(owner, ref, execution.BotMandatePurposeTask); err == nil {
					t.Fatal("paused read became task authority")
				}
				foreign := ref
				foreign.BotID = uuid.New()
				if _, err := runtime.VerifyBotMandate(owner, foreign, execution.BotMandatePurposeOperation); err == nil {
					t.Fatal("foreign reference accepted")
				}
				if _, err := r.RequestRun(owner, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "background", IdempotencyKey: "background"}); err == nil {
					t.Fatal("autonomous admission reopened")
				}
				if allowed {
					currentAllows = false
					if _, err := runtime.VerifyBotMandate(owner, ref, execution.BotMandatePurposeOperation); err == nil {
						t.Fatal("current policy revocation ignored")
					}
					currentAllows = true
				}
				if err := runtime.RevokeBotMandate(owner, ref); err != nil {
					t.Fatal(err)
				}
				if _, err := runtime.VerifyBotMandate(owner, ref, execution.BotMandatePurposeOperation); err == nil {
					t.Fatal("revoked paused mandate accepted")
				}
			})
		}
	}
}
