package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/genai"
)

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
		toolContextStub: toolContextStub{sessionID: "parent-session"},
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
		toolContextStub: toolContextStub{sessionID: "parent-session"},
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
