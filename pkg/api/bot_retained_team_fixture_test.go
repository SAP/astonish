package api

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Retained team fixtures use trusted scoped storage, never public creation.
// The caller supplies the actual authenticated owner context used by later HTTP requests.
func retainedTeamBotFixture(t *testing.T, ctx context.Context, repo store.BotStore, owner, name, key, charter, soul string) store.Bot {
	t.Helper()
	v, err := store.NewBotVersionForCreation(ctx, uuid.MustParse(owner))
	if err != nil {
		t.Fatal(err)
	}
	v.Charter = charter
	v.Persona.Soul = soul
	b, err := repo.Create(ctx, store.BotCreateRequest{Name: name, OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return *b
}
