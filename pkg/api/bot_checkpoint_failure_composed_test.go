package api_test

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type checkpointHTTPModel struct{ calls int }

func (*checkpointHTTPModel) Name() string { return "checkpoint-http" }
func (m *checkpointHTTPModel) GenerateContent(_ context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := journeyWorkAssessment(req); ok {
		return func(yield func(*model.LLMResponse, error) bool) { yield(response, nil) }
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		parts := []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "read_file", Args: map[string]any{"path": "receipt.txt"}}}}
		if m.calls > 1 {
			parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "send_email", Args: map[string]any{"body": "must-not-leak"}}})
		}
		yield(scriptedHTTPProgressiveResponse(req, &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: parts}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}), nil)
	}
}

// Isolated HTTP admission, production worker/ChatRunner, SQLite commit and Home
// publication. The model and Home are controlled; no user run is replayed.
func TestBotCheckpointFailureCommittedHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "receipt.txt"}, []string{"chat", "tool:execute"}, scope)
			llm := &checkpointHTTPModel{}
			var err error
			f.worker, err = daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: f.workerConfig.Sandbox, Pool: f.pool, TurnRegistry: f.registry, WorkerID: uuid.NewString(), ContextWindow: 1000000,
				ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true},
				ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
					return mediateHTTPModel(llm), nil
				},
				Prepare: f.workerConfig.Prepare,
			})
			if err != nil {
				t.Fatal(err)
			}
			run := f.turn()
			if run.Status != store.RunFailed || run.Report.Failure == nil || run.Report.Failure.Kind != store.BotReportFailureExecutionFormat {
				t.Fatalf("missing failed format report: %+v", run)
			}
			if len(run.Report.Unchanged) != 2 || run.Usage.ToolCalls != 1 || f.pool.calls != 1 || llm.calls != 2 {
				t.Fatalf("prior evidence/rejection lost: %+v calls=%d model=%d", run, f.pool.calls, llm.calls)
			}
			if len(run.Report.NeedsApproval) != 0 || len(run.Checkpoint) != 0 {
				t.Fatal("format failure requested approval")
			}
			publisher := &bot.Publisher{Store: f.repo.(store.BotPublicationStore)}
			for range 2 {
				if _, err := publisher.Publish(f.ctx, f.b.ID, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if f.pool.calls != 1 || llm.calls != 2 {
				t.Fatal("terminal run retried")
			}
			restored, err := f.repo.GetRun(f.ctx, f.b.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if restored.Report.Failure == nil || restored.Report.Failure.Kind != store.BotReportFailureExecutionFormat || len(restored.Report.Unchanged) != 2 {
				t.Fatal("restore lost typed failure or evidence")
			}
			if restored.TerminalReason != store.BotTerminalNoAnswer {
				t.Fatalf("malformed model response must close without invented Employee speech: %+v", restored)
			}
			for range 2 {
				history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
				if strings.Contains(history, "[Error:") || strings.Contains(history, "event: error") || strings.Contains(history, `"type":"error"`) || strings.Contains(history, "must-not-leak") {
					t.Fatalf("raw failure leaked: %s", history)
				}
				if strings.Contains(history, "bot-report:"+run.ID.String()) || !strings.Contains(history, `"terminal_reason":"no_eligible_answer"`) {
					t.Fatalf("missing durable neutral terminal or invented answer: %s", history)
				}
			}
			source := string(f.request("GET", f.path+"/sessions/"+run.SessionID.String()+"/stream", "", 200))
			if strings.Contains(source, "[Error:") || strings.Contains(source, `"type":"error"`) || strings.Contains(source, "must-not-leak") {
				t.Fatalf("rejected response leaked: %s", source)
			}
		})
	}
}
