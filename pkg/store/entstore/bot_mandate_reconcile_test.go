package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func TestMandateWeatherReservationReconciliation(t *testing.T) {
	repo, bots, ctx, _, _ := runtimeFixture(t, store.BotScopePersonal)
	p, _ := execution.PrincipalFromContext(ctx)
	ctx, err := execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	policy := func(c context.Context, r store.BotMandateRef) (store.BotMandatePolicy, error) {
		v, e := mandateTestPolicy(c, r)
		v.Limits.MaxTokens = 100000
		v.Limits.MaxTotalTokens = 150000
		return v, e
	}
	ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(2))
	ctx = store.WithBotMandateAdmission(ctx, policy)
	v := store.DefaultBotVersion()
	v.Charter = "Weather"
	first, err := repo.CreateDraftAndStart(ctx, store.BotCreateRequest{Name: "Weather", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "weather-budget"})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewBotMandateRuntime(bots, policy)
	if err != nil {
		t.Fatal(err)
	}
	ref := *first.Run.MandateRef
	run := first.Run.ID
	if err = runtime.ReserveBotMandateRun(ctx, ref, run); err != nil {
		t.Fatal(err)
	}
	// Recorded first two reservations and complete provider usage. Repeated prompts
	// remain billable; only unused reservation capacity may be returned.
	for _, call := range []struct{ hold, actual int64 }{{33308, 9986 + 111}, {38278, 12140 + 98}} {
		id := uuid.New()
		if err = runtime.ReserveBotMandateModel(ctx, ref, run, id, call.hold); err != nil {
			t.Fatal(err)
		}
		if err = runtime.SettleBotMandateModel(ctx, ref, run, id, call.actual); err != nil {
			t.Fatal(err)
		}
		if err = runtime.SettleBotMandateModel(ctx, ref, run, id, 0); err != nil {
			t.Fatal(err)
		}
	}
	// A representative 40k continuation fails before reconciliation: 71586+40000.
	if err = runtime.ConsumeBotMandateQuota(ctx, ref, run, 0, 40000, 0); err != nil {
		t.Fatalf("weather continuation denied by stale reservations: %v", err)
	}
	var quota *store.BotMandateQuotaError
	err = runtime.ConsumeBotMandateQuota(ctx, ref, run, 0, 40000, 0)
	if !errors.As(err, &quota) || quota.Dimension != "run_tokens" || quota.Used != 62335 || quota.Limit != 100000 {
		t.Fatalf("exact finite gate: %+v %v", quota, err)
	}
	id := uuid.New()
	if err = runtime.ReserveBotMandateModel(ctx, ref, run, id, 10000); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := runtime.SettleBotMandateModel(ctx, ref, run, id, 1000); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	// Must remain 63335 after all duplicate settlements, never negative/refunded twice.
	err = runtime.ConsumeBotMandateQuota(ctx, ref, run, 0, 40000, 0)
	if !errors.As(err, &quota) || quota.Used != 63335 {
		t.Fatalf("duplicate settlement: %+v %v", quota, err)
	}
	if err = runtime.SettleBotMandateModel(ctx, ref, run, uuid.New(), 0); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatalf("unknown refund: %v", err)
	}
	// An unexpectedly over-limit actual response must be retained, not rolled back.
	over := uuid.New()
	if err = runtime.ReserveBotMandateModel(ctx, ref, run, over, 1); err != nil {
		t.Fatal(err)
	}
	if err = runtime.SettleBotMandateModel(ctx, ref, run, over, 100000); err != nil {
		t.Fatal(err)
	}
	if err = runtime.ConsumeBotMandateQuota(ctx, ref, run, 0, 1, 0); !errors.As(err, &quota) || quota.Used != 163335 {
		t.Fatalf("overage discarded: %v", err)
	}
}
