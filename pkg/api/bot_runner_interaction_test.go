package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotInteractionAnswerCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]*session.Event)
	}{
		{name: "valid"},
		{name: "valid_choice"},
		{name: "wrong_call", mutate: func(e []*session.Event) { e[1].Content.Parts[0].FunctionResponse.ID = "other" }},
		{name: "wrong_invocation", mutate: func(e []*session.Event) { e[1].InvocationID = "other" }},
		{name: "changed_payload", mutate: func(e []*session.Event) { e[1].Content.Parts[0].FunctionResponse.Response["prompt"] = "changed" }},
		{name: "later_user", mutate: func(e []*session.Event) { e[2].Author = "user" }},
		{name: "later_tool", mutate: func(e []*session.Event) {
			e[2].Content.Parts = []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "later", Name: "read_file"}}}
		}},
		{name: "missing_response", mutate: func(e []*session.Event) { e[1].Content = nil }},
		{name: "duplicate_response", mutate: func(e []*session.Event) { *e[2] = *e[1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ss := session.InMemoryService()
			s, err := ss.Create(ctx, &session.CreateRequest{AppName: "test", UserID: "owner", SessionID: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			events := []*session.Event{
				{ID: uuid.NewString(), Author: "chat", InvocationID: "invocation", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call", Name: "ask_user"}}}}}},
				{ID: uuid.NewString(), Author: "chat", InvocationID: "invocation", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "call", Name: "ask_user", Response: map[string]any{"status": "ok", "questionId": "q", "kind": "yesno", "prompt": "Continue?"}}}}}}},
				{ID: uuid.NewString(), Author: "model", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("saved card", "model")}},
			}
			payload := json.RawMessage(`{"questionId":"q","kind":"yesno","prompt":"Continue?"}`)
			expectedLabel := "no"
			if tc.name == "valid_choice" {
				payload = json.RawMessage(`{"questionId":"q","kind":"select","prompt":"Continue?","options":[{"id":"no","label":"Not now"}]}`)
				expectedLabel = "Not now"
				var response map[string]any
				if err := json.Unmarshal(payload, &response); err != nil {
					t.Fatal(err)
				}
				response["status"] = "ok"
				events[1].Content.Parts[0].FunctionResponse.Response = response
			}
			if tc.mutate != nil {
				tc.mutate(events)
			}
			for _, e := range events {
				if err := ss.AppendEvent(ctx, s.Session, e); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ss.Get(ctx, &session.GetRequest{AppName: "test", UserID: "owner", SessionID: s.Session.ID()})
			if err != nil {
				t.Fatal(err)
			}
			a := &store.BotApproval{ID: uuid.New(), Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: payload}, Response: &store.BotInteractionResponse{QuestionID: "q", Answer: "no"}}
			before, err := json.Marshal(got.Session.Events().At(1).Content)
			if err != nil {
				t.Fatal(err)
			}
			message, err := botInteractionAnswerMessage(got.Session.Events(), a)
			after, marshalErr := json.Marshal(got.Session.Events().At(1).Content)
			if marshalErr != nil || string(before) != string(after) {
				t.Fatal("answer rewrote the original tool response")
			}
			if tc.name != "valid" && tc.name != "valid_choice" {
				if err == nil {
					t.Fatal("unsafe transcript accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var answer map[string]json.RawMessage
			if message.Role != "user" || json.Unmarshal([]byte(message.Parts[0].Text), &answer) != nil || string(answer["type"]) != `"bot_owner_answer"` || string(answer["tool_call_id"]) != `"call"` || string(answer["invocation_id"]) != `"invocation"` || string(answer["approval_id"]) != `"`+a.ID.String()+`"` || !strings.Contains(string(answer["response"]), `"answer":"no"`) || !strings.Contains(string(answer["display"]), `"prompt":"Continue?"`) {
				t.Fatalf("lost correlation: %+v", message)
			}
			var display map[string]string
			if json.Unmarshal(answer["display"], &display) != nil || display["answer"] != expectedLabel || string(answer["tool"]) != `"ask_user"` || !strings.Contains(string(answer["response"]), `"question_id":"q"`) {
				t.Fatalf("lost display label or raw answer identifiers: %s", message.Parts[0].Text)
			}
		})
	}
}
