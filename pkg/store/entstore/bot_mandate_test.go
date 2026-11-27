package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

// Include the ordinary chat/question capabilities so title admissions can issue
// their question-only mandate without weakening the finite quota policy.
func mandateTestPolicy(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
	return store.BotMandatePolicy{TTL: time.Hour, Scopes: []string{string(execution.CapabilityChat), string(execution.CapabilityToolExecute)}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"read_file", "ask_user"}}, {Action: execution.CapabilityChat, Resources: []string{"chat"}}}, Limits: store.BotMandateLimits{MinScheduleIntervalSeconds: 60, MaxSchedules: 1, MaxDailyRuns: 2, MaxConcurrentRuns: 1, MaxRuntimeSeconds: 60, MaxToolCalls: 2, MaxTokens: 10, MaxTotalToolCalls: 3, MaxTotalTokens: 15}}, nil
}
func TestBotMandateAdmissionDurable(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, bots, ctx, _, _ := runtimeFixture(t, scope)
			p, _ := execution.PrincipalFromContext(ctx)
			ctx, err := execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(2))
			ctx = store.WithBotMandateAdmission(ctx, mandateTestPolicy)
			v := store.DefaultBotVersion()
			v.Charter = "Discuss"
			q := store.BotCreateRequest{Name: "Research", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "mandate-title"}
			first, err := repo.CreateAndStart(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if first.Run.MandateRef == nil {
				t.Fatal("missing pin")
			}
			ref := *first.Run.MandateRef
			runtime, err := NewBotMandateRuntime(bots, mandateTestPolicy)
			if err != nil {
				t.Fatal(err)
			}
			m, err := runtime.VerifyBotMandate(context.Background(), ref, execution.BotMandatePurposeTask)
			if err != nil {
				raw, _ := runtime.ReadBotMandate(context.Background(), ref)
				saved, _ := repo.db.ReadRun(ctx, ref.BotID, first.Run.ID)
				t.Fatalf("verify %v record=%+v run=%+v", err, raw, saved)
			}
			if m.Purpose != execution.BotMandatePurposeTask {
				t.Fatal("recurrence authority")
			}
			replay, err := repo.CreateAndStart(ctx, q)
			if err != nil || replay.Run.MandateRef == nil || *replay.Run.MandateRef != ref {
				t.Fatalf("replay: %v", err)
			}
			if err := runtime.CreateBotMandate(ctx, store.BotMandateRecord{Mandate: m}); err == nil {
				t.Fatal("arbitrary record accepted")
			}
			if err := runtime.ReserveBotMandateRun(context.Background(), ref, first.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err := runtime.ConsumeBotMandateQuota(context.Background(), ref, first.Run.ID, 2, 10, 0); err != nil {
				t.Fatal(err)
			}
			if err := runtime.ConsumeBotMandateQuota(context.Background(), ref, first.Run.ID, 100, 1000, 0); err != nil {
				t.Fatal("historical per-run/aggregate metadata enforced:", err)
			}
			if err := runtime.ConsumeBotMandateQuota(context.Background(), ref, first.Run.ID, 0, 0, 1); err == nil {
				t.Fatal("external spend bypass")
			}
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			if err := runtime.ConsumeBotMandateQuota(cancelled, ref, first.Run.ID, 1, 1, 0); err == nil {
				t.Fatal("cancellation bypass")
			}
			modelID := uuid.New()
			if err := runtime.ReserveBotMandateModel(context.Background(), ref, first.Run.ID, modelID, 1000); err != nil {
				t.Fatal(err)
			}
			if err := runtime.SettleBotMandateModel(context.Background(), ref, first.Run.ID, modelID, 2000); err != nil {
				t.Fatal(err)
			}
			if err := runtime.RevokeBotMandate(ctx, ref); err != nil {
				t.Fatal(err)
			}
			if err := runtime.ConsumeBotMandateQuota(context.Background(), ref, first.Run.ID, 1, 1, 0); err == nil {
				t.Fatal("revoked consumption accepted")
			}
			if err := runtime.ReleaseBotMandateRun(context.Background(), ref, first.Run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.VerifyBotMandate(context.Background(), ref, execution.BotMandatePurposeTask); err == nil {
				t.Fatal("revoked accepted")
			}
		})
	}
}
