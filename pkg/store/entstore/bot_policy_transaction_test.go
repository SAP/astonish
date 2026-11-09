package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// The authorizer runs after policy preflight, before the commit transaction.
// Tightening real persisted policy there must reject, not reuse the old result.
func TestBotPolicyTransactionRevalidatesAfterPreflight(t *testing.T) {
	r, s, _, worker, q := authorizedScheduleFixture(t, store.BotScopeTeam)
	ctx, cancel := context.WithTimeout(worker, 3*time.Second)
	defer cancel()
	networks := &teamNetworkPolicyStore{client: r.db.(teamBotDB).client}
	rule := &store.NetworkPolicyRule{Host: "policy.test", Action: store.NetworkPolicyAllow}
	if err := networks.Save(ctx, rule); err != nil {
		t.Fatal(err)
	}
	s.policy = func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		rules, err := networks.List(ctx)
		if err != nil {
			return store.BotMandatePolicy{}, err
		}
		if len(rules) != 1 || rules[0].Action != store.NetworkPolicyAllow {
			return store.BotMandatePolicy{}, store.ErrBotForbidden
		}
		return authorizedSchedulePolicy(ctx, ref)
	}
	s.authorize = func(ctx context.Context, _ store.BotScheduleAuthorization) error {
		rule.Action = store.NetworkPolicyDeny
		return networks.Save(ctx, rule)
	}
	before, err := r.db.GetBot(ctx, q.MandateRef.BotID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnqueueAuthorizedOperation(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("stale preflight accepted: %v", err)
	}
	after, err := r.db.GetBot(ctx, q.MandateRef.BotID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastSequence != before.LastSequence || after.Bot.Revision != before.Bot.Revision {
		t.Fatal("rejected operation wrote state")
	}
	ops, err := s.ListAuthorizedOperations(ctx, q.MandateRef)
	if err != nil || len(ops) != 0 {
		t.Fatalf("operations survived rollback: %+v %v", ops, err)
	}
	if jobs := r.db.(botTriggerDB).triggerScheduler().List(ctx); len(jobs) != 0 {
		t.Fatal("rejected operation created schedule")
	}
}

func TestBotPolicyTransactionAdmission(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, _, base, initial, _ := runtimeFixture(t, scope)
			ctx, cancel := context.WithTimeout(mandateOwnerContext(t, base), 3*time.Second)
			defer cancel()
			policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				if d, ok := r.db.(teamBotDB); ok {
					if _, err := (&teamNetworkPolicyStore{client: d.client}).List(ctx); err != nil {
						return store.BotMandatePolicy{}, err
					}
				}
				return authorizedSchedulePolicy(ctx, ref)
			}
			ctx = store.WithBotMandateAdmission(ctx, policy)
			if _, err := r.AdmitMessage(ctx, store.BotMessageRequest{BotID: initial.BotID, Version: 1, Instructions: "Hello", IdempotencyKey: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
			b, err := r.Get(ctx, initial.BotID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := r.db.GetBot(ctx, initial.BotID)
			if err != nil {
				t.Fatal(err)
			}
			rejectCtx := store.WithBotMandateAdmission(ctx, func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				if _, e := policy(c, ref); e != nil {
					return store.BotMandatePolicy{}, e
				}
				return store.BotMandatePolicy{}, store.ErrBotForbidden
			})
			key := uuid.NewString()
			if _, err = r.AdmitMessage(rejectCtx, store.BotMessageRequest{BotID: initial.BotID, Version: 1, Instructions: "Reject", IdempotencyKey: key}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("reject: %v", err)
			}
			after, err := r.db.GetBot(ctx, initial.BotID)
			if err != nil || after.LastSequence != before.LastSequence || after.Bot.Revision != before.Bot.Revision {
				t.Fatalf("admission rollback: %v", err)
			}
			if _, err = r.db.FindRun(ctx, initial.BotID, key); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("rejected run persisted: %v", err)
			}
			v := store.DefaultBotVersion()
			v.Charter = "New title"
			rejectedKey := uuid.NewString()
			if _, err = r.CreateDraftAndStart(rejectCtx, store.BotCreateRequest{Name: "Rejected title", OwnerID: b.OwnerID, Version: v, IdempotencyKey: rejectedKey}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("title reject: %v", err)
			}
			if _, err = r.db.FindCreate(ctx, rejectedKey); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("rejected title persisted: %v", err)
			}
			if _, err = r.CreateDraftAndStart(ctx, store.BotCreateRequest{Name: "New title", OwnerID: b.OwnerID, Version: v, IdempotencyKey: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
