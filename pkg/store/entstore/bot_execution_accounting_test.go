package entstore

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotMandateZeroExecutionCapsKeepUsageAndSpendAuthority(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, bots, base, legacy, _ := runtimeFixture(t, scope)
			policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, err := mandateTestPolicy(c, ref)
				p.Limits.MaxRuntimeSeconds, p.Limits.MaxToolCalls, p.Limits.MaxTokens, p.Limits.MaxTotalToolCalls, p.Limits.MaxTotalTokens = 0, 0, 0, 0, 0
				return p, err
			}
			ctx := store.WithBotMandateAdmission(mandateOwnerContext(t, base), policy)
			admitted, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: legacy.BotID, Version: 1, Instructions: "read", IdempotencyKey: "zero-execution-caps"})
			if err != nil {
				t.Fatal(err)
			}
			ref := *admitted.Run.MandateRef
			rt, err := NewBotMandateRuntime(bots, policy)
			if err != nil {
				t.Fatal(err)
			}
			if err = rt.ReserveBotMandateRun(ctx, ref, admitted.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err = rt.ConsumeBotMandateQuota(ctx, ref, admitted.Run.ID, 150, 2000000, 0); err != nil {
				t.Fatal(err)
			}
			modelID := uuid.New()
			if err = rt.ReserveBotMandateModel(ctx, ref, admitted.Run.ID, modelID, 100); err != nil {
				t.Fatal(err)
			}
			if err = rt.SettleBotMandateModel(ctx, ref, admitted.Run.ID, modelID, 300); err != nil {
				t.Fatal(err)
			}
			if err = rt.SettleBotMandateModel(ctx, ref, admitted.Run.ID, modelID, 300); err != nil {
				t.Fatal("settlement idempotency", err)
			}
			if err = rt.ConsumeBotMandateQuota(ctx, ref, admitted.Run.ID, 0, 0, 1); err == nil {
				t.Fatal("external spend bypass")
			}
			if err = rt.ConsumeBotMandateQuota(ctx, ref, admitted.Run.ID, math.MaxInt64, 0, 0); err == nil {
				t.Fatal("tool accounting overflow")
			}
			overflowModel := uuid.New()
			if err = rt.ReserveBotMandateModel(ctx, ref, admitted.Run.ID, overflowModel, 1); err != nil {
				t.Fatal(err)
			}
			if err = rt.SettleBotMandateModel(ctx, ref, admitted.Run.ID, overflowModel, math.MaxInt64); err == nil {
				t.Fatal("settlement overflow")
			}
			if err = rt.SettleBotMandateModel(ctx, ref, admitted.Run.ID, overflowModel, 0); err != nil {
				t.Fatal(err)
			}
			record, err := repo.db.GetBot(ctx, ref.BotID)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(record.Metadata[mandateUsageKey])
			var usage mandateUsage
			if err = json.Unmarshal(raw, &usage); err != nil {
				t.Fatal(err)
			}
			current := usage.Active[admitted.Run.ID.String()]
			if usage.Tools != 150 || current.Tools != 150 || usage.Tokens != 2000300 || current.Tokens != 2000300 || usage.DailyRuns != 1 {
				t.Fatalf("usage lost: %+v %+v", usage, current)
			}
		})
	}
}
