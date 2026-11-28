package api_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// Exercise real HTTP admission against disposable scoped SQLite stores. No worker
// is polled: creation must not need a provider or execute unsolicited work.
func TestBotActiveCreateHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			assertInert := func(b store.Bot, wantRuns int) {
				t.Helper()
				saved, err := f.repo.Get(f.ctx, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				if b.Status != store.BotActive || saved.Status != store.BotActive {
					t.Fatalf("create status: response=%s persisted=%s", b.Status, saved.Status)
				}
				runs, err := f.repo.ListRuns(f.ctx, b.ID, store.BotPage{Limit: 100})
				if err != nil {
					t.Fatal(err)
				}
				if len(runs) != wantRuns {
					t.Fatalf("unsolicited/duplicate runs: got %d want %d", len(runs), wantRuns)
				}
				version, err := f.repo.GetVersion(f.ctx, b.ID, saved.Version)
				if err != nil {
					t.Fatal(err)
				}
				for _, trigger := range version.Triggers {
					if trigger.Enabled {
						t.Fatal("creation enabled recurrence")
					}
				}
			}
			assertInert(f.b, 0)
			create := func(body string, title bool) []byte {
				t.Helper()
				if scope == store.BotScopePersonal {
					return f.request("POST", "/api/studio/bots", body, 201)
				}
				// Public team creation is forbidden, but retained team admission
				// still uses the same scoped store and owner turn registry.
				f.request("POST", "/api/studio/bots", body, 403)
				var q struct {
					Name       string `json:"name"`
					Key        string `json:"idempotency_key"`
					Definition struct {
						Charter string `json:"charter"`
					} `json:"definition"`
				}
				if err := json.Unmarshal([]byte(body), &q); err != nil {
					t.Fatal(err)
				}
				v, err := store.NewBotVersionForCreation(f.ctx, f.b.OwnerID)
				if err != nil {
					t.Fatal(err)
				}
				v.Charter = q.Definition.Charter
				if v.Charter == "" {
					v.Charter = "Take responsibility for the owner's goals and deliver useful, verified outcomes. Perform routine work within your authorized mandate and existing permissions without extra review. Escalate genuine ambiguity or actions outside that scope for clarification or authorization. A request or this charter does not itself grant permission for external side effects."
					if title {
						v.Charter = q.Name + "\n\n" + v.Charter
					}
				}
				request := store.BotCreateRequest{Name: q.Name, OwnerID: f.b.OwnerID, Version: v, IdempotencyKey: q.Key}
				var result any
				if title {
					receipt, err := f.repo.(store.BotTitleStartStore).CreateAndStart(store.WithBotMessageAdmission(f.ctx, f.registry), request)
					if err != nil {
						t.Fatal(err)
					}
					result = titleHTTPReceipt{Bot: receipt.Bot, InitialTurn: &store.BotMessageReceipt{Run: receipt.Run, HomeSessionID: receipt.HomeSessionID, UserEventID: receipt.UserEventID, HistoryCursor: receipt.HistoryCursor}}
				} else {
					result, err = f.repo.Create(f.ctx, request)
					if err != nil {
						t.Fatal(err)
					}
				}
				raw, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			for i, definition := range []string{``, `,"start_from_title":false`, `,"definition":{"charter":"Observe only"}`} {
				var b store.Bot
				body := fmt.Sprintf(`{"name":"Ordinary","idempotency_key":"ordinary-%d"%s}`, i, definition)
				if err := json.Unmarshal(create(body, false), &b); err != nil {
					t.Fatal(err)
				}
				assertInert(b, 0)
			}
			body := `{"name":"Reply once","idempotency_key":"title-once","start_from_title":true}`
			var first, replay struct {
				store.Bot
				InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
			}
			if err := json.Unmarshal(create(body, true), &first); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(create(body, true), &replay); err != nil {
				t.Fatal(err)
			}
			if first.InitialTurn == nil || replay.InitialTurn == nil || first.ID != replay.ID || first.InitialTurn.Run.ID != replay.InitialTurn.Run.ID {
				t.Fatal("explicit title admission was not exactly once")
			}
			if !first.InitialTurn.Run.IsInteractiveChat() {
				t.Fatal("title did not admit the requested conversation")
			}
			assertInert(first.Bot, 1)
			var restored store.Bot
			if err := json.Unmarshal(f.request("GET", "/api/studio/bots/"+first.ID.String(), "", 200), &restored); err != nil {
				t.Fatal(err)
			}
			if restored.ID != first.ID || restored.Scope != scope {
				t.Fatal("HTTP read did not retain the scoped Bot")
			}
			if f.pool.calls != 0 || f.model.called {
				t.Fatal("creation executed provider/tool work")
			}
		})
	}
}
