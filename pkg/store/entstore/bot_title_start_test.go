package entstore

import (
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotTitleStartAtomicReplay(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, _, _ := runtimeFixture(t, scope)
			p, _ := execution.PrincipalFromContext(ctx)
			ctx, err := execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(1)
			ctx = store.WithBotMessageAdmission(ctx, registry)
			v := store.DefaultBotVersion()
			v.Charter = "Discuss title"
			q := store.BotCreateRequest{Name: "Research the project", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "title"}
			first, err := repo.CreateDraftAndStart(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if first.Status != store.BotDraft || !first.Run.IsInteractiveChat() || first.UserEventID == "" || first.HistoryCursor == 0 {
				t.Fatalf("receipt: %+v", first)
			}
			again, err := repo.CreateDraftAndStart(ctx, q)
			if err != nil || again.Run.ID != first.Run.ID || again.Bot.ID != first.Bot.ID {
				t.Fatalf("replay: %+v %v", again, err)
			}
			q.Name = "Different"
			if _, err := repo.CreateDraftAndStart(ctx, q); err != store.ErrBotConflict {
				t.Fatalf("mismatch: %v", err)
			}
		})
	}
}

func TestBotTitleStartAdmissionRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, _, _ := runtimeFixture(t, scope)
			p, _ := execution.PrincipalFromContext(ctx)
			v := store.DefaultBotVersion()
			v.Charter = "Discuss title"
			q := store.BotCreateRequest{Name: "Atomic", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "rollback"}
			// Missing authentication evidence makes live registry registration fail after
			// draft/home writes have begun; the outer transaction must roll everything back.
			ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(1))
			if _, err := repo.CreateDraftAndStart(ctx, q); err == nil {
				t.Fatal("missing authority accepted")
			}
			bots, err := repo.List(ctx, store.BotPage{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range bots {
				if b.Name == q.Name {
					t.Fatal("orphan draft survived admission failure")
				}
			}
		})
	}
}
