package api

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/docs/slides"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

func TestUIToolTrackerUnwrapsExecuteToolByCallID(t *testing.T) {
	tracker := newUIToolTracker()
	call := &genai.FunctionCall{
		ID:   "call-1",
		Name: "execute_tool",
		Args: map[string]any{
			"name":      "ask_user",
			"arguments": map[string]any{"prompt": "Continue?"},
		},
	}

	identity := tracker.call(call)
	if identity.name != "ask_user" || identity.args["prompt"] != "Continue?" {
		t.Fatalf("unexpected call identity: %#v", identity)
	}
	if got := tracker.response(&genai.FunctionResponse{ID: "call-1", Name: "execute_tool"}); got != "ask_user" {
		t.Fatalf("response name = %q, want ask_user", got)
	}
}

func TestUIToolTrackerFallsBackToCallOrderWhenResponseIDIsMissing(t *testing.T) {
	tracker := newUIToolTracker()
	tracker.call(&genai.FunctionCall{ID: "call-1", Name: "execute_tool", Args: map[string]any{
		"name": "ask_user", "arguments": map[string]any{},
	}})
	if got := tracker.response(&genai.FunctionResponse{Name: "execute_tool"}); got != "ask_user" {
		t.Fatalf("response name = %q, want ask_user", got)
	}
}

func TestIntegrationExecuteToolAskUserEmitsQuestionComponentEvent(t *testing.T) {
	slideTools, err := slides.GetTools()
	if err != nil {
		t.Fatalf("create slides tools: %v", err)
	}
	var askUser tool.Tool
	for _, candidate := range slideTools {
		if candidate.Name() == "ask_user" {
			askUser = candidate
			break
		}
	}
	if askUser == nil {
		t.Fatal("ask_user tool not found")
	}
	index := agent.NewLexicalToolIndex()
	index.PrimeTools(context.Background(), []tool.Tool{askUser}, nil)
	bridge, err := agent.NewProgressiveToolBridge(index)
	if err != nil {
		t.Fatalf("create progressive bridge: %v", err)
	}
	mockLLM := NewMockLLM(ToolCallTurn("execute_tool", map[string]any{
		"name": "ask_user",
		"arguments": map[string]any{
			"kind":   "select",
			"prompt": "Who is this deck for?",
			"options": []any{
				map[string]any{"id": "students", "label": "Students"},
				map[string]any{"id": "other", "label": "Other"},
			},
		},
	}))
	env := setupIntegrationTest(t, mockLLM, bridge)
	events := runAndCollect(t, env, "Create a deck")

	toolCall := assertHasEvent(t, events, "tool_call")
	assertEventData(t, toolCall, "name", "ask_user")
	args, ok := toolCall.Data["args"].(map[string]any)
	if !ok || args["prompt"] != "Who is this deck for?" {
		t.Fatalf("tool_call args = %#v", toolCall.Data["args"])
	}
	toolResult := assertHasEvent(t, events, "tool_result")
	assertEventData(t, toolResult, "name", "ask_user")
	if len(eventsOfType(events, "chat_question")) == 0 {
		t.Fatalf("chat_question missing from events: %#v", events)
	}
	question := assertHasEvent(t, events, "chat_question")
	if question.Data["questionId"] == "" {
		t.Fatal("chat_question questionId is empty")
	}
	assertEventData(t, question, "prompt", "Who is this deck for?")
}
