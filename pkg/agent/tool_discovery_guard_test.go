package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func TestToolDiscoveryGuardStopsRepeatedIdenticalCalls(t *testing.T) {
	guard := newToolDiscoveryGuard()
	args := map[string]any{"query": "cinematic documentary"}

	for i := 0; i < maxIdenticalDiscoveryCalls; i++ {
		if err := guard.record("search_tools", args); err != nil {
			t.Fatalf("call %d unexpectedly blocked: %v", i+1, err)
		}
	}

	err := guard.record("search_tools", args)
	if !errors.Is(err, errToolDiscoveryLoop) {
		t.Fatalf("record error = %v, want errToolDiscoveryLoop", err)
	}
}

func TestToolDiscoveryGuardStopsNonProgressingDiscoveryCycle(t *testing.T) {
	guard := newToolDiscoveryGuard()
	cycle := []struct {
		name string
		args map[string]any
	}{
		{name: "search_tools", args: map[string]any{"query": "cinematic documentary"}},
		{name: "describe_tools", args: map[string]any{"names": []any{"skill_lookup"}}},
		{name: "skill_lookup", args: map[string]any{"name": "cinematic-documentary"}},
	}

	for i := 0; i < maxDiscoveryCallsPerTurn; i++ {
		call := cycle[i%len(cycle)]
		// Vary an irrelevant argument so this exercises the total non-progress
		// budget rather than the identical-call limit.
		args := make(map[string]any, len(call.args)+1)
		for key, value := range call.args {
			args[key] = value
		}
		args["attempt"] = i
		if err := guard.record(call.name, args); err != nil {
			t.Fatalf("call %d unexpectedly blocked: %v", i+1, err)
		}
	}

	err := guard.record("search_tools", map[string]any{"query": "one more", "attempt": maxDiscoveryCallsPerTurn})
	if !errors.Is(err, errToolDiscoveryLoop) {
		t.Fatalf("record error = %v, want errToolDiscoveryLoop", err)
	}
}

func TestToolDiscoveryGuardValidatesMixedHistoryAndExecuteTool(t *testing.T) {
	guard := newToolDiscoveryGuard()
	if err := guard.validateHistory(nil); err != nil {
		t.Fatalf("initialize history: %v", err)
	}

	var contents []*genai.Content
	for i := 0; i < maxDiscoveryCallsPerTurn; i++ {
		name := []string{"search_tools", "describe_tools", "skill_lookup"}[i%3]
		args := map[string]any{"attempt": i}
		if name == "skill_lookup" {
			// Verify wrapped deferred tools count by their underlying identity.
			args = map[string]any{"name": name, "arguments": args}
			name = executeToolName
		}
		contents = append(contents, &genai.Content{Role: "model", Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("call-%d", i), Name: name, Args: args},
		}}})
	}

	err := guard.validateHistory(contents)
	if !errors.Is(err, errToolDiscoveryLoop) {
		t.Fatalf("validateHistory error = %v, want errToolDiscoveryLoop", err)
	}
}

func TestToolDiscoveryGuardAllowsDirectFailureThenDeferredSuccess(t *testing.T) {
	guard := newToolDiscoveryGuard()
	if err := guard.validateHistory(nil); err != nil {
		t.Fatalf("initialize history: %v", err)
	}

	args := map[string]any{"name": "cinematic-documentary"}
	contents := []*genai.Content{
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "direct", Name: "skill_lookup", Args: args}}}},
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "deferred", Name: executeToolName, Args: map[string]any{"name": "skill_lookup", "arguments": args}}}}},
	}
	if err := guard.validateHistory(contents); err != nil {
		t.Fatalf("successful deferred retry after unavailable direct tool was blocked: %v", err)
	}
}

func TestToolDiscoveryGuardDoesNotRecountFailedPriorTurn(t *testing.T) {
	guard := newToolDiscoveryGuard()
	priorTurn := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "create a large documentary"}}},
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "old-1", Name: "search_tools", Args: map[string]any{"query": "cinematic documentary"}}}}},
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "old-2", Name: "search_tools", Args: map[string]any{"query": "cinematic documentary"}}}}},
		{Role: "user", Parts: []*genai.Part{{Text: "continue"}}},
	}
	if err := guard.validateHistory(priorTurn); err != nil {
		t.Fatalf("baseline prior failed turn: %v", err)
	}

	current := append(append([]*genai.Content{}, priorTurn...),
		&genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "new-1", Name: "read_file", Args: map[string]any{"path": "report.md"}}}}},
	)
	if err := guard.validateHistory(current); err != nil {
		t.Fatalf("productive recovery after prior failed turn was blocked: %v", err)
	}
}

func TestToolDiscoveryGuardIgnoresProductiveTools(t *testing.T) {
	guard := newToolDiscoveryGuard()
	for i := 0; i < maxDiscoveryCallsPerTurn*2; i++ {
		if err := guard.record("read_file", map[string]any{"path": "file.go"}); err != nil {
			t.Fatalf("productive tool call %d blocked: %v", i+1, err)
		}
	}
}

func TestChatAgentRunStopsRepeatedDiscoveryLoop(t *testing.T) {
	service := session.InMemoryService()
	var executions atomic.Int32
	discovery, err := functiontool.New(
		functiontool.Config{Name: "search_tools", Description: "find tools"},
		func(_ tool.Context, _ struct {
			Query string `json:"query"`
		}) (map[string]any, error) {
			executions.Add(1)
			return map[string]any{"count": 0, "message": "No matching tools found"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	llm := &memoryCaptureModel{respond: func(_ context.Context, i int, _ *model.LLMRequest) (*model.LLMResponse, error) {
		if i > 10 {
			return nil, fmt.Errorf("test safety cap: executions=%d", executions.Load())
		}
		return &model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
				ID:   fmt.Sprintf("call-%d", i),
				Name: "search_tools",
				Args: map[string]any{"query": "cinematic documentary"},
			}}}},
			TurnComplete: true,
		}, nil
	}}
	chat := NewChatAgent(llm, []tool.Tool{discovery}, nil, service, &SystemPromptBuilder{CustomPrompt: "CORE"}, false, false)
	runner := memoryTestRunner(t, chat, service)

	var runErr error
	for _, err := range runner.Run(context.Background(), "u", "loop", genai.NewContentFromText("continue", genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			runErr = err
			break
		}
	}
	if !errors.Is(runErr, errToolDiscoveryLoop) && (runErr == nil || !strings.Contains(runErr.Error(), errToolDiscoveryLoop.Error())) {
		t.Fatalf("run error = %v, want tool discovery loop error", runErr)
	}
	if got := executions.Load(); got != maxIdenticalDiscoveryCalls {
		t.Fatalf("tool executions = %d, want %d before hard stop", got, maxIdenticalDiscoveryCalls)
	}
	if got := len(llm.requests); got != maxIdenticalDiscoveryCalls {
		t.Fatalf("provider calls = %d, want %d; the next dispatch must be blocked", got, maxIdenticalDiscoveryCalls)
	}
}
