package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// A rejected Task update must never reach workspace persistence. The embedded
// interface deliberately has no implementation: any call fails this regression.
type synthesisReceiptUnusedWorkspace struct{ bot.TaskWorkspace }

// Exercise the incident's actual sequence, not a fabricated effect ledger:
// HTTP admission -> real file read -> synthesis rejects a Task update -> final
// publication. Only the model and isolated tool-node transport are controlled.
func TestBotProgressSynthesisReceiptHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			f.worker.Stop()
			// Enable the genuine Task tools through the production composition seam.
			var err error
			f.worker, err = daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: journeyHomeBackend{}, Pool: f.pool, TurnRegistry: f.registry, WorkerID: uuid.NewString(), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return f.model, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, TaskWorkspace: synthesisReceiptUnusedWorkspace{}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer f.worker.Stop()
			v, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
			if err != nil {
				t.Fatal(err)
			}
			// A deliberately short, owner-reviewed hard limit makes the existing
			// synthesis boundary observable without changing a production clock.
			v.Budget.MaxRuntimeSeconds = 10
			body, err := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated synthesis receipt regression", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(f.request("PUT", f.path+"/configuration", string(body), 200), &f.b); err != nil {
				t.Fatal(err)
			}
			const final = "The research file contains: isolated receipt. The optional Task update was not executed."
			step := 0
			f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
				step++
				part := &genai.Part{Text: final}
				switch step {
				case 1:
					part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "research-read", Name: "read_file", Args: map[string]any{"path": "/isolated/receipt.txt"}}}
				case 2:
					raw, _ := json.Marshal(req.Contents)
					if !strings.Contains(string(raw), "isolated receipt") {
						t.Error("actual research result missing")
					}
					// This request began in research mode. Cross the reserve while
					// generating, so the tool wrapper (not model correction) stops it.
					time.Sleep(8100 * time.Millisecond)
					part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "stopped-task-update", Name: "bot_task_update", Args: map[string]any{"title": "Optional research tracking"}}}
				case 3:
					raw, _ := json.Marshal(req.Contents)
					if !strings.Contains(string(raw), `"status":"not_executed"`) || !strings.Contains(string(raw), "Synthesis reserve has begun") {
						t.Errorf("missing synthesis rejection: %s", raw)
					}
					if len(req.Tools) != 0 || (req.Config != nil && len(req.Config.Tools) != 0) {
						t.Error("synthesis retained tools")
					}
				default:
					t.Errorf("unnecessary recovery/review model call %d", step)
				}
				return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, true
			}
			var admission store.BotMessageReceipt
			if err := json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Read /isolated/receipt.txt and report its contents. Task tracking is optional.","idempotency_key":"synthesis-receipt"}`, f.b.Version), 202), &admission); err != nil {
				t.Fatal(err)
			}
			if err := f.worker.Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			var run store.BotRun
			if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+admission.Run.ID.String(), "", 200), &run); err != nil {
				t.Fatal(err)
			}
			if run.Status != store.RunSucceeded || run.Report.Summary != final || len(run.Report.Unverified) != 0 {
				t.Fatalf("completed research falsely uncertain: status=%s report=%+v", run.Status, run.Report)
			}
			if step != 3 || f.model.reviews != 0 || f.pool.calls != 1 {
				t.Fatalf("unexpected execution/review: model=%d reviews=%d real reads=%d", step, f.model.reviews, f.pool.calls)
			}
			if run.Usage.ToolCalls != 1 {
				t.Fatalf("rejected tool counted as dispatch: %+v", run.Usage)
			}
			historyPath := f.path + "/sessions/" + f.b.HomeSessionID.String() + "/stream"
			for range 2 {
				history := string(f.request("GET", historyPath, "", 200))
				if strings.Count(history, "bot-report:"+run.ID.String()) != 1 || !strings.Contains(history, final) {
					t.Fatalf("final not published exactly once: %s", history)
				}
				if err := f.worker.Poll(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
