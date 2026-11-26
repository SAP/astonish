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
			for i, definition := range []string{``, `,"start_from_title":false`, `,"definition":{"charter":"Observe only"}`} {
				var b store.Bot
				body := fmt.Sprintf(`{"name":"Ordinary","idempotency_key":"ordinary-%d"%s}`, i, definition)
				if err := json.Unmarshal(f.request("POST", "/api/studio/bots", body, 201), &b); err != nil {
					t.Fatal(err)
				}
				assertInert(b, 0)
			}
			body := `{"name":"Reply once","idempotency_key":"title-once","start_from_title":true}`
			var first, replay struct {
				store.Bot
				InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
			}
			if err := json.Unmarshal(f.request("POST", "/api/studio/bots", body, 201), &first); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(f.request("POST", "/api/studio/bots", body, 201), &replay); err != nil {
				t.Fatal(err)
			}
			if first.InitialTurn == nil || replay.InitialTurn == nil || first.ID != replay.ID || first.InitialTurn.Run.ID != replay.InitialTurn.Run.ID {
				t.Fatal("explicit title admission was not exactly once")
			}
			if !first.InitialTurn.Run.IsInteractiveChat() {
				t.Fatal("title did not admit the requested conversation")
			}
			assertInert(first.Bot, 1)
			if f.pool.calls != 0 || f.model.called {
				t.Fatal("creation executed provider/tool work")
			}
		})
	}
}
