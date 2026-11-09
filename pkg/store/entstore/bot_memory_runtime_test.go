package entstore

import (
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"testing"
	"time"
)

func TestBotRunMemory(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, memories, ctx, b, other := botMemorySearchFixture(t, scope, "vector")
			var repo *botRepository
			switch s := bots.(type) {
			case *personalBotStore:
				repo = s.repository()
				if err := s.client.Bot.UpdateOneID(b.ID).SetStatus("active").Exec(ctx); err != nil {
					t.Fatal(err)
				}
			case *teamBotStore:
				repo = s.repository()
				if err := s.client.Bot.UpdateOneID(b.ID).SetStatus("active").Exec(ctx); err != nil {
					t.Fatal(err)
				}
			}
			run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, IdempotencyKey: "memory", Instructions: "Remember controlled facts"})
			if err != nil {
				t.Fatal(err)
			}
			p, _ := execution.PrincipalFromContext(ctx)
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
			lease, err := bots.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "memory", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			svc, err := store.NewBotRunMemoryService(bots, memories, lease, scope)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Add(worker, store.MemoryEntry{SessionID: run.SessionID.String(), Content: "quasar owned", Category: "fact"}); err != nil {
				t.Fatal(err)
			}
			rows, err := svc.Search(worker, "quasar", 10, 0)
			if err != nil || len(rows) != 1 {
				t.Fatalf("search: %+v %v", rows, err)
			}
			id := rows[0].ID
			if err := svc.Update(worker, id, "quasar updated", "fact"); err != nil {
				t.Fatal(err)
			}
			got, err := svc.Get(worker, id)
			if err != nil || got.Snippet != "quasar updated" {
				t.Fatalf("get: %+v %v", got, err)
			}
			for _, sid := range []string{other.HomeSessionID.String(), b.HomeSessionID.String()} {
				if err := svc.Add(worker, store.MemoryEntry{SessionID: sid, Content: "forbidden"}); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("foreign/prior write: %v", err)
				}
			}
			stale := lease
			stale.Fence++
			denied, err := store.NewBotRunMemoryService(bots, memories, stale, scope)
			if err != nil {
				t.Fatal(err)
			}
			if err := denied.Delete(worker, id); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale fence: %v", err)
			}
			if err := svc.Delete(worker, id); err != nil {
				t.Fatal(err)
			}
			rows, err = svc.Search(worker, "quasar", 10, 0)
			if err != nil || len(rows) != 0 {
				t.Fatalf("deleted search: %+v %v", rows, err)
			}
		})
	}
}
