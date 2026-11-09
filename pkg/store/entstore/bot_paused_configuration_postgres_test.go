//go:build integration

package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestIntegrationPostgresBotHomeBindingRegistration(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newPostgresBotServicesFixture(t, scope, 100, 100)
		active, err := f.bots.Get(f.owner, f.run.BotID)
		if err != nil {
			t.Fatal(err)
		}
		v := store.DefaultBotVersion()
		v.Charter = "Binding persistence"
		b, err := f.bots.CreateDraft(f.owner, store.BotCreateRequest{Name: "Binding", OwnerID: active.OwnerID, Version: v, IdempotencyKey: "pg-binding"})
		if err != nil {
			t.Fatal(err)
		}
		q := BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "fixture", StorageKey: "opaque-pg-home", Revision: 1}}
		internal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceCode, ClientID: "registry", OrgSlug: "restorepg", TeamSlug: "ops", Scopes: []string{BotHomeRegistrationScope}, Authenticated: true})
		if err != nil {
			t.Fatal(err)
		}
		verify := func(_ context.Context, got BotHomeRegistration) error {
			if got != q {
				t.Fatalf("attestation changed: %+v", got)
			}
			return nil
		}
		if err := RegisterBotHomeBinding(f.owner, f.bots, q, verify); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("user registered binding: %v", err)
		}
		if err := RegisterBotHomeBinding(internal, f.bots, q, verify); err != nil {
			t.Fatal(err)
		}
		if err := RegisterBotHomeBinding(internal, f.bots, q, verify); err != nil {
			t.Fatalf("idempotent retry: %v", err)
		}
		var fresh store.BotStore
		switch d := f.repo.db.(type) {
		case teamBotDB:
			fresh = &teamBotStore{client: d.client, auth: f.repo.auth}
		case personalBotDB:
			fresh = &personalBotStore{client: d.client, auth: f.repo.auth}
		}
		got, err := fresh.Get(f.owner, b.ID)
		if err != nil || got.Home != q.Home || got.Revision != b.Revision+1 || got.Status != store.BotDraft {
			t.Fatalf("fresh adapter binding: %+v %v", got, err)
		}
		q.Home.ID = uuid.New()
		if err := RegisterBotHomeBinding(internal, f.bots, q, verify); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("replacement: %v", err)
		}
		runs, err := fresh.ListRuns(f.owner, b.ID, store.BotPage{})
		if err != nil || len(runs) != 0 {
			t.Fatalf("binding executed work: %+v %v", runs, err)
		}
	})
}
func TestIntegrationPostgresBotPausedStoppedConfiguration(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, status := range []store.BotStatus{store.BotPaused, store.BotStopped} {
			t.Run(string(status), func(t *testing.T) {
				f := newPostgresBotServicesFixture(t, scope, 100, 1000)
				exerciseBotInactiveConfiguration(t, f.repo, f.owner, f.worker, f.run, f.lease, status)
			})
		}
	})
}
