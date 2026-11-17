package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotHomeNarrativeHistoricalTelemetry(t *testing.T) {
	home, run := uuid.New(), uuid.New()
	for _, kind := range []string{"tool_call", "tool_result", "thinking", "subtask_progress"} {
		t.Run(kind, func(t *testing.T) {
			e := &session.Event{ID: "saved-row", Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{Type: kind, Payload: json.RawMessage(`{"name":"web_fetch","args":{"url":"PRIVATE_URL"}}`)}}}}
			before, _ := json.Marshal(e)
			got := projectBotHomeNarrative(e, home, home)
			raw, _ := json.Marshal(got)
			after, _ := json.Marshal(e)
			if got.ID != e.ID || strings.Contains(string(raw), "PRIVATE_URL") || string(before) != string(after) {
				t.Fatalf("unsafe or destructive projection: %s", raw)
			}
			if projectBotHomeNarrative(e, home, run) != e {
				t.Fatal("run diagnostics changed")
			}
		})
	}
}

func TestBotHomeNarrativeKeepsSpeechQuestionsAndPermissions(t *testing.T) {
	home := uuid.New()
	for _, kind := range []string{"chat_question", "approval", "plan_approval", "bot_review"} {
		e := &session.Event{Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{Type: kind, Payload: json.RawMessage(`{"prompt":"Which assignment?"}`)}}}}
		if !reflect.DeepEqual(projectBotHomeNarrative(e, home, home), e) {
			t.Fatalf("lost %s", kind)
		}
	}
	e := &session.Event{ID: "ack", Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{Type: "text", Payload: json.RawMessage(`{"text":"I will check the sources."}`)}}}}
	got := projectBotHomeNarrative(e, home, home)
	if got.Content == nil || got.Content.Parts[0].Text != "I will check the sources." || len(got.Actions.StateDelta) != 0 || e.Content != nil {
		t.Fatal("narration not projected as complete speech")
	}
	e = &session.Event{ID: "mixed", Author: "chat", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "Visible acknowledgement"}, {FunctionCall: &genai.FunctionCall{Name: "web_search"}}, {FunctionCall: &genai.FunctionCall{Name: "ask_user"}}, {Text: "PRIVATE_THOUGHT", Thought: true}}}}}
	got = projectBotHomeNarrative(e, home, home)
	if len(got.Content.Parts) != 2 || got.Content.Parts[0].Text != "Visible acknowledgement" || got.Content.Parts[1].FunctionCall.Name != "ask_user" || len(e.Content.Parts) != 4 {
		t.Fatal("mixed projection lost speech/question or mutated history")
	}
}
