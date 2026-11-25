package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

type heartbeatScopeKey struct{}
type heartbeatScope struct{ closed bool }
type heartbeatStore struct {
	store.BotStore
	renew func(context.Context, store.BotLease, time.Time) (store.BotLease, error)
}

func (s heartbeatStore) RenewLease(ctx context.Context, l store.BotLease, expires time.Time) (store.BotLease, error) {
	return s.renew(ctx, l, expires)
}

type heartbeatMandateRuntime struct {
	store.BotMandateRuntime
	verify func(context.Context) (execution.BotMandate, error)
}

func (r heartbeatMandateRuntime) VerifyBotMandate(ctx context.Context, _ execution.BotMandateRef, _ execution.BotMandatePurpose) (execution.BotMandate, error) {
	return r.verify(ctx)
}

func TestSupervisorHeartbeatReadLifetimeAndRevocation(t *testing.T) {
	for _, failAt := range []int{0, 1, 2, 3} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			m, original := mandateTestExecution(t)
			budget, err := NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			prepared := RunPreparation{Mandate: m, Budget: &MandateBudget{SharedRunBudget: budget, Mandate: m}}
			lease := store.BotLease{ExpiresAt: time.Now().Add(time.Minute)}
			checks, renews, opens, closes := 0, 0, 0, 0
			var retained context.Context
			var previous *heartbeatScope
			checkScope := func(ctx context.Context) {
				t.Helper()
				scope, ok := ctx.Value(heartbeatScopeKey{}).(*heartbeatScope)
				if !ok || scope.closed {
					t.Fatal("heartbeat scope absent or closed during checks")
				}
				deadline, ok := ctx.Deadline()
				if !ok || !deadline.Equal(lease.ExpiresAt) {
					t.Fatal("lease deadline changed")
				}
				retained = ctx
			}
			m.Runtime = heartbeatMandateRuntime{verify: func(ctx context.Context) (execution.BotMandate, error) {
				checkScope(ctx)
				checks++
				if checks == failAt {
					return execution.BotMandate{}, store.ErrBotForbidden
				}
				return original.m, nil
			}}
			s := &Supervisor{config: SupervisorConfig{LeaseDuration: time.Minute,
				BeginHeartbeatReads: func(ctx context.Context) (context.Context, func()) {
					if previous != nil && !previous.closed {
						t.Fatal("previous heartbeat still open")
					}
					previous = &heartbeatScope{}
					scope := previous
					opens++
					return context.WithValue(ctx, heartbeatScopeKey{}, scope), func() { scope.closed = true; closes++ }
				},
				Store: heartbeatStore{renew: func(ctx context.Context, l store.BotLease, _ time.Time) (store.BotLease, error) {
					checkScope(ctx)
					renews++
					if failAt == 3 {
						return store.BotLease{}, store.ErrBotConflict
					}
					return l, nil
				}},
			}}
			_, err = s.renewHeartbeat(context.Background(), prepared, lease)
			if failAt == 0 && err != nil {
				t.Fatal(err)
			}
			if failAt == 1 || failAt == 2 {
				if !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("revocation lost: %v", err)
				}
			}
			if failAt == 3 && !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("lease barrier lost: %v", err)
			}
			wantChecks := 2
			if failAt == 1 {
				wantChecks = 1
			}
			wantRenews := 1
			if failAt == 1 || failAt == 2 {
				wantRenews = 0
			}
			if checks != wantChecks || renews != wantRenews || opens != 1 || closes != 1 || !previous.closed || retained.Err() == nil {
				t.Fatalf("checks=%d renews=%d opens=%d closes=%d retained=%v", checks, renews, opens, closes, retained.Err())
			}
			if failAt == 0 {
				_, err = s.renewHeartbeat(context.Background(), prepared, lease)
				if err != nil || checks != 4 || renews != 2 || opens != 2 || closes != 2 {
					t.Fatalf("next heartbeat reused authority or lifetime: %v", err)
				}
			}
		})
	}
}
