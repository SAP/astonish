package api

import (
	"context"
	"encoding/json"
	"google.golang.org/adk/session"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/docs/slides"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

func TestAskUserQuestionRoundTrip(t *testing.T) {
	for _, raw := range []string{`{"questionId":"old","kind":"yesno","prompt":"Continue?"}`, `{"questionId":"text","kind":"text","prompt":"Details?"}`, `{"questionId":"custom","kind":"select","prompt":"Choose","options":[{"id":"x","label":"X"}],"allow_custom_answer":true}`, `{"kind":"select","prompt":"Choose","options":[{"id":"x","label":"X"}],"allow_custom_answer":false}`} {
		var payload chatQuestionPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(payload)
		message := tryParseChatQuestionMessage(chatQuestionPrefix + string(encoded))
		if message == nil || message.QuestionID != payload.QuestionID || message.QuestionKind != payload.Kind || !reflect.DeepEqual(message.AllowCustomAnswer, payload.AllowCustomAnswer) {
			t.Fatalf("lost question: %#v", message)
		}
	}
}

func TestUnwrapUIToolCallUsesBoundedEnvelope(t *testing.T) {
	fileArgs := map[string]any{"path": "/tmp/report.md"}
	valid := map[string]any{"name": "load_file", "arguments": fileArgs}
	for _, tt := range []struct {
		name  string
		args  map[string]any
		valid bool
	}{
		{"object", valid, true},
		{"string_arguments", map[string]any{"name": "load_file", "arguments": `{"path":"/tmp/report.md"}`}, true},
		{"outer_arguments", map[string]any{"arguments": valid}, true},
		{"self_wrapper", map[string]any{"name": "execute_tool", "arguments": valid}, true},
		{"self_wrapper_string", map[string]any{"name": "execute_tool", "arguments": `{"name":"load_file","arguments":"{\"path\":\"/tmp/report.md\"}"}`}, true},
		{"missing_arguments", map[string]any{"name": "load_file"}, false},
		{"malformed_arguments", map[string]any{"name": "write_file", "arguments": "not JSON"}, false},
		{"array_arguments", map[string]any{"name": "write_file", "arguments": `[]`}, false},
		{"discovery", map[string]any{"name": "search_tools", "arguments": fileArgs}, false},
		{"recursive", map[string]any{"name": "execute_tool", "arguments": map[string]any{"name": "execute_tool", "arguments": valid}}, false},
		{"nil", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := unwrapUIToolCall("execute_tool", tt.args)
			wantName, wantArgs := "execute_tool", tt.args
			if tt.valid {
				wantName, wantArgs = "load_file", fileArgs
			}
			if got.name != wantName || !reflect.DeepEqual(got.args, wantArgs) {
				t.Fatalf("identity = %#v, want %s %#v", got, wantName, wantArgs)
			}
		})
	}
	got := unwrapUIToolCall("custom_tool", valid)
	if got.name != "custom_tool" || !reflect.DeepEqual(got.args, valid) {
		t.Fatalf("non-dispatcher call was unwrapped: %#v", got)
	}
}

func TestUIToolTrackerUnknownIDUsesOnlyUnambiguousPendingCall(t *testing.T) {
	tracker := newUIToolTracker()
	tracker.call(&genai.FunctionCall{ID: "known", Name: "execute_tool", Args: map[string]any{"name": "shell_command", "arguments": map[string]any{}}})
	if got := tracker.response(&genai.FunctionResponse{ID: "provider-remapped", Name: "execute_tool"}); got != "shell_command" {
		t.Fatalf("single pending call remained wrapped: %s", got)
	}
}

func TestUIToolTrackerUnknownIDDoesNotConsumeAmbiguousPendingCalls(t *testing.T) {
	tracker := newUIToolTracker()
	tracker.call(&genai.FunctionCall{ID: "first", Name: "execute_tool", Args: map[string]any{"name": "read_file", "arguments": map[string]any{}}})
	tracker.call(&genai.FunctionCall{ID: "second", Name: "execute_tool", Args: map[string]any{"name": "shell_command", "arguments": map[string]any{}}})
	if got := tracker.response(&genai.FunctionResponse{ID: "unknown", Name: "execute_tool"}); got != "execute_tool" {
		t.Fatalf("unknown ID stole an ambiguous pending call: %s", got)
	}
	if got := tracker.response(&genai.FunctionResponse{ID: "first", Name: "execute_tool"}); got != "read_file" {
		t.Fatal(got)
	}
}

func TestDelegationHistoryIdentityAndUnknownStatus(t *testing.T) {
	for _, batch := range []string{"batch-a", "batch-b"} {
		msg := buildSubTaskExecutionMessage(&pendingDelegation{batchID: batch, tasks: []SubTaskInfoMsg{{Name: "worker"}}}, map[string]any{"status": "success", "results": []any{map[string]any{"name": "worker", "task_id": batch + "-task", "child_session_id": batch + "-child", "status": "success", "attempts": float64(2)}}})
		if msg.Tasks[0].TaskID != batch+"-task" || msg.Events[1].ChildSessionID != batch+"-child" || msg.Events[1].Attempt != 2 {
			t.Fatalf("lost identity: %#v", msg)
		}
	}
	if got := buildSubTaskExecutionMessage(nil, nil).Status; got != "interrupted" {
		t.Fatal(got)
	}
	if got := buildSubTaskExecutionMessage(nil, map[string]any{"error": "invalid schema"}).Status; got != "error" {
		t.Fatal(got)
	}
}

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
	if err := index.PrimeTools(context.Background(), []tool.Tool{askUser}, nil); err != nil {
		t.Fatalf("PrimeTools: %v", err)
	}
	bridge, err := agent.NewProgressiveToolBridge(index)
	if err != nil {
		t.Fatalf("create progressive bridge: %v", err)
	}
	for _, kind := range []string{"select", "text"} {
		t.Run(kind, func(t *testing.T) {
			input := map[string]any{"kind": kind, "prompt": "Who is this deck for?"}
			if kind == "select" {
				input["options"] = []any{map[string]any{"id": "students", "label": "Students"}}
				input["allow_custom_answer"] = true
			}
			mockLLM := NewMockLLM(ToolCallTurn("execute_tool", map[string]any{"name": "ask_user", "arguments": input}))
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
			assertEventData(t, question, "kind", kind)
			if kind == "select" && question.Data["allow_custom_answer"] != true {
				t.Fatal("custom flag lost in SSE")
			}
			data, _ := json.Marshal(question.Data)
			history := tryParseChatQuestionMessage(chatQuestionPrefix + string(data))
			saved, err := env.SessionService.Get(context.Background(), &session.GetRequest{AppName: studioChatAppName, UserID: studioChatUserID, SessionID: env.Runner.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for event := range saved.Session.Events().All() {
				if event.Content == nil {
					continue
				}
				for _, part := range event.Content.Parts {
					if strings.HasPrefix(part.Text, chatQuestionPrefix) {
						restored := tryParseChatQuestionMessage(part.Text)
						if restored == nil || restored.QuestionKind != kind || !reflect.DeepEqual(restored.AllowCustomAnswer, history.AllowCustomAnswer) {
							t.Fatalf("bad persistence: %#v", restored)
						}
						found = true
					}
				}
			}
			if !found {
				t.Fatal("question not persisted")
			}
			if history == nil || history.QuestionKind != kind || (kind == "select" && (history.AllowCustomAnswer == nil || !*history.AllowCustomAnswer)) {
				t.Fatalf("history mismatch: %#v", history)
			}
		})
	}
}
