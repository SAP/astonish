//go:build integration

package entstore

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestIntegrationPostgresMemoryAttribution(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DSN") == "" {
		t.Fatal("ASTONISH_TEST_DSN required (disposable PostgreSQL)")
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"vector", "fts", "hybrid"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				f := newPostgresBotServicesFixture(t, scope, 100, 100)
				a, err := f.bots.Get(f.owner, f.run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				v := store.DefaultBotVersion()
				v.Charter = "Other Bot"
				b, err := f.bots.CreateDraft(f.owner, store.BotCreateRequest{Name: "Other", OwnerID: a.OwnerID, Version: v, IdempotencyKey: "attribution-other"})
				if err != nil {
					t.Fatal(err)
				}
				var embed store.EmbedFunc
				if mode != "fts" {
					embed = func(context.Context, string) ([]float32, error) { v := make([]float32, 384); v[0] = 1; return v, nil }
				}
				switch m := f.memories.(type) {
				case *teamMemoryStore:
					m.embedFunc = embed
				case *personalMemoryStore:
					m.embedFunc = embed
				}
				id := attributionContract(t, f.bots, f.memories, f.owner, a, b, scope, f.run.ID, f.run.SessionID.String())
				before, err := f.memories.Get(f.owner, id)
				if err != nil {
					t.Fatal(err)
				}
				// Recreate concrete adapters without any vector cache; persisted metadata
				// and embeddings, not process-local state, must drive retrieval.
				switch m := f.memories.(type) {
				case *teamMemoryStore:
					f.memories = &teamMemoryStore{client: m.client, db: m.db, dialect: m.dialect, table: m.table, ftsTable: m.ftsTable, embedFunc: m.embedFunc}
				case *personalMemoryStore:
					f.memories = &personalMemoryStore{client: m.client, db: m.db, dialect: m.dialect, table: m.table, ftsTable: m.ftsTable, embedFunc: m.embedFunc}
				}
				svc, _ := store.NewBotMemoryService(f.bots, f.memories, a.ID, scope)
				query := "needle"
				if mode == "vector" {
					query = "semanticprobe"
				}
				rows, err := svc.SearchAttributed(f.owner, store.MemoryAttributionFilter{BotID: a.ID, GoalID: before.Attribution.GoalID, RunID: f.run.ID}, query, 1, 0, "fact")
				if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].Attribution, before.Attribution) {
					t.Fatalf("fresh adapter search: %+v %v", rows, err)
				}
			})
		}
	}
}
