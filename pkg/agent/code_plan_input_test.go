package agent

import (
	"context"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// Faithful wire payload of AskUserResult returned by askUser in
// pkg/docs/slides/tools.go:2053. In particular it has no awaiting-input flag.
func codePlanQuestionResult() map[string]any {
	return map[string]any{
		"status": "ok", "questionId": "q-123456789abc", "kind": "text",
		"prompt":  "Which account should I use?",
		"message": "Question shown to the user. Wait for their reply — the user's next message is their answer; do not proceed until they respond.",
	}
}

func codePlanInputEvent(parts ...*genai.Part) *session.Event {
	return &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: parts}}}
}

func TestCodePlanInputRequiredStructuredBoundaries(t *testing.T) {
	tests := []struct {
		name, tool string
		result     map[string]any
		want       bool
	}{
		{"question", "ask_user", codePlanQuestionResult(), true},
		{"qualified question", "mcp:questions/ask_user", codePlanQuestionResult(), true},
		{"question error", "ask_user", map[string]any{"error": "prompt required"}, false},
		{"empty question", "ask_user", map[string]any{}, false},
		{"nil question", "ask_user", nil, false},
		{"invalid question", "ask_user", map[string]any{"status": "ok", "questionId": "q-1", "kind": "text"}, false},
		{"question prose only", "ask_user", map[string]any{"message": "May I continue?"}, false},
		{"unrelated question shaped output", "read_file", codePlanQuestionResult(), false},
		{"handoff", "browser_request_human", map[string]any{"success": true, "listen_address": "127.0.0.1:9222"}, true},
		{"failed handoff", "browser_request_human", map[string]any{"success": false}, false},
		{"handoff error", "browser_request_human", map[string]any{"error": "launch failed"}, false},
		{"empty handoff", "browser_request_human", map[string]any{}, false},
		{"input flag", "execute_tool", map[string]any{"requires_user_input": true}, true},
		{"awaiting input", "execute_tool", map[string]any{"awaiting_user_input": true}, true},
		{"handoff flag", "execute_tool", map[string]any{"human_handoff": true}, true},
		{"approval flag", "execute_tool", map[string]any{"awaiting_approval": true}, true},
		{"approval status", "execute_tool", map[string]any{"status": "approval_required"}, true},
		{"pending authorization", "execute_tool", map[string]any{"status": "pending_authorization"}, true},
		{"false flag", "execute_tool", map[string]any{"requires_user_input": false}, false},
		{"string flag", "execute_tool", map[string]any{"requires_user_input": "true"}, false},
		{"failure overrides flag", "ask_user", map[string]any{"requires_user_input": true, "status": "failed"}, false},
		{"error overrides question", "ask_user", map[string]any{"status": "ok", "questionId": "q-1", "kind": "text", "prompt": "Which?", "error": "not delivered"}, false},
		{"routine result", "execute_tool", map[string]any{"status": "ok", "message": "Continue?"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := codePlanInputEvent(nil, &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: tt.tool, Response: tt.result}})
			if got := codePlanInputRequired(event, nil); got != tt.want {
				t.Fatalf("input required = %v, want %v", got, tt.want)
			}
		})
	}
	for _, event := range []*session.Event{nil, {}, codePlanInputEvent(nil), codePlanInputEvent(&genai.Part{Text: "Phase complete. Shall I continue?"})} {
		if codePlanInputRequired(event, nil) {
			t.Fatal("empty event or narration must not pause execution")
		}
	}
}

func TestCodePlanInputRequiredTracksDeferredCalls(t *testing.T) {
	for _, args := range []map[string]any{
		{"name": "ask_user", "arguments": map[string]any{}},
		{"tool_name": "ask_user", "arguments": map[string]any{}},
		{"name": "ask_user", "arguments": `{}`},
		{"name": "execute_tool", "arguments": map[string]any{"name": "ask_user", "arguments": map[string]any{}}},
		{"arguments": `{"name":"ask_user","arguments":{}}`},
		{"name": "mcp:questions/ask_user", "arguments": map[string]any{}},
	} {
		calls := map[string]string{}
		call := codePlanInputEvent(&genai.Part{FunctionCall: &genai.FunctionCall{ID: "q", Name: "execute_tool", Args: args}})
		if codePlanInputRequired(call, calls) {
			t.Fatal("request alone must not pause")
		}
		other := codePlanInputEvent(&genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "other", Name: "execute_tool", Response: codePlanQuestionResult()}})
		if codePlanInputRequired(other, calls) {
			t.Fatal("unmatched result inherited another call's identity")
		}
		result := codePlanInputEvent(&genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "q", Name: "execute_tool", Response: codePlanQuestionResult()}})
		if !codePlanInputRequired(result, calls) {
			t.Fatalf("deferred question did not pause: args=%v", args)
		}
		if len(calls) != 0 {
			t.Fatalf("completed calls retained: %v", calls)
		}
	}
}

func TestCodePlanInputRequiredBatchedAndFailedDeferredResults(t *testing.T) {
	calls := map[string]string{}
	call := &genai.Part{FunctionCall: &genai.FunctionCall{ID: "q", Name: "execute_tool", Args: map[string]any{"name": "ask_user", "arguments": map[string]any{}}}}
	failed := &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "q", Name: "execute_tool", Response: map[string]any{"error": "prompt required"}}}
	if codePlanInputRequired(codePlanInputEvent(call, failed), calls) || len(calls) != 0 {
		t.Fatalf("failed deferred question paused or retained call: %v", calls)
	}
	result := &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "q", Name: "execute_tool", Response: codePlanQuestionResult()}}
	if !codePlanInputRequired(codePlanInputEvent(call, result), calls) {
		t.Fatal("batched question did not pause")
	}
	invalid := &genai.Part{FunctionCall: &genai.FunctionCall{ID: "q", Name: "execute_tool", Args: map[string]any{"name": "ask_user"}}}
	if codePlanInputRequired(codePlanInputEvent(invalid, result), calls) {
		t.Fatal("invalid dispatcher envelope must not establish logical tool identity")
	}
}

func TestCodePlanInputRequiredProgressiveDispatcher(t *testing.T) {
	// Exercise the real bridge and functiontool wire conversion with the exact
	// production question shape, not a fabricated awaiting_user_input flag.
	inner, err := functiontool.New(functiontool.Config{Name: "ask_user", Description: "Ask a question"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
		return codePlanQuestionResult(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewSubAgentManager(SubAgentConfig{})
	mgr.ToolGroups = map[string]*ToolGroup{"core": {Name: "core", Tools: []tool.Tool{inner}}}
	scope, _, err := mgr.childToolScope(context.Background(), SubAgentTask{ToolFilter: []string{"core"}, ToolSelectionMode: ChildToolSelectionPreferred})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewScopedProgressiveToolBridge(scope)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": "ask_user", "arguments": map[string]any{}}
	calls := map[string]string{}
	codePlanInputRequired(codePlanInputEvent(&genai.Part{FunctionCall: &genai.FunctionCall{ID: "question", Name: "execute_tool", Args: args}}), calls)
	ctx := &contextToolContext{minimalReadonlyContext{Context: context.Background()}}
	output, err := bridge[1].(runnableDeferredTool).Run(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if !codePlanInputRequired(codePlanInputEvent(&genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "question", Name: "execute_tool", Response: output}}), calls) {
		t.Fatalf("real dispatcher output not recognized as input boundary: %v", output)
	}
}
