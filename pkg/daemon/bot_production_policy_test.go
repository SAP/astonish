package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type policyMandateRuntime struct {
	cleanupMandateRuntime
	revoked bool
}

func (r *policyMandateRuntime) VerifyBotMandate(ctx context.Context, ref execution.BotMandateRef, purpose execution.BotMandatePurpose) (execution.BotMandate, error) {
	if r.revoked {
		return execution.BotMandate{}, store.ErrBotForbidden
	}
	return r.cleanupMandateRuntime.VerifyBotMandate(ctx, ref, purpose)
}

func TestProductionBotPolicyOptionalMandate(t *testing.T) {
	t.Run("scheduled-without-mandate", func(t *testing.T) {
		runtime := productionBotPolicyRuntime(botLiveGuard{request: bot.RunRequest{Run: store.BotRun{Trigger: store.TriggerCron}}})
		if runtime.Mandate != nil {
			t.Fatalf("absent scheduled mandate became a nonnil interface (%T)", runtime.Mandate)
		}
		if runtime.Guard == nil || runtime.Classifier == nil {
			t.Fatal("ordinary run policy must remain installed")
		}
	})
	t.Run("real-mandate-revocation", func(t *testing.T) {
		rt := &policyMandateRuntime{cleanupMandateRuntime: cleanupMandateRuntime{ref: execution.BotMandateRef{ID: uuid.New(), BotID: uuid.New(), OwnerID: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Revision: 1}, run: uuid.New()}}
		auth, err := execution.VerifyBotMandateAuthorization(t.Context(), rt, rt.ref, execution.BotMandatePurposeTask)
		if err != nil {
			t.Fatal(err)
		}
		mandate := &bot.MandateExecution{Runtime: rt, Authorization: auth, Identity: execution.BotRunIdentity{BotID: rt.ref.BotID, RunID: rt.run, ActorID: rt.ref.OwnerID, OrgSlug: "org", TeamSlug: "team", Version: 1}}
		runtime := productionBotPolicyRuntime(botLiveGuard{request: bot.RunRequest{Mandate: mandate}})
		if runtime.Mandate != mandate {
			t.Fatal("real mandate was omitted or replaced")
		}
		if err := runtime.Mandate.Check(t.Context()); err != nil {
			t.Fatalf("valid mandate denied: %v", err)
		}
		rt.revoked = true
		if err := runtime.Mandate.Check(t.Context()); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("revoked mandate accepted: %v", err)
		}
	})
}
