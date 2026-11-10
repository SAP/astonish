package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestInteractiveAccountingDurableLedgerAndReplay(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat", "tool:execute"}, scope)
			first := f.turn()
			if first.Usage.ToolCalls != 1 || first.Usage.InputTokens != 20 || first.Usage.OutputTokens != 12 {
				t.Fatalf("initial ledger: %+v", first.Usage)
			}
			replay := f.turn()
			if replay.ID != first.ID || f.pool.calls != 1 || replay.Usage != first.Usage {
				t.Fatalf("replay billed twice: first=%+v replay=%+v calls=%d", first.Usage, replay.Usage, f.pool.calls)
			}
			f.model.called = false
			var receipt store.BotMessageReceipt
			body := fmt.Sprintf(`{"version":%d,"instructions":"Read again","idempotency_key":"second-turn"}`, f.b.Version)
			if err := json.Unmarshal(f.request("POST", f.path+"/messages", body, 202), &receipt); err != nil {
				t.Fatal(err)
			}
			if err := f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			second, err := f.repo.GetRun(f.ctx, f.b.ID, receipt.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if second.ID == first.ID || f.pool.calls != 2 || second.Usage.ToolCalls != 1 || second.Usage.InputTokens != 20 || second.Usage.OutputTokens != 12 {
				t.Fatalf("existing-session new turn: %+v calls=%d", second.Usage, f.pool.calls)
			}
			original, err := f.repo.GetRun(f.ctx, f.b.ID, first.ID)
			if err != nil {
				t.Fatal(err)
			}
			if original.Usage != first.Usage {
				t.Fatalf("new turn mutated original ledger: %+v", original.Usage)
			}
		})
	}
}
