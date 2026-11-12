package agent

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/credentials"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type handoffTestLLM struct {
	generate func(context.Context, *model.LLMRequest) (*model.LLMResponse, error)
}

func (*handoffTestLLM) Name() string { return "handoff-test" }
func (m *handoffTestLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) { resp, err := m.generate(ctx, req); yield(resp, err) }
}
func handoffDone() *model.LLMResponse {
	return &model.LLMResponse{Content: genai.NewContentFromText("done", "model")}
}

type handoffTestService struct {
	adksession.Service
	records []string
	fail    bool
}

func (s *handoffTestService) AppendEvent(ctx context.Context, sess adksession.Session, event *adksession.Event) error {
	if text, ok := event.Actions.StateDelta[DelegationHandoffStateKey].(string); ok {
		if event.Content != nil {
			return errors.New("handoff must be state-only")
		}
		if s.fail {
			return errors.New("handoff storage unavailable")
		}
		s.records = append(s.records, text)
	}
	return s.Service.AppendEvent(ctx, sess, event)
}
func handoffFixture(t *testing.T) (*SubAgentManager, *handoffTestService, SubAgentTask) {
	t.Helper()
	svc := &handoffTestService{Service: adksession.InMemoryService()}
	m := NewSubAgentManager(SubAgentConfig{})
	m.SessionService, m.AppName, m.UserID, m.WorkDir = svc, "handoff-test", "user", t.TempDir()
	seed, err := CaptureDelegationPlan(context.Background()).ForTask("do work", "prior findings: use exact path", "")
	if err != nil {
		t.Fatal(err)
	}
	task := SubAgentTask{Name: "worker", Description: "do work", Instructions: "prior findings: use exact path", ParentID: "parent", BatchID: "batch", TaskID: "task", ChildSessionID: "child", Handoff: seed}
	m.LLM = &handoffTestLLM{generate: func(context.Context, *model.LLMRequest) (*model.LLMResponse, error) { return handoffDone(), nil }}
	return m, svc, task
}
func decodeHandoff(t *testing.T, text string) DelegationHandoff {
	t.Helper()
	var h DelegationHandoff
	if err := json.Unmarshal([]byte(text), &h); err != nil {
		t.Fatal(err)
	}
	return h
}
func handoffRequestText(req *model.LLMRequest) string {
	var b strings.Builder
	if req.Config != nil && req.Config.SystemInstruction != nil {
		for _, p := range req.Config.SystemInstruction.Parts {
			b.WriteString(p.Text)
		}
	}
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func TestDelegationHandoffModelAndStoredRecord(t *testing.T) {
	m, svc, task := handoffFixture(t)
	secret := "raw-credential-12345"
	m.Redactor = credentials.NewRedactor()
	m.Redactor.AddSecret("test", secret)
	task.Description = "implement %{http_code} " + secret
	task.Instructions = "Prior findings: exact symbol Foo and path a.go. " + secret
	snapshot := PlanSnapshot{PlanID: "plan", Revision: 7, Sequence: 19, Goal: "goal", Document: PlanDocumentInfo{Context: "context " + secret, WhatNotToDo: "no UI", Verification: "acceptance"}, Steps: []PlanStepInfo{{Name: "phase", Description: "description", Details: "detail " + secret, Summary: "summary", Files: []PlanFileChange{{Path: "a.go", Kind: "modify"}}, Outcome: "outcome", Verify: "go test", VerifyKind: "unit", ParallelGroup: "wave-1"}, {Name: "sibling", Details: "SIBLING_PRIVATE"}}}
	runtime := NewPlanRuntimeFromSnapshot(snapshot, PlanRuntimeOptions{})
	ctx := WithDelegationPlanSource(WithPlanRuntime(context.Background(), runtime), "/sessions/parent/PLAN.md")
	var err error
	task.Handoff, err = CaptureDelegationPlan(ctx).ForTask(task.Description, task.Instructions, "phase")
	if err != nil {
		t.Fatal(err)
	}
	task.PlanStep = "phase"
	called := false
	m.LLM = &handoffTestLLM{generate: func(_ context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
		called = true
		if len(svc.records) != 1 {
			t.Fatal("handoff not persisted before model")
		}
		text := handoffRequestText(req)
		if strings.Count(text, svc.records[0]) != 1 {
			t.Fatal("provider did not receive exact stored handoff once")
		}
		if strings.Contains(text, secret) || strings.Contains(text, "SIBLING_PRIVATE") {
			t.Fatal("secret or sibling phase leaked")
		}
		h := decodeHandoff(t, svc.records[0])
		if h.Version != 1 || h.ParentSessionID != "parent" || h.BatchID != "batch" || h.TaskID != "task" || h.ChildSessionID != "child" || h.Attempt != 1 {
			t.Fatalf("identity: %+v", h)
		}
		if h.Plan.Revision != 7 || h.Plan.Sequence != 19 || h.Plan.PlanID != "plan" || h.Plan.SourceMirror != "/sessions/parent/PLAN.md" {
			t.Fatalf("plan: %+v", h.Plan)
		}
		if h.Plan.Step.Details != "detail [REDACTED]" || h.Plan.Document.WhatNotToDo != "no UI" || h.Plan.Step.VerifyKind != "unit" {
			t.Fatalf("phase: %+v", h.Plan)
		}
		if h.EffectiveTask != "implement %{http_code} [REDACTED]" || !strings.Contains(h.EffectiveInstructions, "Prior findings: exact symbol") {
			t.Fatalf("task: %+v", h)
		}
		var names []string
		for _, group := range req.Config.Tools {
			for _, decl := range group.FunctionDeclarations {
				names = append(names, decl.Name)
			}
		}
		if !reflect.DeepEqual(names, h.InitialToolNames) {
			t.Fatalf("initial declarations %v != %v", names, h.InitialToolNames)
		}
		return handoffDone(), nil
	}}
	result := m.RunTask(ctx, task)
	if result.Status != "success" || !called {
		t.Fatalf("result: %+v", result)
	}
	got, err := svc.Get(ctx, &adksession.GetRequest{AppName: m.AppName, UserID: m.UserID, SessionID: task.ChildSessionID})
	if err != nil {
		t.Fatal(err)
	}
	value, err := got.Session.State().Get(DelegationHandoffStateKey)
	if err != nil || value != svc.records[0] {
		t.Fatalf("state not persisted: %v %v", value, err)
	}
}

func TestDelegationHandoffFileStoreReopen(t *testing.T) {
	ctx := context.Background()
	m, _, task := handoffFixture(t)
	dir := t.TempDir()
	fs, err := persistentsession.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Match production creation: identity starts only in CreateRequest.State.
	created, err := fs.Create(ctx, &adksession.CreateRequest{
		AppName: m.AppName, UserID: m.UserID, SessionID: task.ChildSessionID,
		State: map[string]any{
			persistentsession.StateKeyParentID: task.ParentID,
			"batch_id":                         task.BatchID, "task_id": task.TaskID,
			"child_session_id": task.ChildSessionID, "attempt": 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, err := m.freezeDelegationHandoff(task, task.ParentID, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendDelegationHandoff(ctx, fs, created.Session, text); err != nil {
		t.Fatal(err)
	}
	// No other event supplies identity before reopening a fresh FileStore.
	reopened, err := persistentsession.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(ctx, &adksession.GetRequest{AppName: m.AppName, UserID: m.UserID, SessionID: task.ChildSessionID})
	if err != nil {
		t.Fatal(err)
	}
	h := decodeHandoff(t, text)
	want := map[string]any{
		DelegationHandoffStateKey: text, persistentsession.StateKeyParentID: h.ParentSessionID,
		"batch_id": h.BatchID, "task_id": h.TaskID, "child_session_id": h.ChildSessionID, "attempt": h.Attempt,
	}
	for key, expected := range want {
		value, err := got.Session.State().Get(key)
		// JSON round-tripping may restore numeric state as float64.
		actualJSON, _ := json.Marshal(value)
		expectedJSON, _ := json.Marshal(expected)
		if err != nil || string(actualJSON) != string(expectedJSON) {
			t.Errorf("reopened state %q = %s (%v), want %s", key, actualJSON, err, expectedJSON)
		}
	}
	if got.Session.Events().Len() != 1 {
		t.Fatalf("expected one atomic handoff event, got %d", got.Session.Events().Len())
	}
}

func TestAppendDelegationHandoffRejectsInvalidJSON(t *testing.T) {
	for _, text := range []string{"{bad", `{"attempt":"one"}`, `{} {}`} {
		t.Run(text, func(t *testing.T) {
			m, svc, task := handoffFixture(t)
			created, err := svc.Create(context.Background(), &adksession.CreateRequest{AppName: m.AppName, UserID: m.UserID, SessionID: task.ChildSessionID})
			if err != nil {
				t.Fatal(err)
			}
			if err := appendDelegationHandoff(context.Background(), svc, created.Session, text); err == nil {
				t.Fatal("invalid handoff was accepted")
			}
			if len(svc.records) != 0 || created.Session.Events().Len() != 0 {
				t.Fatal("invalid handoff appended an event")
			}
		})
	}
}

func TestDelegationHandoffSerializationPreservesIntegersAndUTF8(t *testing.T) {
	m, svc, task := handoffFixture(t)
	const sequence int64 = 9007199254740993
	ctx := WithPlanRuntime(context.Background(), NewPlanRuntimeFromSnapshot(PlanSnapshot{
		PlanID: "plan", Revision: sequence, Sequence: sequence,
		Steps: []PlanStepInfo{{Name: "phase"}},
	}, PlanRuntimeOptions{}))
	var err error
	task.Description = "invalid UTF8: \xff; literal {task_id}; quote \" and newline\n"
	task.Handoff, err = CaptureDelegationPlan(ctx).ForTask(task.Description, task.Instructions, "phase")
	if err != nil {
		t.Fatal(err)
	}
	m.LLM = &handoffTestLLM{generate: func(_ context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
		if len(svc.records) != 1 || strings.Count(handoffRequestText(req), svc.records[0]) != 1 {
			t.Fatal("provider serialization differs from stored record")
		}
		h := decodeHandoff(t, svc.records[0])
		if h.Plan.Revision != sequence || h.Plan.Sequence != sequence || h.EffectiveTask != strings.ToValidUTF8(task.Description, "\ufffd") {
			t.Fatalf("serialization changed handoff values: %+v", h)
		}
		return handoffDone(), nil
	}}
	if result := m.RunTask(ctx, task); result.Status != "success" {
		t.Fatal(result)
	}
}

func TestDelegationHandoffCaptureFrozenAndMissing(t *testing.T) {
	plan := NewPlanState("original", PlanDocumentInfo{PlanID: "p", Revision: 1, Sequence: 2}, []PlanStepInfo{{Name: "one", Details: "original detail", Files: []PlanFileChange{{Path: "original.go"}}}})
	runtime := NewPlanRuntime(plan, PlanRuntimeOptions{})
	capture := CaptureDelegationPlan(WithPlanRuntime(context.Background(), runtime))
	// Simulate a concurrent accepted revision after the batch capture.
	plan.mu.Lock()
	plan.goal = "new goal"
	plan.doc.Revision = 2
	plan.steps[0].details = "new detail"
	plan.steps[0].files[0].Path = "new.go"
	plan.mu.Unlock()
	seed, err := capture.ForTask("work", "findings", "one")
	if err != nil {
		t.Fatal(err)
	}
	if seed.plan.Goal != "original" || seed.plan.Revision != 1 || seed.plan.Step.Details != "original detail" || seed.plan.Step.Files[0].Path != "original.go" {
		t.Fatalf("capture changed: %+v", seed.plan)
	}
	if _, err := capture.ForTask("", "", "ONE"); err == nil || !strings.Contains(err.Error(), "available steps: one") {
		t.Fatalf("expected exact-match error: %v", err)
	}
	missing, err := capture.ForTask("", "", "")
	if err != nil || missing.plan.Status != DelegationPlanStepMissing || missing.plan.Step != nil {
		t.Fatal("step inferred")
	}
	for _, ctx := range []context.Context{nil, context.Background(), WithPlanRuntime(WithPlanRuntime(context.Background(), runtime), nil)} {
		missing, err := CaptureDelegationPlan(ctx).ForTask("", "", "one")
		if err != nil || missing.plan.Status != DelegationPlanMissing || missing.plan.Step != nil {
			t.Fatal("missing scope inferred")
		}
	}
}

func TestDelegationHandoffFailsClosedAndCustomOptOut(t *testing.T) {
	for _, kind := range []string{"append", "oversize", "reserved", "custom", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			m, svc, task := handoffFixture(t)
			calls := 0
			m.LLM = &handoffTestLLM{generate: func(_ context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
				calls++
				if strings.Contains(handoffRequestText(req), "Immutable Delegation Handoff") {
					t.Fatal("custom/legacy prompt changed")
				}
				return handoffDone(), nil
			}}
			switch kind {
			case "append":
				svc.fail = true
			case "oversize":
				task.Instructions = strings.Repeat("x", DelegationHandoffMaxBytes)
			case "reserved":
				task.SessionState = map[string]any{"task_id": "forged"}
			case "custom":
				task.CustomPrompt = true
			case "legacy":
				task.Handoff = nil
			}
			result := m.RunTask(context.Background(), task)
			success := kind == "custom" || kind == "legacy"
			if success {
				if result.Status != "success" || calls != 1 {
					t.Fatalf("%+v calls=%d", result, calls)
				}
			} else if result.Status != "error" || calls != 0 {
				t.Fatalf("%+v calls=%d", result, calls)
			}
			if len(svc.records) != 0 {
				t.Fatal("unexpected record")
			}
		})
	}
	for _, key := range []string{DelegationHandoffStateKey, persistentsession.StateKeyParentID, "batch_id", "task_id", "child_session_id", "attempt"} {
		if !reservedDelegationStateKey(key) {
			t.Fatalf("unprotected %s", key)
		}
	}
}

func TestDelegationHandoffRetryPreservesSeedAndCheckpoint(t *testing.T) {
	m, svc, task := handoffFixture(t)
	// Use exactly the existing bounded retry checkpoint, not a second summary.
	checkpoint := childRetryCheckpoint(TaskResult{Result: "verified evidence", ToolCalls: 1}, nil)
	first := m.RunTask(context.Background(), task)
	if first.Status != "success" {
		t.Fatal(first)
	}
	task.Attempt, task.ChildSessionID = 2, "retry-child"
	task.Description += "\n\n" + checkpoint + "\nAdditional guidance: continue"
	result := m.RunTask(context.Background(), task)
	if result.Status != "success" || len(svc.records) != 2 {
		t.Fatalf("%+v records=%d", result, len(svc.records))
	}
	h1, h2 := decodeHandoff(t, svc.records[0]), decodeHandoff(t, svc.records[1])
	if h2.Attempt != 2 || h2.ChildSessionID == h1.ChildSessionID || h2.OriginalTask != h1.OriginalTask || h2.EffectiveInstructions != h1.EffectiveInstructions || !reflect.DeepEqual(h1.Plan, h2.Plan) || !strings.Contains(h2.EffectiveTask, checkpoint) {
		t.Fatalf("retry lost context: %+v", h2)
	}
}

func TestDelegationHandoffProviderRetryReusesRecord(t *testing.T) {
	m, svc, task := handoffFixture(t)
	m.Config.MaxRetries = 2
	calls := 0
	m.LLM = &handoffTestLLM{generate: func(_ context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
		calls++
		if len(svc.records) != 1 || strings.Count(handoffRequestText(req), svc.records[0]) != 1 {
			t.Fatal("provider retry changed handoff")
		}
		if calls == 1 {
			return nil, context.DeadlineExceeded
		}
		return handoffDone(), nil
	}}
	result := m.RunTask(context.Background(), task)
	if result.Status != "success" || calls != 2 || len(svc.records) != 1 {
		t.Fatalf("%+v calls=%d", result, calls)
	}
}

func TestDelegationHandoffOuterRetryUsesFrozenPhase(t *testing.T) {
	m, svc, task := handoffFixture(t)
	m.Config.MaxRetries = 1
	plan := NewPlanState("old goal", PlanDocumentInfo{PlanID: "p", Revision: 1, Sequence: 1}, []PlanStepInfo{{Name: "phase", Details: "old phase"}})
	ctx := WithPlanRuntime(context.Background(), NewPlanRuntime(plan, PlanRuntimeOptions{}))
	var err error
	task.Handoff, err = CaptureDelegationPlan(ctx).ForTask(task.Description, task.Instructions, "phase")
	if err != nil {
		t.Fatal(err)
	}
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "read"}, func(tool.Context, struct{}) (map[string]any, error) {
		return map[string]any{"result": "verified file content"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.ToolGroups = map[string]*ToolGroup{"core": {Name: "core", Tools: []tool.Tool{read}}}
	task.ToolFilter = []string{"core"}
	calls := 0
	m.LLM = &handoffTestLLM{generate: func(_ context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
		calls++
		switch calls {
		case 1:
			return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "read-call", Name: "read_file", Args: map[string]any{}}}}}}, nil
		case 2:
			return nil, context.DeadlineExceeded
		case 3: // Existing evaluator, not a child request.
			plan.mu.Lock()
			plan.goal = "new goal"
			plan.doc.Revision++
			plan.steps[0].details = "new phase"
			plan.mu.Unlock()
			return &model.LLMResponse{Content: genai.NewContentFromText("ACTION: continue\nREASON: progress\nGUIDANCE: keep verified work", "model")}, nil
		default:
			if len(svc.records) != 2 {
				t.Fatal("missing attempt record")
			}
			h := decodeHandoff(t, svc.records[1])
			if h.Attempt != 2 || h.Plan.Revision != 1 || h.Plan.Step.Details != "old phase" || h.EffectiveInstructions != task.Instructions || !strings.Contains(h.EffectiveTask, "verified file content") || !strings.Contains(h.EffectiveTask, "Additional guidance: keep verified work") {
				t.Fatalf("retry lost context %+v", h)
			}
			if strings.Count(handoffRequestText(req), svc.records[1]) != 1 {
				t.Fatal("retry provider handoff differs")
			}
			return handoffDone(), nil
		}
	}}
	result := m.RunTasks(ctx, []SubAgentTask{task})[0]
	if result.Status != "success" || result.Attempts != 2 || calls != 4 {
		t.Fatalf("%+v calls=%d trace=%+v", result, calls, result.Trace.Steps)
	}
}

func TestDelegationHandoffSurvivesCompactionWithoutParentPlan(t *testing.T) {
	m, svc, task := handoffFixture(t)
	path := filepath.Join(t.TempDir(), "PLAN.md")
	if err := os.WriteFile(path, []byte("MUTABLE_PARENT_PLAN_SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	m.Compactor = persistentsession.NewCompactor(200)
	m.Compactor.PreserveRecent = 1
	m.Compactor.PlanFilePath = path
	summaries := 0
	m.Compactor.LLM = func(context.Context, string) (string, error) { summaries++; return "compacted verified work", nil }
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "read"}, func(tool.Context, struct{}) (map[string]any, error) {
		return map[string]any{"result": strings.Repeat("evidence ", 1000)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.ToolGroups = map[string]*ToolGroup{"core": {Name: "core", Tools: []tool.Tool{read}}}
	task.ToolFilter = []string{"core"}
	calls := 0
	m.LLM = &handoffTestLLM{generate: func(_ context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
		calls++
		text := handoffRequestText(req)
		if strings.Contains(text, "MUTABLE_PARENT_PLAN_SENTINEL") || strings.Count(text, svc.records[0]) != 1 {
			t.Fatal("mutable plan injected or handoff lost")
		}
		if calls < 4 {
			return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "read-call", Name: "read_file", Args: map[string]any{}}}}}}, nil
		}
		return handoffDone(), nil
	}}
	result := m.RunTask(context.Background(), task)
	if result.Status != "success" || calls != 4 || summaries == 0 {
		t.Fatalf("result=%+v calls=%d summaries=%d", result, calls, summaries)
	}
	if m.Compactor.PlanFilePath != path {
		t.Fatal("parent compactor mutated")
	}
}
