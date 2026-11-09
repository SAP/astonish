package entstore

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotMessageAdmissionDurable(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, _, _ := runtimeFixture(t, scope)
			p, _ := execution.PrincipalFromContext(ctx)
			v := store.DefaultBotVersion()
			v.Charter = "Discuss a draft safely"
			b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Chat", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "chat"})
			if err != nil {
				t.Fatal(err)
			}
			repo.activationCheck = func(context.Context, store.Bot, store.BotVersion) error {
				t.Fatal("control conversation performed execution readiness")
				return nil
			}
			q := store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "turn", Instructions: "Hello"}
			receipt, err := repo.AdmitMessage(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Run.Intent != store.BotIntentDraftConversation || receipt.Run.Presentation != store.BotRunPresentationHomeInteractive || receipt.HomeSessionID != b.HomeSessionID || receipt.UserEventID == "" || receipt.HistoryCursor <= 0 {
				t.Fatalf("invalid receipt: %+v", receipt)
			}
			again, err := repo.AdmitMessage(ctx, q)
			if err != nil || again.Run.ID != receipt.Run.ID || again.UserEventID != receipt.UserEventID || again.HistoryCursor != receipt.HistoryCursor {
				t.Fatalf("unstable receipt %+v %v", again, err)
			}
			q.Instructions = "Changed"
			if _, err := repo.AdmitMessage(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("collision: %v", err)
			}
			after, err := repo.Get(ctx, b.ID)
			if err != nil || after.Status != store.BotDraft || after.Version != b.Version {
				t.Fatal("message changed lifecycle", err)
			}
		})
	}
}
