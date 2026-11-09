package entstore

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotActivationScopedReadiness(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, _, run, _, _ := botRunFixture(t, scope)
			owner, err := r.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			v := store.DefaultBotVersion()
			v.Charter = "Review explicit activation"
			b, err := r.CreateDraft(ctx, store.BotCreateRequest{Name: "Activation", OwnerID: owner.OwnerID, Version: v, IdempotencyKey: "activation-scoped"})
			if err != nil {
				t.Fatal(err)
			}
			backend := &Store{}
			r.activationCheck = nil
			r.auth.readiness = backend.checkBotActivation
			q := store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Reviewed"}
			if _, err := r.Activate(ctx, q); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("missing readiness: %v", err)
			}
			calls := 0
			backend.SetBotActivationReadiness(func(_ context.Context, got store.Bot, version store.BotVersion) error {
				calls++
				if got.ID != b.ID || got.Scope != scope || version.BotID != b.ID || version.Version != b.Version {
					t.Fatal("wrong scoped snapshot")
				}
				return nil
			})
			if _, err := r.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, IdempotencyKey: "draft", Instructions: "stay draft"}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("draft admission: %v", err)
			}
			if calls != 0 {
				t.Fatal("draft request invoked readiness")
			}
			active, err := r.Activate(ctx, q)
			if err != nil || active.Status != store.BotActive || active.Revision != b.Revision+1 || calls != 1 {
				t.Fatalf("activation=%+v calls=%d err=%v", active, calls, err)
			}
			backend.SetBotActivationReadiness(nil)
			if err := backend.checkBotActivation(ctx, *active, v); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("removal: %v", err)
			}
		})
	}
}

func TestBotActivationReadinessCAS(t *testing.T) {
	r, ctx, _, run, _, _ := botRunFixture(t, store.BotScopeTeam)
	owner, err := r.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	v := store.DefaultBotVersion()
	v.Charter = "Review"
	b, err := r.CreateDraft(ctx, store.BotCreateRequest{Name: "CAS", OwnerID: owner.OwnerID, Version: v, IdempotencyKey: "activation-cas"})
	if err != nil {
		t.Fatal(err)
	}
	r.activationCheck = nil
	r.auth.readiness = func(c context.Context, snapshot store.Bot, version store.BotVersion) error {
		version.Charter = "Changed during readiness"
		version.ChangeReason = "Concurrent edit"
		_, err := r.ApplyVersion(c, store.BotVersionRequest{BotID: snapshot.ID, ExpectedRevision: snapshot.Revision, Version: version})
		return err
	}
	if _, err := r.Activate(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Review"}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale readiness accepted: %v", err)
	}
	got, err := r.Get(ctx, b.ID)
	if err != nil || got.Status != store.BotDraft {
		t.Fatalf("draft mutated: %+v %v", got, err)
	}
}
