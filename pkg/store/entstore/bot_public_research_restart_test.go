package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotPublicResearchRestartQuota(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, botID, _, _, reopen := ApprovalScopeReopenFixture(t, scope)
			ctx = mandateOwnerContext(t, ctx)
			policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, err := mandateTestPolicy(c, ref)
				p.PublicResearch = &execution.BotPublicResearchScope{Version: 1, Route: execution.BotConfiguredSearchRoute{Engine: "perplexity", Provider: "owned", Model: "sonar", Tool: "bot_public_research", Fingerprint: "owned-config", CredentialHandles: []string{"provider:owned:api_key"}}, Facets: []execution.BotResearchFacet{execution.BotResearchOverview}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 2}
				return p, err
			}
			ctx = store.WithBotMandateAdmission(ctx, policy)
			bots, _ := reopen()
			messages := bots.(store.BotMessageStore)
			admitted, err := messages.AdmitMessage(ctx, store.BotMessageRequest{BotID: botID, Version: 1, Instructions: "Public research subject: Stock Market News\nPrivate assignment must not enter query", IdempotencyKey: "public-source"})
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
			identity := execution.BotRunIdentity{BotID: botID, RunID: admitted.Run.ID}
			bound := execution.WithBotRunIdentity(ctx, identity)
			quota := rt.(*botMandateRuntime)
			if err = quota.ReserveBotPublicResearch(bound, ref, "parent"); err != nil {
				t.Fatal(err)
			}
			t.Run("reopen-ceiling", func(t *testing.T) {
				bots, _ = reopen()
				rt, err = NewBotMandateRuntime(bots, policy)
				if err != nil {
					t.Fatal(err)
				}
				quota = rt.(*botMandateRuntime)
				if err = rt.ReserveBotMandateRun(ctx, ref, admitted.Run.ID); err != nil {
					t.Fatal("idempotent run reserve", err)
				}
				if err = quota.ReserveBotPublicResearch(bound, ref, "parent"); err != nil {
					t.Fatal("remaining call lost", err)
				}
				bots, _ = reopen()
				rt, err = NewBotMandateRuntime(bots, policy)
				if err != nil {
					t.Fatal(err)
				}
				quota = rt.(*botMandateRuntime)
				if err = quota.ReserveBotPublicResearch(bound, ref, "parent"); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("restart reset ceiling: %v", err)
				}
				rec, err := quota.repo.db.GetBot(ctx, botID)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(rec.Metadata[mandateUsageKey])
				var usage mandateUsage
				if err = json.Unmarshal(raw, &usage); err != nil || usage.Active[admitted.Run.ID.String()].PublicResearchCalls != 2 {
					t.Fatalf("durable quota projection: %s %v", raw, err)
				}
			})
			t.Run("reopen-provenance", func(t *testing.T) {
				replay, e := bots.(store.BotMessageStore).AdmitMessage(ctx, store.BotMessageRequest{BotID: botID, Version: 1, Instructions: "Public research subject: Stock Market News\nPrivate assignment must not enter query", IdempotencyKey: "public-source"})
				if e != nil || replay.Run.ID != admitted.Run.ID || replay.Run.MandateRef == nil || *replay.Run.MandateRef != ref {
					t.Fatalf("admission receipt changed: %+v %v", replay, e)
				}
				m, e := rt.VerifyBotMandate(ctx, ref, execution.BotMandatePurposeTask)
				if e != nil || m.PublicResearch == nil || m.PublicResearch.Subject != "Stock Market News" || m.PublicResearch.SourceEventID != admitted.Run.UserEventID {
					t.Fatalf("disclosure provenance lost: %+v %v", m.PublicResearch, e)
				}
				q, e := m.PublicResearch.Query(execution.BotResearchOverview)
				if e != nil || q != "Stock Market News overview" {
					t.Fatalf("private query leaked: %q %v", q, e)
				}
				private, e := bots.(store.BotMessageStore).AdmitMessage(ctx, store.BotMessageRequest{BotID: botID, Version: 1, Instructions: "Private assignment: find public market news using confidential context", IdempotencyKey: "private-source"})
				if e != nil {
					t.Fatal(e)
				}
				privateMandate, e := rt.VerifyBotMandate(ctx, *private.Run.MandateRef, execution.BotMandatePurposeTask)
				if e != nil || privateMandate.PublicResearch != nil {
					t.Fatalf("unlabelled private assignment acquired disclosure: %+v %v", privateMandate.PublicResearch, e)
				}
				foreign := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: uuid.New(), RunID: admitted.Run.ID})
				if e = quota.ReserveBotPublicResearch(foreign, ref, "child"); !errors.Is(e, store.ErrBotForbidden) {
					t.Fatalf("foreign Bot reserved: %v", e)
				}
			})
			t.Run("released-no-replay", func(t *testing.T) {
				if err = rt.ReleaseBotMandateRun(ctx, ref, admitted.Run.ID); err != nil {
					t.Fatal(err)
				}
				bots, _ = reopen()
				rt, err = NewBotMandateRuntime(bots, policy)
				if err != nil {
					t.Fatal(err)
				}
				if err = rt.ReserveBotMandateRun(ctx, ref, admitted.Run.ID); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("released run replayed: %v", err)
				}
				if err = rt.(*botMandateRuntime).ReserveBotPublicResearch(bound, ref, "parent"); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("released search replayed: %v", err)
				}
			})
		})
	}
}
