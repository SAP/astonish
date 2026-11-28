package entstore

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotMandatePreflightFreshAuthority(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, bots, base, run, _ := runtimeFixture(t, scope)
			owner := mandateOwnerContext(t, base)
			receipt, err := repo.AdmitMessage(owner, store.BotMessageRequest{BotID: run.BotID, Version: 1, Instructions: "read", IdempotencyKey: "preflight"})
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := NewBotMandateRuntime(bots, mandateTestPolicy)
			if err != nil {
				t.Fatal(err)
			}
			rt := runtime.(*botMandateRuntime)
			checks := 0
			check := rt.repo.auth.check
			rt.repo.auth.check = func(ctx context.Context, user string) (string, error) { checks++; return check(ctx, user) }
			ref := *receipt.Run.MandateRef
			ctx, end := rt.BeginModelPreflight(context.Background())
			defer end()
			for range 2 {
				if _, err := rt.VerifyBotMandate(ctx, ref, execution.BotMandatePurposeTask); err != nil {
					t.Fatal(err)
				}
			}
			if checks != 2 {
				t.Fatalf("membership checks=%d", checks)
			}
			if err := rt.RevokeBotMandate(owner, ref); err != nil {
				t.Fatal(err)
			}
			if _, err := rt.VerifyBotMandate(ctx, ref, execution.BotMandatePurposeTask); err == nil {
				t.Fatal("cached revocation within operation")
			}
			end()
			if WithBotContentReadOperationFromContext(context.Background(), ctx) != context.Background() {
				t.Fatal("operation survived close")
			}
			if _, err := rt.VerifyBotMandate(context.Background(), ref, execution.BotMandatePurposeTask); err == nil {
				t.Fatal("cached authorization on later operation")
			}
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := rt.VerifyBotMandate(canceled, ref, execution.BotMandatePurposeTask); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("canceled authority: %v", err)
			}
		})
	}
}
