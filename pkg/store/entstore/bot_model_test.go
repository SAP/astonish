package entstore

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestAuthorizeBotModelConfiguration(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, _, run, _, _ := botRunFixture(t, scope)
			var bots store.BotStore
			switch d := r.db.(type) {
			case personalBotDB:
				bots = &personalBotStore{client: d.client, auth: r.auth}
			case teamBotDB:
				bots = &teamBotStore{client: d.client, auth: r.auth}
			}
			if _, err := AuthorizeBotModelConfiguration(ctx, bots, run.BotID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("active run: %v", err)
			}
			owner := uuid.MustParse(r.auth.userID)
			v := store.DefaultBotVersion()
			v.Charter = "Review the model"
			draft, err := bots.CreateDraft(ctx, store.BotCreateRequest{Name: "Model review", OwnerID: owner, Version: v, IdempotencyKey: "model-review"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := AuthorizeBotModelConfiguration(ctx, bots, draft.ID); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"user", "org", "team"} {
				if scope == store.BotScopePersonal && field == "team" {
					continue
				}
				p, _ := execution.PrincipalFromContext(ctx)
				switch field {
				case "user":
					p.Subject = uuid.NewString()
				case "org":
					p.OrgSlug = "foreign"
				case "team":
					p.TeamSlug = "foreign"
				}
				foreign, err := execution.WithPrincipal(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := AuthorizeBotModelConfiguration(foreign, bots, draft.ID); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("foreign %s: %v", field, err)
				}
			}
			switch s := bots.(type) {
			case *personalBotStore:
				s.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
			case *teamBotStore:
				s.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
			}
			if _, err := AuthorizeBotModelConfiguration(ctx, bots, draft.ID); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("viewer: %v", err)
			}
		})
	}
}
