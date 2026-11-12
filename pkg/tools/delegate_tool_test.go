package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type publicHandoffLLM struct {
	check func(*model.LLMRequest)
	calls int
}

func (*publicHandoffLLM) Name() string { return "public-handoff" }
func (m *publicHandoffLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.check != nil {
			m.check(req)
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("done", "model")}, nil)
	}
}

func TestDelegateTasksScopedHandoff(t *testing.T) {
	for _, mode := range []string{"matched", "invalid", "no-step", "unbound", "bound-nil"} {
		t.Run(mode, func(t *testing.T) {
			manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
			manager.SessionService = adksession.InMemoryService()
			manager.AppName, manager.UserID = "handoff", "user"
			llm := &publicHandoffLLM{}
			manager.LLM = llm
			plan := agent.NewPlanRuntimeFromSnapshot(agent.PlanSnapshot{PlanID: "plan", Revision: 3, Sequence: 8, Goal: "goal", Document: agent.PlanDocumentInfo{Context: "root causes", WhatNotToDo: "no API", Verification: "acceptance"}, Steps: []agent.PlanStepInfo{{Name: "phase", Details: "implement full phase", Verify: "go test", VerifyKind: "unit"}}}, agent.PlanRuntimeOptions{})
			ctx := context.Background()
			if mode != "unbound" {
				ctx = agent.WithPlanRuntime(ctx, plan)
			}
			if mode == "bound-nil" {
				ctx = agent.WithPlanRuntime(ctx, nil)
			}
			step := "phase"
			if mode == "invalid" {
				step = "typo"
			}
			if mode == "no-step" {
				step = ""
			}
			llm.check = func(req *model.LLMRequest) {
				var text strings.Builder
				for _, part := range req.Config.SystemInstruction.Parts {
					text.WriteString(part.Text)
				}
				prompt := text.String()
				if !strings.Contains(prompt, "Immutable Delegation Handoff") || !strings.Contains(prompt, "prior finding exact path") {
					t.Fatal("missing handoff or findings")
				}
				want := string(agent.DelegationPlanMissing)
				if mode == "matched" {
					want = string(agent.DelegationPlanMatched)
					for _, value := range []string{"implement full phase", "root causes", "no API", "acceptance"} {
						if !strings.Contains(prompt, value) {
							t.Fatalf("missing %s", value)
						}
					}
				}
				if mode == "no-step" {
					want = string(agent.DelegationPlanStepMissing)
				}
				if !strings.Contains(prompt, `"status": "`+want+`"`) {
					t.Fatalf("missing status %s", want)
				}
			}
			inputs := []SubTaskInput{{Name: "worker", Task: "work", Instructions: "prior finding exact path", PlanStep: step}}
			if mode == "invalid" {
				// A late invalid step must prevent dispatch of earlier valid tasks too.
				inputs = append([]SubTaskInput{{Name: "valid", Task: "work", PlanStep: "phase"}}, inputs...)
			}
			result, err := delegateTasksWithManager(&toolContextStub{Context: ctx, sessionID: "parent"}, DelegateTasksArgs{Tasks: inputs}, manager)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "invalid" {
				if result.Status != "error" || llm.calls != 0 || !strings.Contains(result.Summary, "available steps: phase") {
					t.Fatalf("%+v calls=%d", result, llm.calls)
				}
				return
			}
			if result.Status != "success" || llm.calls != 1 {
				t.Fatalf("%+v calls=%d", result, llm.calls)
			}
			child, err := manager.SessionService.Get(ctx, &adksession.GetRequest{AppName: manager.AppName, UserID: manager.UserID, SessionID: result.Results[0].ChildSessionID})
			if err != nil {
				t.Fatal(err)
			}
			value, err := child.Session.State().Get(agent.DelegationHandoffStateKey)
			if err != nil {
				t.Fatal(err)
			}
			var h agent.DelegationHandoff
			if err := json.Unmarshal([]byte(value.(string)), &h); err != nil {
				t.Fatal(err)
			}
			if h.ParentSessionID != "parent" || h.BatchID != result.BatchID || h.TaskID != result.Results[0].TaskID {
				t.Fatalf("identity %+v", h)
			}
		})
	}
}

func TestDelegateTasks_NilManager(t *testing.T) {
	// Ensure the manager is nil
	old := subAgentManagerVar
	subAgentManagerVar = nil
	defer func() { subAgentManagerVar = old }()

	result, err := delegateTasks(nil, DelegateTasksArgs{
		Tasks: []SubTaskInput{{Name: "test", Task: "do something"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "error" {
		t.Errorf("Status = %q, want 'error'", result.Status)
	}
	if result.Summary != "Sub-agent system is not available" {
		t.Errorf("Summary = %q, want 'Sub-agent system is not available'", result.Summary)
	}
}

func TestDelegateToolPreferencesDecode(t *testing.T) {
	for _, payload := range []string{`{"tasks":[{"name":"worker","task":"work"}]}`, `{"tasks":[{"name":"worker","task":"work","tools":null}]}`, `{"tasks":[{"name":"worker","task":"work","tools":[]}]}`} {
		var args DelegateTasksArgs
		if err := json.Unmarshal([]byte(payload), &args); err != nil {
			t.Fatal(err)
		}
		if len(args.Tasks[0].Tools) != 0 {
			t.Fatal("expected auto-selection")
		}
	}
}

// delegationPreferenceLLM exercises the real child runner, including discovery
// responses, without a provider or network dependency.
type delegationPreferenceLLM struct {
	step         int
	declarations []string
	disabled     bool
}

func (*delegationPreferenceLLM) Name() string { return "delegation-preference-contract" }
func (m *delegationPreferenceLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var names []string
		for _, group := range req.Config.Tools {
			for _, declaration := range group.FunctionDeclarations {
				names = append(names, declaration.Name)
			}
		}
		sort.Strings(names)
		for i, name := range names {
			if i > 0 && names[i-1] == name {
				yield(nil, fmt.Errorf("duplicate declaration %s", name))
				return
			}
		}
		for _, helper := range []string{"search_tools", "describe_tools", "execute_tool"} {
			if !containsDelegationDeclaration(names, helper) {
				yield(nil, fmt.Errorf("missing helper %s: %v", helper, names))
				return
			}
		}
		if m.step == 0 {
			m.declarations = names
		} else if !reflect.DeepEqual(names, m.declarations) {
			yield(nil, fmt.Errorf("declarations changed: %v -> %v", m.declarations, names))
			return
		}
		if m.step > 0 {
			var response *genai.FunctionResponse
			for _, content := range req.Contents {
				for _, part := range content.Parts {
					if part.FunctionResponse != nil {
						response = part.FunctionResponse
					}
				}
			}
			if response == nil {
				yield(nil, fmt.Errorf("missing function response at step %d", m.step))
				return
			}
			wantResponse := []string{"", "search_tools", "describe_tools", "execute_tool"}[m.step]
			if response.Name != wantResponse {
				yield(nil, fmt.Errorf("response = %s, want %s", response.Name, wantResponse))
				return
			}
			data, _ := json.Marshal(response.Response)
			text := string(data)
			if m.step < 3 && !strings.Contains(text, "browser_navigate") {
				yield(nil, fmt.Errorf("additional permitted tool absent from %s: %s", response.Name, text))
				return
			}
			if m.disabled && m.step < 3 && strings.Contains(text, "read_file") {
				yield(nil, fmt.Errorf("disabled preference leaked: %s", text))
				return
			}
			if m.step == 3 && !strings.Contains(text, "navigation-complete") {
				yield(nil, fmt.Errorf("additional tool did not execute: %s", text))
				return
			}
		}
		var part *genai.Part
		switch m.step {
		case 0:
			part = &genai.Part{FunctionCall: &genai.FunctionCall{Name: "search_tools", Args: map[string]any{"query": "*"}}}
		case 1:
			part = &genai.Part{FunctionCall: &genai.FunctionCall{Name: "describe_tools", Args: map[string]any{"names": []string{"browser_navigate"}}}}
		case 2:
			part = &genai.Part{FunctionCall: &genai.FunctionCall{Name: "execute_tool", Args: map[string]any{"name": "browser_navigate", "arguments": map[string]any{}}}}
		default:
			part = &genai.Part{Text: "additional permitted tool executed"}
		}
		m.step++
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, nil)
	}
}

func containsDelegationDeclaration(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func TestDelegateTasksPreferredToolsContract(t *testing.T) {
	for _, tc := range []struct {
		name, tools string
		disabled    bool
	}{
		{name: "group", tools: `,"tools":["core"]`},
		{name: "bare", tools: `,"tools":["read_file"]`},
		{name: "qualified", tools: `,"tools":["core/read_file"]`},
		{name: "null", tools: `,"tools":null`},
		{name: "omitted"},
		{name: "empty", tools: `,"tools":[]`},
		{name: "invalid-fallback", tools: `,"tools":["unknown-tool"]`},
		{name: "disabled-fallback", tools: `,"tools":["read_file"]`, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var readCalls, browserCalls atomic.Int32
			read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read a file"}, func(tool.Context, struct{}) (map[string]any, error) {
				readCalls.Add(1)
				return map[string]any{"result": "read-complete"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			browser, err := functiontool.New(functiontool.Config{Name: "browser_navigate", Description: "Navigate a browser"}, func(tool.Context, struct{}) (map[string]any, error) {
				browserCalls.Add(1)
				return map[string]any{"result": "navigation-complete"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			llm := &delegationPreferenceLLM{disabled: tc.disabled}
			manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
			manager.LLM = llm
			manager.AppName, manager.UserID = "delegation-contract", "test-user"
			manager.SessionService = common.NewAutoInitService(adksession.InMemoryService())
			manager.SearchToolsFactory = NewScopedSearchToolsTool
			manager.ToolGroups = map[string]*agent.ToolGroup{
				"core":    {Name: "core", Tools: []tool.Tool{read, read}},
				"browser": {Name: "browser", Tools: []tool.Tool{browser}},
			}
			bound, err := NewDelegateTasksToolForManager(manager)
			if err != nil {
				t.Fatal(err)
			}
			var input map[string]any
			if err := json.Unmarshal([]byte(`{"tasks":[{"name":"worker","task":"work"`+tc.tools+`}]}`), &input); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if tc.disabled {
				ctx = store.WithDisabledTools(ctx, []string{"read_file"})
			}
			out, err := bound.(interface {
				Run(tool.Context, any) (map[string]any, error)
			}).Run(&toolContextStub{Context: ctx, sessionID: "parent-session"}, input)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var result DelegateTasksResult
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != "success" || len(result.Results) != 1 {
				t.Fatalf("delegation failed: %s", data)
			}
			child := result.Results[0]
			if child.Status != "success" || !strings.HasSuffix(child.Result, "additional permitted tool executed") || child.ToolCalls != 3 || child.Attempts != 1 {
				t.Fatalf("child contract mismatch: %+v", child)
			}
			if browserCalls.Load() != 1 || readCalls.Load() != 0 || llm.step != 4 {
				t.Fatalf("execution counters: browser=%d read=%d model=%d", browserCalls.Load(), readCalls.Load(), llm.step)
			}
		})
	}
}

func TestDelegateTasks_EmptyTasks(t *testing.T) {
	for _, tasks := range [][]SubTaskInput{nil, {}} {
		assertDelegateTasksValidation(t, tasks, "No tasks provided")
	}
}

func TestDelegateTasks_TooManyTasks(t *testing.T) {
	tasks := make([]SubTaskInput, 11)
	for i := range tasks {
		tasks[i] = SubTaskInput{Name: "task", Task: "do something"}
	}
	assertDelegateTasksValidation(t, tasks, "Too many tasks (max 10 per delegation call)")
}

type delegationToolContext struct {
	toolContextStub
	callID string
}

func (c *delegationToolContext) FunctionCallID() string { return c.callID }

func assertDelegateTasksValidation(t *testing.T, tasks []SubTaskInput, summary string) {
	t.Helper()
	manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
	var events []agent.SubTaskProgressEvent
	manager.SubTaskProgress = func(event agent.SubTaskProgressEvent) {
		events = append(events, event)
	}
	ctx := &delegationToolContext{
		toolContextStub: toolContextStub{Context: context.Background(), sessionID: "parent-session"},
		callID:          "validation-call",
	}
	result, err := newDelegateTasksRunner(manager)(ctx, DelegateTasksArgs{Tasks: tasks})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "error" || result.Summary != summary || len(result.Results) != 0 {
		t.Fatalf("validation result = %+v, want error %q with no children", result, summary)
	}
	if result.BatchID != ctx.callID {
		t.Fatalf("batch ID = %q, want %q", result.BatchID, ctx.callID)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one completion", events)
	}
	event := events[0]
	if event.Type != string(agent.DelegationComplete) || event.Status != "error" || event.Error != summary ||
		event.BatchID != result.BatchID || event.SessionID != ctx.sessionID || event.Sequence != 1 {
		t.Fatalf("completion = %+v, want correlated validation error", event)
	}
}

func TestDelegateTasks_PanicUnwindCompletesWithError(t *testing.T) {
	manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
	var events []agent.SubTaskProgressEvent
	panicValue := &struct{ message string }{"progress callback panic"}
	manager.SubTaskProgress = func(event agent.SubTaskProgressEvent) {
		events = append(events, event)
		if event.Type == string(agent.DelegationStart) {
			panic(panicValue)
		}
	}
	ctx := &delegationToolContext{
		toolContextStub: toolContextStub{Context: context.Background(), sessionID: "parent-session"},
		callID:          "panic-call",
	}
	func() {
		defer func() {
			if got := recover(); got != panicValue {
				t.Fatalf("panic = %v, want original sentinel %v", got, panicValue)
			}
		}()
		_, _ = newDelegateTasksRunner(manager)(ctx, DelegateTasksArgs{
			Tasks: []SubTaskInput{{Name: "test", Task: "do something"}},
		})
	}()
	if len(events) != 2 {
		t.Fatalf("events = %+v, want start and completion", events)
	}
	complete := events[1]
	if complete.Type != string(agent.DelegationComplete) || complete.Status != "error" || complete.Error == "" ||
		complete.BatchID != ctx.callID || complete.SessionID != ctx.sessionID || complete.Sequence != 2 {
		t.Fatalf("completion = %+v, want correlated panic-unwind error", complete)
	}
}

func TestDelegateTasksBoundManagerSurvivesGlobalReplacement(t *testing.T) {
	first := agent.NewSubAgentManager(agent.SubAgentConfig{})
	second := agent.NewSubAgentManager(agent.SubAgentConfig{})

	firstEvents := 0
	secondEvents := 0
	first.SubTaskProgress = func(agent.SubTaskProgressEvent) { firstEvents++ }
	second.SubTaskProgress = func(agent.SubTaskProgressEvent) { secondEvents++ }

	bound, err := NewDelegateTasksToolForManager(first)
	if err != nil {
		t.Fatal(err)
	}
	old := subAgentManagerVar
	SetSubAgentManager(second)
	t.Cleanup(func() { SetSubAgentManager(old) })

	declared, ok := bound.(interface {
		Declaration() *genai.FunctionDeclaration
	})
	if !ok || declared.Declaration() == nil {
		t.Fatalf("bound delegate tool type %T is missing its declaration", bound)
	}
	runFirst := newDelegateTasksRunner(first)
	_, _ = runFirst(nil, DelegateTasksArgs{})

	if firstEvents == 0 {
		t.Fatal("manager-bound delegate tool did not emit through its own manager")
	}
	if secondEvents != 0 {
		t.Fatalf("manager-bound delegate tool leaked to replacement global manager: %d events", secondEvents)
	}
}

func TestNewDelegateTasksTool(t *testing.T) {
	got, err := NewDelegateTasksTool()
	if err != nil {
		t.Fatalf("NewDelegateTasksTool() error = %v", err)
	}
	if got.Name() != "delegate_tasks" {
		t.Errorf("Name() = %q, want 'delegate_tasks'", got.Name())
	}
	if got.Description() == "" {
		t.Error("Description() is empty")
	}

	declared, ok := got.(interface {
		Declaration() *genai.FunctionDeclaration
	})
	if !ok {
		t.Fatal("delegate_tasks does not expose its provider declaration")
	}
	declaration := declared.Declaration()
	if declaration == nil || declaration.ParametersJsonSchema == nil {
		t.Fatal("delegate_tasks declaration is missing its input schema")
	}
	schema, err := json.Marshal(declaration.ParametersJsonSchema)
	if err != nil {
		t.Fatalf("marshal delegate_tasks schema: %v", err)
	}
	var inputSchema struct {
		Properties map[string]struct {
			Items struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &inputSchema); err != nil {
		t.Fatal(err)
	}
	toolsDescription := inputSchema.Properties["tasks"].Items.Properties["tools"].Description
	for _, phrase := range []string{"preferred", "not an allow-list", "additional authorized tools", "Omitted, null or empty"} {
		if !strings.Contains(toolsDescription, phrase) {
			t.Errorf("tools schema description %q missing %q", toolsDescription, phrase)
		}
	}
	for _, phrase := range []string{"preferences", "not an allow-list", "discover, describe, and execute", "parent's permissions and child restrictions", "Omitted, null, or empty"} {
		if !strings.Contains(got.Description(), phrase) {
			t.Errorf("delegate description missing %q", phrase)
		}
	}
	for _, required := range []string{`"tasks"`, `"name"`, `"task"`} {
		if !strings.Contains(string(schema), required) {
			t.Errorf("delegate_tasks schema %s does not contain %s", schema, required)
		}
	}
	for _, unsupported := range []string{`"expected_output"`, `"output_format"`} {
		if strings.Contains(string(schema), unsupported) {
			t.Errorf("delegate_tasks schema unexpectedly contains unsupported field %s: %s", unsupported, schema)
		}
	}
}
