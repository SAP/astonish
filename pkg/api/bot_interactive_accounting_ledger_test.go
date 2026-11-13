package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

func TestInteractiveAccountingDurableLedgerAndReplay(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", scope, legacy), func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat", "tool:execute"}, scope)
				// Agent-led work only requests the tool and produces the answer.
				// Historical work additionally orients, prepares and reviews; those
				// calls must still use the same meter, never become free checks.
				wantCalls, wantChecks := 2, 0
				if legacy {
					wantCalls, wantChecks = 5, 3
				}
				wantInput, wantOutput := int64(wantCalls*5), int64(wantCalls*3)
				calls, checks := 0, 0
				assessment := f.model.assessment
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					calls++
					response, ok := assessment(req)
					if ok {
						checks++
					}
					return response, ok
				}
				turn := func(key string, pin bool) *store.BotRun {
					t.Helper()
					var receipt store.BotMessageReceipt
					body := fmt.Sprintf(`{"version":%d,"instructions":"Read the controlled receipt","idempotency_key":%q}`, f.b.Version, key)
					if err := json.Unmarshal(f.request("POST", f.path+"/messages", body, 202), &receipt); err != nil {
						t.Fatal(err)
					}
					if legacy && pin {
						pinLegacyHTTPAttempt(t, f, receipt.Run)
					}
					if err := f.worker.Poll(context.Background()); err != nil {
						t.Fatal(err)
					}
					run, err := f.repo.GetRun(f.ctx, f.b.ID, receipt.Run.ID)
					if err != nil {
						t.Fatal(err)
					}
					if run.Status != store.RunSucceeded || (!legacy && (run.Work == nil || run.Work.Protocol != store.BotWorkProtocolAgentLedV1)) || (legacy && run.Report.Quality == nil) {
						t.Fatalf("unexpected completion/protocol: %+v", run)
					}
					return run
				}
				first := turn("turn", true)
				if first.Usage.ToolCalls != 1 || first.Usage.InputTokens != wantInput || first.Usage.OutputTokens != wantOutput || calls != wantCalls || checks != wantChecks {
					t.Fatalf("initial ledger: %+v calls=%d checks=%d", first.Usage, calls, checks)
				}
				replay := turn("turn", false)
				if replay.ID != first.ID || f.pool.calls != 1 || replay.Usage != first.Usage || calls != wantCalls || checks != wantChecks {
					t.Fatalf("replay billed twice: first=%+v replay=%+v tools=%d calls=%d checks=%d", first.Usage, replay.Usage, f.pool.calls, calls, checks)
				}
				f.model.called = false
				second := turn("second-turn", true)
				if second.ID == first.ID || f.pool.calls != 2 || second.Usage.ToolCalls != 1 || second.Usage.InputTokens != wantInput || second.Usage.OutputTokens != wantOutput || calls != 2*wantCalls || checks != 2*wantChecks {
					t.Fatalf("existing-session new turn: %+v tools=%d calls=%d checks=%d", second.Usage, f.pool.calls, calls, checks)
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
}
