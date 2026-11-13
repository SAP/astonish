package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"testing"

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

type preexecutionPublicationModel struct {
	t            *testing.T
	orientations int
}

func (*preexecutionPublicationModel) Name() string { return "preexecution-publication" }
func (m *preexecutionPublicationModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		prompt := req.Config.SystemInstruction.Parts[0].Text
		if !strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1") {
			m.t.Errorf("unexpected production/narration after failed orientation: %.80s", prompt)
		} else {
			m.orientations++
		}
		if len(req.Tools) != 0 {
			m.t.Error("orientation has execution tools")
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"PRIVATE_MALFORMED_ORIENTATION": }`, "model")}, nil)
	}
}

// Isolated real HTTP -> production worker -> SQLite -> owner history/reload.
// No daemon process or external provider is used.
func TestBotPreexecutionPublicationHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", nil, []string{"chat", "tool:execute"}, scope)
			llm := &preexecutionPublicationModel{t: t}
			var err error
			f.worker, err = daemon.NewBotWorker(daemon.BotWorkerConfig{
				Backend: f.db, Sandbox: journeyHomeBackend{}, Pool: f.pool, TurnRegistry: f.registry, WorkerID: uuid.NewString(), ContextWindow: 1000000,
				ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 1000, SupportsOutputLimit: true},
				ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
					return llm, nil
				},
				Prepare: func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
					return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			var receipt store.BotMessageReceipt
			body := fmt.Sprintf(`{"version":%d,"instructions":"Write a report","idempotency_key":"preexecution-failure"}`, f.b.Version)
			if err := json.Unmarshal(f.request("POST", f.path+"/messages", body, 202), &receipt); err != nil {
				t.Fatal(err)
			}
			// Historical assessment behavior remains pinned to its original protocol.
			pinLegacyHTTPAttempt(t, f, receipt.Run)
			if err := f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			var run store.BotRun
			if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+receipt.Run.ID.String(), "", 200), &run); err != nil {
				t.Fatal(err)
			}
			m := run.Report.EmployeeMessage
			if m == nil || !strings.Contains(m.Text, "couldn't start the requested work") || !strings.Contains(m.Text, "No result was produced") || strings.Contains(m.Text, "incomplete result") || m.Reason != store.BotEmployeeReasonSetup {
				t.Fatalf("misleading owner publication: %+v", m)
			}
			if run.Status != store.RunPartial || llm.orientations != 2 || f.pool.calls != 0 || len(run.Report.Artifacts) != 0 || len(run.Report.Changed) != 0 {
				t.Fatalf("unexpected execution: status=%s orientations=%d tools=%d report=%+v", run.Status, llm.orientations, f.pool.calls, run.Report)
			}
			if q := run.Report.Quality; q == nil || q.CandidateDigest != "" {
				t.Fatalf("unexpected candidate: %+v", q)
			}
			if got := bot.RenderReport(run); got != m.Text+"\n" {
				t.Fatalf("terminal parity: %q", got)
			}
			history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
			if strings.Count(history, m.Text) != 1 || strings.Contains(history, "PRIVATE_MALFORMED_ORIENTATION") || strings.Contains(history, "incomplete result") {
				t.Fatalf("unsafe owner history: %s", history)
			}
			t.Logf("%s: exhausted orientation (%d attempts), zero tools/candidate/artifacts; HTTP history and terminal share no-result message", scope, llm.orientations)
		})
	}
}
