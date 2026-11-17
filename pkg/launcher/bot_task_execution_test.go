package launcher

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

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

// These fixtures replace storage and the model, not the production factory,
// policy wrappers, prompt builder or tool loop. No external provider is used.
type retainedTaskRepo struct {
	botFactoryTestRepo
	store.BotTaskStore
	current store.BotTask
	other   store.BotTask
}

func (r *retainedTaskRepo) ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error) {
	return nil, nil
}
func (r *retainedTaskRepo) ListTasks(context.Context, store.BotID, store.BotTaskPage) (*store.BotTaskList, error) {
	return &store.BotTaskList{Tasks: []store.BotTask{r.current, r.other}}, nil
}
func (r *retainedTaskRepo) GetTask(_ context.Context, b store.BotID, id uuid.UUID) (*store.BotTask, error) {
	if b != r.current.BotID {
		return nil, store.ErrBotForbidden
	}
	task := r.current
	if id == r.other.ID {
		task = r.other
	} else if id != task.ID {
		return nil, store.ErrBotNotFound
	}
	task.Head = task.Head.Clone()
	return &task, nil
}

type retainedTaskWorkspace struct {
	bot.TaskWorkspace
	revisions    map[store.BotTaskRef]*store.BotTaskRevision
	loads        []store.BotTaskRef
	executions   int
	publications int
}

func (w *retainedTaskWorkspace) Load(_ context.Context, b store.BotID, ref store.BotTaskRef) (*store.BotTaskRevision, error) {
	w.loads = append(w.loads, ref)
	r := w.revisions[ref]
	if r == nil || r.BotID != b {
		return nil, store.ErrBotNotFound
	}
	return r.Clone(), nil
}
func (w *retainedTaskWorkspace) Publish(context.Context, store.BotTaskPrepareRequest) (*store.BotTask, error) {
	w.publications++
	return nil, errors.New("unexpected publication")
}
func (w *retainedTaskWorkspace) Execute(_ context.Context, _ store.BotTaskMutation, ref store.BotTaskRef, path string, _ execution.RunBudget) (*sandbox.BotHomeWorkspaceScriptResult, error) {
	w.executions++
	if w.revisions[ref] == nil || path != "scripts/helper.py" {
		return nil, store.ErrBotInvalid
	}
	return &sandbox.BotHomeWorkspaceScriptResult{}, nil
}

const retainedDefinition = "# Procedure\n\n## Output\nRead the controlled inventory and report its count.\n"
const retainedAssignment = "Compare inventory availability for the owner's regional stores; preserve the requested count format."
const retainedTitle = "Regional inventory report"

func retainedTaskFixture(t *testing.T) (*botFactoryFixture, *retainedTaskRepo, *retainedTaskWorkspace) {
	t.Helper()
	f := newBotFactoryFixture(t)
	f.req.Bot.Name = "Inventory assistant"
	f.req.Run.Trigger = store.TriggerCron
	f.req.Run.TriggerID = uuid.New()
	ref := store.BotTaskRef{TaskID: uuid.New(), Revision: 1, Digest: strings.Repeat("a", 64)}
	revision := &store.BotTaskRevision{SchemaVersion: store.BotTaskSchemaVersion, ID: uuid.New(), BotID: f.req.Bot.ID, Ref: ref, Manifest: store.BotTaskManifest{SchemaVersion: store.BotTaskSchemaVersion, BotID: f.req.Bot.ID, TaskID: ref.TaskID, Title: retainedTitle, Assignment: retainedAssignment}, Bundle: map[string][]byte{"TASK.md": []byte(retainedDefinition)}}
	definitionFile := sandbox.BotHomeWorkspaceFile{Path: "TASK.md", Data: []byte(retainedDefinition)}
	revision.Manifest.Assets = []store.BotTaskAsset{{Path: "TASK.md", Kind: store.BotTaskAssetDefinition, Digest: definitionFile.Digest(), SizeBytes: int64(len(definitionFile.Data))}}
	revision.Ref.Digest = revision.ContentDigest()
	ref = revision.Ref
	if err := revision.Validate(store.BotTaskDraft); err != nil {
		t.Fatal(err)
	}
	f.req.Run.Task = ref.Clone()
	f.req.Task = revision.Clone()
	// LoadRunInput replaces the saved scheduler label with these exact bytes.
	f.req.Instructions = retainedDefinition
	repo := &retainedTaskRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, current: store.BotTask{ID: ref.TaskID, BotID: f.req.Bot.ID, Title: retainedTitle, Status: store.BotTaskReady, Head: ref.Clone(), Revision: 1}}
	repo.other = store.BotTask{ID: uuid.New(), BotID: f.req.Bot.ID, Title: "Other procedure", Status: store.BotTaskReady, Head: &store.BotTaskRef{TaskID: uuid.New(), Revision: 1, Digest: strings.Repeat("b", 64)}, Revision: 1}
	repo.other.Head.TaskID = repo.other.ID
	w := &retainedTaskWorkspace{revisions: map[store.BotTaskRef]*store.BotTaskRevision{ref: revision}}
	other := revision.Clone()
	other.Ref = *repo.other.Head
	other.Bundle["TASK.md"] = []byte("Other task bytes")
	w.revisions[other.Ref] = other
	f.services.PersonalBots, f.services.Bots = repo, repo
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(c context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		resources, err := prepare(c, r, s)
		resources.TaskWorkspace = w
		return resources, err
	}
	return f, repo, w
}

type retainedTaskExecutionModel struct {
	t     *testing.T
	calls int
}

func (*retainedTaskExecutionModel) Name() string { return "controlled-retained-task" }
func (m *retainedTaskExecutionModel) GenerateContent(_ context.Context, q *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		var text strings.Builder
		for _, p := range q.Config.SystemInstruction.Parts {
			text.WriteString(p.Text)
		}
		for _, c := range q.Contents {
			for _, p := range c.Parts {
				text.WriteString(p.Text)
			}
		}
		if m.calls == 1 {
			for _, required := range []string{retainedDefinition, retainedAssignment, retainedTitle, "already executing", "scripts/"} {
				if !strings.Contains(text.String(), required) {
					m.t.Errorf("production model input missing %q", required)
				}
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "inventory-read", Name: "read_file", Args: map[string]any{}}}}}}, nil)
			return
		}
		seen := false
		for _, c := range q.Contents {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil && p.FunctionResponse.Name == "read_file" && p.FunctionResponse.Response["content"] == "Available: 17" {
					seen = true
				}
			}
		}
		if !seen {
			m.t.Error("actual read receipt missing before final answer")
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Available: 17", "model"), TurnComplete: true}, nil)
	}
}

func TestBotRetainedTaskToolContractAndPin(t *testing.T) {
	f, repo, workspace := retainedTaskFixture(t)
	resources, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Cleanup()
	binding := resources.Agent.LLM.(*botFactoryLLM).binding
	tools, err := newBotTaskTools(binding)
	if err != nil {
		t.Fatal(err)
	}
	catalog := map[string]botRunnableTool{}
	for _, tt := range tools {
		catalog[tt.Name()] = tt.(botRunnableTool)
	}
	ctx := botFactoryHandoffContext{base: f.ctx, session: f.req.Run.SessionID.String(), functionCallID: "task-contract"}
	declaration := catalog["bot_task_execute"].Declaration()
	if !strings.Contains(declaration.Description, "does not invoke, enqueue or schedule") || !strings.Contains(declaration.Description, "scripts/") {
		t.Fatalf("ambiguous helper declaration: %+v", declaration)
	}
	for _, path := range []string{"", "TASK.md", "scripts/../helper.py", "scripts/helper.sh"} {
		result, err := catalog["bot_task_execute"].Run(ctx, map[string]any{"task_id": repo.other.ID.String(), "path": path})
		diagnostic, _ := result["error"].(string)
		if err != nil || !strings.Contains(diagnostic, "does not invoke a Task") {
			t.Fatalf("path %q: result=%v err=%v", path, result, err)
		}
		if binding.tasks.current.ID != f.req.Run.Task.TaskID || workspace.executions != 0 {
			t.Fatal("invalid helper request changed selection or executed")
		}
	}
	// A newer registry head must not replace the immutable occurrence on read.
	repo.current.Head.Revision++
	repo.current.Head.Digest = strings.Repeat("c", 64)
	result, err := catalog["bot_task_read"].Run(ctx, map[string]any{"task_id": repo.current.ID.String()})
	if err != nil || result["content"] != retainedDefinition || *binding.tasks.current.Head != *f.req.Run.Task {
		t.Fatalf("pin lost: %v %v", result, err)
	}
	result, err = catalog["bot_task_read"].Run(ctx, map[string]any{"task_id": repo.other.ID.String()})
	if err != nil || result["content"] != "Other task bytes" {
		t.Fatalf("other task selection broken: %v %v", result, err)
	}
	_, err = catalog["bot_task_execute"].Run(ctx, map[string]any{"path": "scripts/helper.py"})
	if err != nil || workspace.executions != 1 {
		t.Fatalf("valid helper dispatch: %v calls=%d", err, workspace.executions)
	}
	result, err = catalog["bot_task_read"].Run(ctx, map[string]any{"task_id": repo.current.ID.String()})
	if err != nil || result["content"] != retainedDefinition || *binding.tasks.current.Head != *f.req.Run.Task {
		t.Fatalf("return to original pin: %v %v", result, err)
	}
	repo.current.Status = store.BotTaskArchived
	if _, err = catalog["bot_task_read"].Run(ctx, map[string]any{"task_id": repo.current.ID.String()}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("archival check bypassed: %v", err)
	}
}

func TestBotRetainedTaskGuidanceModes(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "scheduled", true: "owner-conversation"}[interactive], func(t *testing.T) {
			f, _, w := retainedTaskFixture(t)
			// Factory setup uses the background wiring; the guidance's typed mode is
			// changed independently to test priority without inventing interactive auth.
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			binding := resources.Agent.LLM.(*botFactoryLLM).binding
			binding.req.Interactive = interactive
			text, err := binding.tasks.guidance(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(text, "already executing") == interactive {
				t.Fatalf("wrong execution guidance for interactive=%v", interactive)
			}
			// Oversized definitions must remain reachable without silently dropping them.
			w.revisions[*f.req.Run.Task].Bundle["TASK.md"] = []byte(strings.Repeat("x", 12001))
			text, err = binding.tasks.guidance(f.ctx)
			if err != nil || !strings.Contains(text, "Read it with bot_task_read") || !strings.Contains(text, retainedAssignment) {
				t.Fatalf("oversized context: %v %q", err, text)
			}
		})
	}
}

func TestBotRetainedTaskProductionFactoryExecution(t *testing.T) {
	f, _, workspace := retainedTaskFixture(t)
	llm := &retainedTaskExecutionModel{t: t}
	f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
	reads := 0
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read controlled inventory"}, func(ctx tool.Context, _ map[string]any) (map[string]any, error) {
		reads++
		bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true})
		return map[string]any{"content": "Available: 17"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(c context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		resources, err := prepare(c, r, s)
		resources.Tools = []tool.Tool{read}
		return resources, err
	}
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(factory)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != store.RunSucceeded || result.Report.Summary != "Available: 17" || reads != 1 || llm.calls != 2 || workspace.executions != 0 || workspace.publications != 0 {
		t.Fatalf("result=%+v reads=%d model=%d scripts=%d publications=%d", result, reads, llm.calls, workspace.executions, workspace.publications)
	}
	if len(workspace.loads) == 0 {
		t.Fatal("factory bypassed workspace pin load")
	}
	for _, ref := range workspace.loads {
		if ref != *f.req.Run.Task {
			t.Fatal("factory read a different Task revision")
		}
	}
}
