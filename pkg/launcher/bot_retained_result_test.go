package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// Controlled workspace, not a physical Home: the concrete Task tool and real
// ChatRunner/adapter execute; publication and sources are deterministic fixtures.
type retainedResultWorkspace struct {
	bot.TaskWorkspace
	revision              store.BotTaskRevision
	scripts, publications int
	executeErr            error
}

func (w *retainedResultWorkspace) Load(context.Context, store.BotID, store.BotTaskRef) (*store.BotTaskRevision, error) {
	return w.revision.Clone(), nil
}
func (w *retainedResultWorkspace) Publish(_ context.Context, q store.BotTaskPrepareRequest) (*store.BotTask, error) {
	w.publications++
	w.revision = q.Candidate
	q.Task.Head = q.Candidate.Ref.Clone()
	q.Task.Revision = int64(w.publications + 1)
	return &q.Task, nil
}
func (w *retainedResultWorkspace) Execute(_ context.Context, _ store.BotTaskMutation, _ store.BotTaskRef, path string, _ execution.RunBudget) (*sandbox.BotHomeWorkspaceScriptResult, error) {
	w.scripts++
	if w.executeErr != nil {
		return nil, w.executeErr
	}
	if path == "" {
		return nil, store.ErrBotInvalid
	}
	return &sandbox.BotHomeWorkspaceScriptResult{}, nil
}

type retainedResultWork struct{ state *store.BotWorkState }

func (w *retainedResultWork) LoadWork(context.Context, store.BotLease) (*store.BotWorkState, error) {
	return w.state.Clone(), nil
}
func (w *retainedResultWork) SaveWork(_ context.Context, q store.BotWorkUpdate) (*store.BotWorkState, error) {
	if err := store.ValidateBotWorkUpdate(w.state, &q.State); err != nil {
		return nil, err
	}
	w.state = q.State.Clone()
	w.state.Revision++
	return w.state.Clone(), nil
}

func retainedResultTaskTools(t *testing.T) (*botFactoryFixture, *retainedResultWorkspace, []tool.Tool) {
	t.Helper()
	f := newBotFactoryFixture(t)
	f.req.Run.Intent = store.BotIntentRegular
	f.req.Instructions = "Invoke retained Task"
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomyReversible, AllowedTools: []string{"bot_task_execute", "bot_task_read", "bot_task_update", "bot_configured_search", "web_fetch"}})
	if err != nil {
		t.Fatal(err)
	}
	f.req.Policy = policy
	ref := store.BotTaskRef{TaskID: uuid.New(), Revision: 15, Digest: strings.Repeat("a", 64)}
	f.req.Run.Task = &ref
	workspace := &retainedResultWorkspace{revision: store.BotTaskRevision{Ref: ref, Bundle: map[string][]byte{"TASK.md": []byte("Research products and companies")}}}
	binding := &botFactoryBinding{owner: store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services), req: f.req, verifyIsolation: func(context.Context) error { return nil }}
	binding.tasks = &botTaskServices{binding: binding, workspace: workspace, tasks: taskWorkspaceRegistry{revision: workspace.revision}, current: &store.BotTask{ID: ref.TaskID, BotID: f.req.Bot.ID, Title: "Products and companies", Head: &ref, Revision: 15}}
	tools, err := newBotTaskTools(binding)
	if err != nil {
		t.Fatal(err)
	}
	return f, workspace, tools
}

func TestBotRetainedTaskResearchFinal(t *testing.T) {
	f, workspace, actions := retainedResultTaskTools(t)
	fetches := 0
	for _, name := range []string{"bot_configured_search", "web_fetch"} {
		name := name
		action, err := functiontool.New(functiontool.Config{Name: name, Description: "Controlled public source"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
			if name == "web_fetch" {
				fetches++
				if fetches == 1 {
					return map[string]any{"error": "source returned status 403", "status_code": 403}, nil
				}
			}
			return map[string]any{"content": "Company A released Product B; Company C released Product D."}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
	}
	answer := "## Products and companies\n\nCompany A released Product B; Company C released Product D.\n\nLimitations: one source returned HTTP 403; the invalid script request did not execute."
	llm := &retainedResultModel{turns: []*genai.Content{
		retainedTaskTurn("bot_task_execute", map[string]any{"task_id": f.req.Run.Task.TaskID.String()}),
		retainedTaskTurn("bot_task_read", map[string]any{}),
		retainedTaskTurn("bot_configured_search", map[string]any{"facet": "overview"}),
		retainedTaskTurn("web_fetch", map[string]any{"url": "https://example.test/blocked"}),
		retainedTaskTurn("web_fetch", map[string]any{"url": "https://example.test/a"}),
		retainedTaskTurn("web_fetch", map[string]any{"url": "https://example.test/b"}),
		retainedTaskTurn("web_fetch", map[string]any{"url": "https://example.test/c"}),
		retainedTaskTurn("bot_task_update", map[string]any{"content": "# Products and companies\nUse working sources and report limitations."}),
		genai.NewContentFromText(answer, "model"),
	}}
	work := &retainedResultWork{state: store.NewBotAgentWorkState(store.BotWorkContract{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Run.Version, Instructions: f.req.Instructions})}
	work.state.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	runtime := agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: WithBotTaskClassifier(botFactoryTestClassifier{}, actions)}
	adapter, err := api.NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*api.BotChatResources, error) {
		return &api.BotChatResources{Agent: agent.NewChatAgent(llm, actions, nil, f.sessions, &agent.SystemPromptBuilder{}, false, false), SessionService: f.sessions, AppName: "astonish", PolicyRuntime: &runtime, Work: work, Cleanup: func() {}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil || result.Report.Summary != answer || result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || result.Report.Failure != nil {
		t.Logf("tools=%d fetches=%d publications=%d model=%d", workspace.scripts, fetches, workspace.publications, llm.calls)
		t.Fatalf("completed research discarded: result=%+v err=%v", result, err)
	}
	if workspace.scripts != 0 || workspace.publications != 1 || fetches != 4 || llm.calls != 9 || result.Usage.ToolCalls != 8 {
		t.Fatalf("unexpected execution/replay: scripts=%d publications=%d fetches=%d model=%d usage=%+v", workspace.scripts, workspace.publications, fetches, llm.calls, result.Usage)
	}
	if workspace.revision.Ref.Revision != 16 || workspace.revision.Ref.Digest == f.req.Run.Task.Digest {
		t.Fatal("Task revision not retained")
	}
	if result.Status != store.RunPartial || !work.state.HasUsefulPartialReadOnlyEmployeeResult(result.Status, result.Report) || work.state.EmployeeReviewAttempts != 0 {
		t.Fatalf("lost limitations or added review: %+v", result)
	}
	raw, err := json.Marshal(work.state)
	if err != nil {
		t.Fatal(err)
	}
	var restored store.BotWorkState
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(result.Report)
	if err != nil {
		t.Fatal(err)
	}
	var report store.BotReport
	if err = json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateBotWorkCompletion(&restored, store.BotRunCommit{Lease: f.req.Run.Lease, Status: result.Status, Report: report}); err != nil {
		t.Fatalf("restored commit invalid: %v", err)
	}
	if err = report.ValidateEmployeeMessage(result.Status, &restored); err != nil {
		t.Fatal(err)
	}
}

func TestBotTaskInvalidExecuteNonexecutionBoundary(t *testing.T) {
	for _, mode := range []string{"missing-path", "traversal", "not-script", "backend-invalid", "backend-cancel", "revoked", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f, w, actions := retainedResultTaskTools(t)
			ctx := optionalKnowledgeContext{knowledgeCtx(f)}
			ctx.base, _ = bot.WithToolOutcomes(ctx.base)
			path := ""
			switch mode {
			case "traversal":
				path = "../escape.py"
			case "not-script":
				path = "TASK.md"
			case "backend-invalid":
				path = "scripts/run.py"
				w.executeErr = store.ErrBotInvalid
			case "backend-cancel":
				path = "scripts/run.py"
				w.executeErr = context.Canceled
			case "revoked":
				f.req.Bot.Status = store.BotPaused
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx.base)
				cancel()
				ctx.base = cancelled
			}
			var execute botRunnableTool
			for _, action := range actions {
				if action.Name() == "bot_task_execute" {
					execute = action.(botRunnableTool)
				}
			}
			result, err := execute.Run(ctx, map[string]any{"path": path})
			outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
			safe := mode == "missing-path" || mode == "traversal" || mode == "not-script"
			if safe {
				if err != nil || result["error"] == nil || w.scripts != 0 || !ok || !outcome.Verified || outcome.Nonexecution == nil || outcome.Nonexecution.CallID != ctx.FunctionCallID() || outcome.Nonexecution.ToolName != "bot_task_execute" || outcome.ReceiptConflict || len(outcome.Unchanged) != 0 {
					t.Fatalf("invalid invocation lacks exclusive nonexecution receipt: result=%v err=%v calls=%d outcome=%+v", result, err, w.scripts, outcome)
				}
			} else {
				if err == nil || ok {
					t.Fatalf("uncertain/backend/policy error recovered: result=%v err=%v outcome=%+v", result, err, outcome)
				}
				if mode == "backend-invalid" && !errors.Is(err, store.ErrBotInvalid) {
					t.Fatal(err)
				}
				if mode == "backend-cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
		})
	}
}

type retainedResultModel struct {
	turns []*genai.Content
	calls int
}

func (*retainedResultModel) Name() string { return "retained-task-fixture" }
func (m *retainedResultModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.calls >= len(m.turns) {
			yield(nil, errors.New("unexpected extra model call"))
			return
		}
		content := m.turns[m.calls]
		m.calls++
		yield(&model.LLMResponse{Content: content, TurnComplete: true}, nil)
	}
}
func retainedTaskTurn(name string, args map[string]any) *genai.Content {
	return &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}}}}
}
