package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBotTitlePublicResearchAdmission(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, _, _ := runtimeFixture(t, scope)
			ctx = mandateOwnerContext(t, ctx)
			ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(8))
			policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, err := mandateTestPolicy(c, ref)
				p.PublicResearch = &execution.BotPublicResearchScope{Version: 1, Route: execution.BotConfiguredSearchRoute{Engine: execution.BotSearchPerplexity, Provider: "owned", Model: "sonar", Tool: "perplexity_web_search", Fingerprint: "owned", CredentialHandles: []string{"provider:owned:api_key"}}, Facets: []execution.BotResearchFacet{execution.BotResearchOverview}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 2}
				return p, err
			}
			ctx = store.WithBotMandateAdmission(ctx, policy)
			principal, _ := execution.PrincipalFromContext(ctx)
			version := store.DefaultBotVersion()
			version.Charter = "Private charter must never become query text"
			request := store.BotCreateRequest{Name: "AI News", OwnerID: uuid.MustParse(principal.Subject), Version: version, IdempotencyKey: "ordinary-title"}
			first, err := repo.CreateDraftAndStart(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			record, err := readMandate(ctx, repo.db, *first.Run.MandateRef)
			if err != nil {
				t.Fatal(err)
			}
			if record.Provenance.InitiationKind != store.BotInitiationKindTitle || record.Mandate.PublicResearch == nil || record.Mandate.PublicResearch.Subject != "AI News" || record.Mandate.PublicResearch.SourceEventID != first.UserEventID {
				t.Fatalf("ordinary title lost bounded research intent: %+v", record)
			}
			replay, err := repo.CreateDraftAndStart(ctx, request)
			if err != nil || replay.Run.ID != first.Run.ID || *replay.Run.MandateRef != *first.Run.MandateRef {
				t.Fatalf("replay changed authority: %+v %v", replay, err)
			}
			direct, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: first.ID, Version: 1, Instructions: "AI News using my private assignment", IdempotencyKey: "private-turn"})
			if err != nil {
				t.Fatal(err)
			}
			private, err := readMandate(ctx, repo.db, *direct.Run.MandateRef)
			if err != nil || private.Mandate.PublicResearch != nil {
				t.Fatalf("private follow-up inferred disclosure: %+v %v", private, err)
			}
			// Adapter state must not turn attachments or mismatched private text
			// into the initially admitted public subject.
			for _, text := range []string{"Private attachment discussion", "AI News\nPrivate assignment"} {
				bound := context.WithValue(ctx, titleMandateAdmissionKey{}, titleMandateAdmission{Kind: store.BotInitiationKindTitle, Subject: "AI News"})
				turn, err := repo.AdmitMessage(bound, store.BotMessageRequest{BotID: first.ID, Version: 1, Instructions: text, IdempotencyKey: text})
				if err != nil {
					t.Fatal(err)
				}
				record, err := readMandate(ctx, repo.db, *turn.Run.MandateRef)
				if err != nil || record.Mandate.PublicResearch != nil {
					t.Fatalf("mismatched initiation disclosed: %+v %v", record, err)
				}
			}
		})
	}
}
