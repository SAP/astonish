//go:build integration

package entstore

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestIntegrationPostgresBotMandateModelReservation(t *testing.T) {
	requireDraftActivationPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newPostgresBotServicesFixture(t, scope, 100, 100)
		if err := f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: f.lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, f.worker, f.lease, "fixture complete")}); err != nil {
			t.Fatal(err)
		}
		policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
			p, err := mandateTestPolicy(c, ref)
			p.Limits.MaxTokens = 100000
			p.Limits.MaxTotalTokens = 150000
			return p, err
		}
		ctx := store.WithBotMandateAdmission(mandateOwnerContext(t, f.owner), policy)
		admitted, err := f.repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: f.run.BotID, Version: 2, Instructions: "read the weather", IdempotencyKey: "pg-model-budget"})
		if err != nil {
			t.Fatal(err)
		}
		ref, run := *admitted.Run.MandateRef, admitted.Run.ID
		newRuntime := func() store.BotMandateRuntime {
			t.Helper()
			rt, err := NewBotMandateRuntime(f.bots, policy)
			if err != nil {
				t.Fatal(err)
			}
			return rt
		}
		rt := newRuntime()
		// A second platform/router owns independent SQL pools against the same
		// tenant; correctness must not depend on sharing a Go repository instance.
		peer, err := New(ctx, Config{DSN: f.platform.platformDSN, InstanceSuffix: f.platform.instanceSuffix})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { closePostgresFixtureStore(t, peer) })
		org, err := peer.ForOrg(ref.OrgSlug)
		if err != nil {
			t.Fatal(err)
		}
		var peerBots store.BotStore
		if scope == store.BotScopeTeam {
			peerBots = org.ForTeam(ref.TeamSlug).Bots()
		} else {
			peerBots = org.ForUser(ref.OwnerID).Bots()
		}
		peerRuntime, err := NewBotMandateRuntime(peerBots, policy)
		if err != nil {
			t.Fatal(err)
		}
		if err = rt.ReserveBotMandateRun(ctx, ref, run); err != nil {
			t.Fatal(err)
		}
		for _, call := range []struct{ hold, actual int64 }{{33308, 10097}, {38278, 12238}} {
			id := uuid.New()
			if err = rt.ReserveBotMandateModel(ctx, ref, run, id, call.hold); err != nil {
				t.Fatal(err)
			}
			if err = rt.SettleBotMandateModel(ctx, ref, run, id, call.actual); err != nil {
				t.Fatal(err)
			}
			postgresReviewedRace(t, func() error { return rt.SettleBotMandateModel(ctx, ref, run, id, 0) })
		}
		f.reopenReviewed(t)
		rt = newRuntime()
		if err = rt.ConsumeBotMandateQuota(ctx, ref, run, 0, 40000, 0); err != nil {
			t.Fatalf("reopened continuation retained unused reservation: %v", err)
		}
		id := uuid.New()
		if err = rt.ReserveBotMandateModel(ctx, ref, run, id, 10000); err != nil {
			t.Fatal(err)
		}
		var settlers atomic.Int64
		postgresReviewedRace(t, func() error {
			if settlers.Add(1)%2 == 0 {
				return peerRuntime.SettleBotMandateModel(ctx, ref, run, id, 1000)
			}
			return rt.SettleBotMandateModel(ctx, ref, run, id, 1000)
		})
		f.reopenReviewed(t)
		rt = newRuntime()
		var quota *store.BotMandateQuotaError
		err = rt.ConsumeBotMandateQuota(ctx, ref, run, 0, 40000, 0)
		if !errors.As(err, &quota) || quota.Used != 63335 || quota.Limit != 100000 {
			t.Fatalf("durable exact-once settlement: %+v %v", quota, err)
		}
		if err = rt.SettleBotMandateModel(ctx, ref, run, uuid.New(), 0); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("unknown refund: %v", err)
		}
		over := uuid.New()
		if err = rt.ReserveBotMandateModel(ctx, ref, run, over, 1); err != nil {
			t.Fatal(err)
		}
		if err = rt.SettleBotMandateModel(ctx, ref, run, over, 100000); err != nil {
			t.Fatal(err)
		}
		f.reopenReviewed(t)
		rt = newRuntime()
		err = rt.ConsumeBotMandateQuota(ctx, ref, run, 0, 1, 0)
		if !errors.As(err, &quota) || quota.Used != 163335 {
			t.Fatalf("actual overage lost after reopen: %+v %v", quota, err)
		}
	})
}
